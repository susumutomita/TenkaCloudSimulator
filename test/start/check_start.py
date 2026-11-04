"""Real make start / CLI startup checks; temporary tokens never enter reports.

Development-only Python standard library. Does not invoke any Lambda/Python guest.
"""
import contextlib
import json
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]
COMPILER = shutil.which(os.environ.get('GO', 'go'))
if not COMPILER:
    raise SystemExit('Startup checks require a real Go compiler (set GO=/path/to/go).')
REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
REPORTS.mkdir(parents=True, exist_ok=True)


def check(condition, message):
    # Do not use assertEqual/assertIn with token/output operands: a failing
    # test framework must not accidentally include secret values in CI logs.
    if not condition:
        raise AssertionError(message)


def request(endpoint, path='/', token=None, method='GET'):
    headers = {'X-Simulator-Token': token} if token else {}
    req = urllib.request.Request(endpoint + path, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=3) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


@contextlib.contextmanager
def launch(command, cwd, env, terminal=False, stop_signal=signal.SIGTERM):
    master = None
    if terminal:
        master, slave = pty.openpty()
        process = subprocess.Popen(command, cwd=cwd, env=env, stdout=slave,
                                   stderr=slave, start_new_session=True)
        os.close(slave)
        stream = master
    else:
        process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        stream = process.stdout.fileno()
    output = bytearray()
    endpoint = None
    try:
        deadline = time.monotonic() + 90
        while not endpoint:
            check(process.poll() is None, 'startup exited before readiness (output withheld)')
            check(time.monotonic() < deadline, 'startup did not become ready')
            if select.select([stream], [], [], .1)[0]:
                block = os.read(stream, 8192)
                output.extend(block)
                for line in bytes(output).decode(errors='replace').splitlines():
                    if line.startswith('AWS simulation endpoint '):
                        endpoint = line.split('AWS simulation endpoint ', 1)[1].strip()
        yield endpoint, bytes(output).decode(errors='replace')
    finally:
        if process.poll() is None:
            if stop_signal == signal.SIGINT:
                # Ctrl+C is delivered to the foreground process group. Sending
                # SIGINT only to make is not a terminal interrupt and GNU make
                # may wait for a child which never received that signal.
                os.killpg(process.pid, stop_signal)
            else:
                # Programmatic SIGTERM to make must stop its exec'd CLI child.
                process.send_signal(stop_signal)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
            raise AssertionError('startup left a child after shutdown') from None
        if master is not None:
            os.close(master)
        if endpoint:
            address = urllib.parse.urlsplit(endpoint)
            try:
                connection = socket.create_connection((address.hostname, address.port), timeout=.5)
            except OSError:
                pass
            else:
                connection.close()
                raise AssertionError('listener survived foreground shutdown')


