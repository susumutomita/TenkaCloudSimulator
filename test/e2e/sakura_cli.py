#!/usr/bin/env python3
"""Native HTTP composition probe, not a community SDK compatibility test.

Runs only an explicitly supplied local simulator binary and owned numeric-loopback
listeners, SQLite files and generated dummy token files. No cloud credentials.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import queue
import sqlite3
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = '/_simulator/providers/sakura/cloud/zone/is1a/api/cloud/1.1/disk'
BASIC = 'Basic ' + base64.b64encode(b'TCSIMSAKURA:dummy-local-only').decode()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        return None


class CLI:
    def __init__(self, binary, directory, environment, enabled=True):
        self.directory = Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True)
        self.db = self.directory / 'state.db'
        self.tokenfile = self.directory / 'token'
        args = [binary, '--listen', '127.0.0.1:0', '--state', str(self.db),
                '--token-file', str(self.tokenfile)]
        if enabled:
            args += ['--enable-sakura-disks', '--sakura-environment', environment,
                     '--sakura-project', 'practice', '--sakura-zone', 'is1a',
                     '--sakura-owner', 'learner']
        self.process = subprocess.Popen(args, stdout=subprocess.PIPE,
                                        stderr=subprocess.STDOUT, text=True,
                                        env={'PATH': os.defpath, 'CI': '1', 'GOMAXPROCS': '1'})
        self.lines = queue.Queue()
        self.token_leak = False
        self.token = ''
        self.thread = threading.Thread(target=self._drain, daemon=True)
        self.thread.start()
        self.calls = self.responses = 0
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        try:
            limit = time.monotonic() + 20
            while True:
                remaining = limit - time.monotonic()
                if remaining <= 0:
                    raise AssertionError('owned CLI readiness deadline')
                line = self.lines.get(timeout=remaining)
                if line is None:
                    raise AssertionError('owned CLI exited before readiness; output redacted')
                if line.startswith('Console http://'):
                    parsed = urllib.parse.urlsplit(line[len('Console '):])
                    assert parsed.scheme == 'http' and parsed.hostname == '127.0.0.1'
                    assert parsed.port and not parsed.username and not parsed.password
                    self.origin = 'http://127.0.0.1:' + str(parsed.port)
                    self.token = self.tokenfile.read_text().strip()
                    assert len(self.token) >= 32
                    break
        except BaseException:
            self.close()
            raise

    def _drain(self):
        for line in self.process.stdout:
            if self.token and self.token in line:
                self.token_leak = True
            # Only the readiness URL leaves the reader. Never expose raw output.
            if line.startswith('Console http://'):
                self.lines.put(line.strip())
        self.lines.put(None)

    def call(self, method, path, body=None, status=200, token=None, principal='learner'):
        assert path.startswith('/_simulator/') and not path.startswith('//')
        target = self.origin + path
        parsed = urllib.parse.urlsplit(target)
        assert parsed.scheme + '://' + parsed.netloc == self.origin
        assert parsed.hostname == '127.0.0.1' and parsed.port
        headers = {'Authorization': BASIC, 'Content-Type': 'application/json'}
        credential = self.token if token is None else token
        if credential:
            headers['X-Simulator-Token'] = credential
        if principal:
            headers['X-Simulator-Principal'] = principal
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(target, data=data, headers=headers, method=method)
        self.calls += 1
        try:
            response = self.opener.open(request, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            self.responses += 1
            assert response.status == status, 'native HTTP status %s wanted %s; body redacted' % (response.status, status)
            raw = response.read(131073)
            assert len(raw) <= 131072
            if 'application/json' in response.headers.get('Content-Type', ''):
                return json.loads(raw)
            return None

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        if hasattr(self, 'thread'):
            self.thread.join(timeout=2)
            assert not self.thread.is_alive(), 'owned stdout reader did not close'
        if self.process.stdout:
            self.process.stdout.close()
        assert not self.token_leak, 'generated dummy credential printed'


def frame(path):
    # Compare every actual saved table, never report raw state or credential bytes.
    with sqlite3.connect('file:' + str(path) + '?mode=ro', uri=True) as db:
        tables = sorted(row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'"))
        content = {}
        for name in tables:
            quoted = '"' + name.replace('"', '""') + '"'
            rows = db.execute('SELECT * FROM ' + quoted).fetchall()
            content[name] = sorted(repr(row) for row in rows)
    return hashlib.sha256(json.dumps(content, sort_keys=True).encode()).hexdigest()


def create(cli, name):
    value = cli.call('POST', BASE, {'Disk': {'Name': name, 'Plan': {'ID': 4}, 'SizeMB': 20480}}, 201)['Disk']
    assert value['Name'] == name and 'Availability' not in value
    return value['ID']


def run(binary, report):
    completed = []
    owned = []
    counts = []
    with tempfile.TemporaryDirectory(prefix='sakura-two-cli-') as directory:
        root = Path(directory)
        try:
            disabled = CLI(binary, root / 'a', 'team-A', False)
            owned.append(disabled)
            assert 'sakura' not in disabled.call('GET', '/_simulator/v1/capabilities')
            disabled.call('GET', BASE, status=404)
            disabled.close()
            completed.append('default-disabled')
            a = CLI(binary, root / 'a', 'team-A')
            b = CLI(binary, root / 'b', 'team-B')
            owned.extend([a, b])
            assert a.process.pid != b.process.pid and a.token != b.token
            aid, bid = create(a, 'A'), create(b, 'B')
            assert aid == bid == '1'
            peer = frame(b.db)
            ap = a.call('POST', '/_simulator/v1/access', {'Principal': 'learner'})['AccessToken']
            bp = b.call('POST', '/_simulator/v1/access', {'Principal': 'learner'})['AccessToken']
            other = a.call('POST', '/_simulator/v1/access', {'Principal': 'other'})['AccessToken']
            before = frame(a.db)
            for token in ['', b.token, bp]:
                a.call('GET', BASE + '/' + aid, status=401, token=token)
            a.call('GET', BASE + '/' + aid, token=ap)
            a.call('GET', BASE + '/' + aid, status=403, token=ap, principal='other')
            a.call('GET', BASE + '/' + aid, status=403, token=other, principal='other')
            a.call('GET', BASE + '/' + aid, status=403, principal='other')
            a.call('GET', BASE + '/' + aid, status=403, principal='')
            assert frame(a.db) == before and frame(b.db) == peer
            completed.append('separate-authorities-participant-owner-no-operator-bypass')
            a.call('PUT', BASE + '/' + aid, {'Disk': {'Name': 'A-updated', 'Description': 'saved'}})
            assert a.call('GET', BASE + '/' + aid)['Disk']['Name'] == 'A-updated'
            listed = a.call('GET', BASE + '?%7B%22From%22%3A0%2C%22Count%22%3A0%7D')
            assert len(listed['Disks']) == 1
            assert frame(b.db) == peer
            old_operator = a.token
            a.close()
            reopened = CLI(binary, root / 'a', 'team-A')
            owned.append(reopened)
            assert reopened.token == old_operator
            assert reopened.call('GET', BASE + '/' + aid)['Disk']['Description'] == 'saved'
            reopened.call('GET', BASE + '/' + aid, status=401, token=ap)
            assert b.call('GET', BASE + '/' + bid, token=bp)['Disk']['Name'] == 'B'
            completed.append('actual-process-restart-persistent-disk-and-token-ephemeral-participant')
            reopened.call('DELETE', BASE + '/' + aid)
            fresh = create(reopened, 'fresh')
            assert int(fresh) > int(aid)
            reopened.call('DELETE', BASE + '/' + fresh)
            assert reopened.call('GET', BASE)['Disks'] == []
            assert frame(b.db) == peer
            b.call('DELETE', BASE + '/' + bid)
            assert b.call('GET', BASE)['Disks'] == []
            completed.append('all-five-native-operations-fresh-id-peer-preservation-cleanup')
        finally:
            for cli in reversed(owned):
                cli.close()
                assert cli.calls == cli.responses and cli.calls > 0
                counts.append({'calls': cli.calls, 'responses': cli.responses})
    output = {'success': True, 'scope': 'native HTTP actual standalone CLI subprocesses; not SDK',
              'completed': completed, 'processCallCounts': counts,
              'unsupported': ['community SDK not executed', 'no VM/block I/O', 'no actual cloud', 'not mandatory-suite registered']}
    Path(report).write_text(json.dumps(output, indent=2) + '\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--report', required=True)
    arguments = parser.parse_args()
    run(str(Path(arguments.binary).resolve(strict=True)), arguments.report)
