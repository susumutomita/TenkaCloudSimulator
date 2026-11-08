"""Execute the selected release binary with an empty PATH and persistent state.

Uses only Python's standard library. All HTTP calls target the spawned loopback
server. This is startup/auth/storage/embedded-asset smoke, not full SDK/browser QA.
"""
import argparse
import contextlib
import json
import os
from pathlib import Path
import platform
import signal
import socket
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def request(endpoint, path, token='', data=None, target=None):
    headers = {'X-Simulator-Token': token}
    if target:
        headers.update({'Content-Type': 'application/x-amz-json-1.1',
                        'X-Amz-Target': 'AmazonSSM.' + target})
    req = urllib.request.Request(endpoint + path, headers=headers,
                                 data=None if data is None else json.dumps(data).encode())
    # Never use inherited proxy settings for the private loopback fixture.
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=5) as response:
        return response.status, response.read()


@contextlib.contextmanager
def running(binary, directory):
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    endpoint = f'http://127.0.0.1:{port}'
    token_file = directory / 'operator-token'
    env = {'PATH': '/nonexistent', 'CI': 'true', 'HOME': str(directory)}
    process = subprocess.Popen([str(binary), '--listen', f'127.0.0.1:{port}',
                                '--state', str(directory / 'state.db'),
                                '--token-file', str(token_file)],
                               cwd=directory, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, text=True)
    token = ''
    try:
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise AssertionError('release binary exited before readiness')
            try:
                token = token_file.read_text().strip()
                if request(endpoint, '/_simulator/health', token)[0] == 200:
                    break
            except (OSError, urllib.error.URLError):
                time.sleep(.1)
        else:
            raise AssertionError('release binary did not become ready')
        assert len(token) >= 32
        assert stat.S_IMODE(token_file.stat().st_mode) == 0o600
        try:
            request(endpoint, '/_simulator/health')
        except urllib.error.HTTPError as error:
            assert error.code == 401, error.code
        else:
            raise AssertionError('unauthenticated access unexpectedly succeeded')
        yield endpoint, token
    finally:
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
        try:
            stdout, stderr = process.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate()
            raise AssertionError('release binary did not stop gracefully')
        # Do not print process output, which could contain a fixture credential.
        assert not token or (token not in stdout and token not in stderr), 'token leaked to process output'
        assert process.returncode == 0, f'release binary exit code: {process.returncode}'


def smoke(binary):
    binary = Path(binary).resolve(strict=True)
    binary.chmod(0o700)  # Artifact transport does not retain executable permission.
    with tempfile.TemporaryDirectory(prefix='simulator-release-smoke-') as temporary:
        directory = Path(temporary)
        with running(binary, directory) as (endpoint, token):
            initial_token = token
            for path in ('/_simulator/', '/_simulator/console/', '/_simulator/docs/'):
                status, body = request(endpoint, path, token)
                assert status == 200 and b'<html' in body.lower(), path
            request(endpoint, '/', token, {'Name': '/release-smoke', 'Type': 'String',
                                          'Value': 'persisted-dummy-value'}, 'PutParameter')
        with running(binary, directory) as (endpoint, token):
            assert token == initial_token, 'restart changed the operator token'
            _, body = request(endpoint, '/', token, {'Name': '/release-smoke'}, 'GetParameter')
            assert json.loads(body)['Parameter']['Value'] == 'persisted-dummy-value'
            request(endpoint, '/', token, {'Name': '/release-smoke'}, 'DeleteParameter')
    print(f'Native release smoke passed on {platform.system()}/{platform.machine()}: '
          'authentication, embedded HTML, persisted state/token restart and graceful shutdown.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    smoke(parser.parse_args().binary)