def main():
    env = {**os.environ, 'GO': COMPILER}
    env.pop('CI', None)
    for name in ['MAKEFLAGS', 'MFLAGS', 'MAKEOVERRIDES', 'START_LISTEN', 'START_STATE', 'START_TOKEN_FILE']:
        env.pop(name, None)
    with tempfile.TemporaryDirectory(prefix='simulator-start-') as directory:
        home = Path(directory)
        repo = home / 'source tree'
        repo.mkdir()
        # Only build inputs, never .env/credentials/private worktree directories.
        for name in ['cmd', 'internal']:
            shutil.copytree(ROOT / name, repo / name,
                            ignore=shutil.ignore_patterns('__pycache__', 'node_modules', '*.db', '*.db-*',
                                '.env*', '.aws', '.agents', '.codex', '.claude', '.git',
                                'simulator-token', 'operator-token'))
        for name in ['go.mod', 'go.sum', 'Makefile']:
            shutil.copy2(ROOT / name, repo / name)
        (repo / 'scripts').mkdir()
        shutil.copy2(ROOT / 'scripts/start.sh', repo / 'scripts/start.sh')
        state = home / 'private data' / 'practice.db'
        token_file = home / 'private data' / 'operator token'
        command = ['make', '--no-print-directory', 'start', 'START_LISTEN=127.0.0.1:0',
                   'START_STATE=' + str(state), 'START_TOKEN_FILE=' + str(token_file)]
        check(not (repo / 'dist').exists(), 'unbuilt test was accidentally prebuilt')
        # A leftover cross-build environment must not make start execute a foreign binary.
        with launch(command, repo, {**env, 'GOOS': 'windows', 'GOARCH': 'arm64'}) as (endpoint, output):
            token = token_file.read_text().strip()
            check(token not in output, 'redirected make start displayed operator token')
            check(token_file.stat().st_mode & 0o777 == 0o600, 'operator file is not private')
            check(state.stat().st_mode & 0o077 == 0, 'new state file is not private')
            check(request(endpoint)[0] == 401, 'startup disabled authentication')
            check(request(endpoint, '/start-persistence', token, 'PUT')[0] == 200, 'authenticated write failed')
        original_token = token_file.read_bytes()
        with launch(command, repo, env, stop_signal=signal.SIGINT) as (endpoint, output):
            check(token_file.read_bytes() == original_token, 'restart rotated existing token')
            check(token not in output, 'built restart displayed token in captured logs')
            check(b'start-persistence' in request(endpoint, token=token)[1], 'restart lost persisted state')
        # An already built checkout must rebuild changed source, rather than use
        # the existing dist silently. This changes a copy of our own source only.
        main_file = repo / 'cmd/simulator/main.go'
        original_source = main_file.read_text()
        check(original_source.count('TenkaCloud Simulator · independent local simulation') == 1,
              'startup test source marker moved')
        main_file.write_text(original_source.replace('TenkaCloud Simulator · independent local simulation',
                                                     'TenkaCloud Simulator · refreshed startup source'))
        with launch(command, repo, env) as (_, output):
            check('refreshed startup source' in output, 'start used a stale prebuilt binary')
            check(token not in output, 'captured source-refresh startup displayed token')
        for ci, displayed in [(None, True), ('true', False)]:
            current = {**env}
            if ci is not None:
                current['CI'] = ci
            with launch(command, repo, current, terminal=True) as (endpoint, output):
                check((token in output) == displayed, 'make start terminal/CI display policy failed')
                check(token_file.read_bytes() == original_token, 'make start display rotated token')
                check(request(endpoint, token=token)[0] == 200, 'make start terminal auth failed')
        before_binary = (repo / 'dist/tenkacloud-simulator').read_bytes()
        before_state = state.read_bytes()
        for kind in ['missing', 'failed', 'empty']:
            compiler = '/nonexistent/simulator-go' if kind == 'missing' else str(home / (kind + ' go'))
            if kind != 'missing':
                wrapper = Path(compiler)
                wrapper.write_text('#!/bin/sh\nexit ' + ('17' if kind == 'failed' else '0') + '\n')
                wrapper.chmod(0o700)
            result = subprocess.run(command, cwd=repo, env={**env, 'GO': compiler},
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
            check(result.returncode != 0, 'missing/failed compiler started an old executable')
            expected = {'missing': b'Go is required', 'failed': b'Build failed',
                        'empty': b'Go did not produce'}[kind]
            check(expected in result.stdout, 'compiler failure lacked readable guidance')
            check(not list((repo / 'dist').glob('.simulator-start.*')), 'failed build left staged executable')
            check(token.encode() not in result.stdout, 'compiler error path displayed token')
            check(token_file.read_bytes() == original_token, 'failed build changed existing token')
            check(state.read_bytes() == before_state, 'failed build changed existing state')
            check((repo / 'dist/tenkacloud-simulator').read_bytes() == before_binary, 'failed build replaced good binary')
        # Existing README paths remain the real defaults, relative to the source
        # checkout even when make -C is called from another working directory.
        defaults = ['make', '--no-print-directory', '-C', str(repo), 'start',
                    'START_LISTEN=127.0.0.1:0']
        with launch(defaults, home, env) as (endpoint, output):
            default_token = (repo / 'simulator-token').read_text().strip()
            check((repo / 'practice.db').is_file(), 'default state path changed')
            check(default_token not in output, 'default captured startup displayed token')
            check(request(endpoint, token=default_token)[0] == 200, 'default token cannot authenticate')
        binary = str(repo / 'dist/tenkacloud-simulator')
        direct = [binary, '--listen=127.0.0.1:0', '--token-file=' + str(token_file)]
        # The already built runtime needs no Go/Node/Python executable on PATH.
        runtime_env = {**env, 'PATH': '/nonexistent'}
        for terminal, ci, show, displayed in [(True, None, True, True), (True, None, False, False),
                                             (True, 'true', True, False), (True, '', True, False),
                                             (False, None, True, False)]:
            current = {**runtime_env}
            if ci is not None:
                current['CI'] = ci
            args = direct + (['--show-token'] if show else [])
            with launch(args, home, current, terminal=terminal) as (endpoint, output):
                check((token in output) == displayed, 'actual terminal/CI token display policy failed')
                check(token_file.read_bytes() == original_token, 'display changed saved token')
                check(request(endpoint)[0] == 401 and request(endpoint, token=token)[0] == 200,
                      'terminal display path changed authentication')
        result = {'realMakeStartUnbuiltAndBuilt': True, 'staleSourceRebuilt': True,
                  'compilerFailureNeverUsesStaleBinary': True, 'privatePersistentStateAndToken': True,
                  'spacesInOverridePaths': True, 'actualTTYOptIn': True,
                  'redirectAndCISuppression': True, 'authenticationRequired': True,
                  'SIGTERMAndSIGINTCleanup': True, 'noRuntimeExecutablesRequired': True,
                  'tokenValuesOmitted': True}
        (REPORTS / 'make-start.json').write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result))


if __name__ == '__main__':
    main()
