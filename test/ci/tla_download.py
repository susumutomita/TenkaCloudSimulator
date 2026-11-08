"""Exercise configured TLC transport with real curl against owned loopback only.

No production URL override is added. Only this test substitutes a fixture URL.
The canonical formal checker rejects dummy bytes before starting Java.
"""
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import unittest

ROOT = Path(__file__).resolve().parents[2]
URL = 'https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar'
WORKFLOWS = ('.github/workflows/ci.yml', '.github/workflows/formal.yml')


def configured_command():
    commands = []
    for path in WORKFLOWS:
        lines = (ROOT / path).read_text().splitlines()
        matching = [i for i, line in enumerate(lines) if URL in line]
        if len(matching) != 1:
            raise AssertionError(f'{path}: one literal pinned download required')
        index = matching[0]
        if shlex.split(lines[index - 1].strip()) != ['python3', 'test/ci/tla_download.py']:
            raise AssertionError(f'{path}: local transport suite must precede download')
        commands.append(shlex.split(lines[index].strip()))
        following = shlex.split(lines[index + 1].strip())
        if following[0] != 'TLA_JAR=/tmp/tla2tools.jar':
            raise AssertionError(f'{path}: canonical formal invocation must follow download')
        if path.endswith('formal.yml'):
            if following[1:] != ['python3', 'scripts/check-formal.py', '--merge-gate']:
                raise AssertionError('immutable formal checker changed')
        elif following[1:] != ['make', 'formal']:
            raise AssertionError('CI formal checker changed')
    if commands[0] != commands[1]:
        raise AssertionError('workflow download commands drifted')
    return commands[0]


class DownloadTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix='tla-curl-fixture-')
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name)
        self.calls = []
        self.release_slow = threading.Event()
        self.handler_threads = []
        calls = self.calls
        release_slow = self.release_slow
        handler_threads = self.handler_threads

        class Handler(BaseHTTPRequestHandler):
            def do_GET(inner):
                handler_threads.append(threading.current_thread())
                calls.append(inner.path)
                if inner.path == '/slow':
                    release_slow.wait(timeout=3)
                if inner.path == '/recover':
                    status = 504 if len(calls) == 1 else 200
                elif inner.path == '/exhaust':
                    status = 504
                elif inner.path == '/denied':
                    status = 403
                elif inner.path == '/bad-digest':
                    status = 200
                else:
                    status = 404
                body = b'local fixture, not a jar'
                inner.send_response(status)
                inner.send_header('Content-Length', str(len(body)))
                inner.end_headers()
                try:
                    inner.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    if inner.path != '/slow':
                        raise

            def log_message(inner, *_args):
                pass

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.close_server)

    def close_server(self):
        self.release_slow.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        self.assertFalse(self.thread.is_alive())
        for handler in self.handler_threads:
            handler.join(timeout=2)
            self.assertFalse(handler.is_alive())

    def download(self, route, shortened_outer_timeout=None):
        command = configured_command()
        if shortened_outer_timeout is not None:
            self.assertEqual(shortened_outer_timeout, '0.2s')
            command[command.index('95s')] = shortened_outer_timeout
        # Preserve every configured retry/deadline flag. Only fixture URL/output
        # change; disable user curlrc/proxy so this can never contact an upstream.
        command[command.index(URL)] = f'http://127.0.0.1:{self.server.server_port}{route}'
        command[command.index('--output') + 1] = str(self.path / 'download.fixture')
        position = command.index('curl')
        command[position + 1:position + 1] = ['--disable', '--noproxy', '127.0.0.1']
        return subprocess.run(command, capture_output=True, text=True, timeout=102)

    def test_configured_bounds_and_pins(self):
        self.assertEqual(configured_command(), [
            'timeout', '--kill-after=5s', '95s', 'curl', '--fail', '--location',
            '--retry', '3', '--retry-delay', '2', '--retry-max-time', '60',
            '--connect-timeout', '10', '--max-time', '30', URL,
            '--output', '/tmp/tla2tools.jar'])
        self.assertEqual((ROOT / 'formal/tla2tools.sha256').read_text().split()[0],
                         '936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88')

    def test_504_then_200_retries_same_url_and_saves_final_bytes(self):
        result = self.download('/recover')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls, ['/recover', '/recover'])
        self.assertEqual((self.path / 'download.fixture').read_bytes(), b'local fixture, not a jar')

    def test_403_is_not_retried(self):
        result = self.download('/denied')
        self.assertEqual(result.returncode, 22, result.stderr)
        self.assertEqual(self.calls, ['/denied'])

    def test_exhausted_504_fails_after_four_requests(self):
        result = self.download('/exhaust')
        self.assertEqual(result.returncode, 22, result.stderr)
        self.assertEqual(self.calls, ['/exhaust'] * 4)

    def test_outer_timeout_stops_a_blocked_request(self):
        # Only the fixture argv's outer deadline is shortened. Production keeps
        # 95s/kill5, and the actual retry/connect/transfer flags remain unchanged.
        import time
        start = time.monotonic()
        result = self.download('/slow', shortened_outer_timeout='0.2s')
        elapsed = time.monotonic() - start
        self.assertEqual(result.returncode, 124, result.stderr)
        self.assertEqual(self.calls, ['/slow'])
        self.assertLess(elapsed, 2)

    def test_wrong_digest_is_rejected_by_canonical_checker_without_retry(self):
        result = self.download('/bad-digest')
        self.assertEqual(result.returncode, 0, result.stderr)
        environment = dict(os.environ, TLA_JAR=str(self.path / 'download.fixture'),
                           PYTHONPYCACHEPREFIX=str(self.path / 'pycache'))
        checked = subprocess.run([sys.executable, 'scripts/check-formal.py',
                                  '--reports', str(self.path / 'formal-reports')],
                                 cwd=ROOT, env=environment, capture_output=True,
                                 text=True, timeout=15)
        self.assertEqual(checked.returncode, 1, checked.stderr)
        self.assertIn('TLC jar checksum mismatch', checked.stderr)
        evidence = json.loads((self.path / 'formal-reports/evidence.json').read_text())
        self.assertFalse(evidence['success'])
        self.assertEqual(evidence['runs'], [])
        self.assertNotIn('tool_version', evidence)
        self.assertEqual(self.calls, ['/bad-digest'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
