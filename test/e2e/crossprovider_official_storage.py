"""SOURCE CANDIDATE: real official Python SDKs against two authenticated local CLIs.
No SDK install, skip, mocked client, cloud credential discovery, or cloud endpoint.
Run manually only after the integration owner grants an execution window.
"""
import argparse
import base64
import contextlib
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit

KEYS = ('a/b', 'a%2Fb', 'a//b', 'a/./b', 'a/../b', 'a+b', 'a b',
        '日本語/雪', 'a%252Fb', '/leading', 'tail/', 'a/acl/entity')
PROJECT = 'tenkacloud-local'
ACCOUNT = 'tenkacloudlocal'


class MissingSDK(RuntimeError):
    def __init__(self, packages):
        self.packages = packages
        super().__init__('Required official SDK packages are absent')


def bounded_items(items, limit=100):
    result = []
    for item in items:
        if len(result) >= limit:
            raise AssertionError('SDK list exceeded finite acceptance bound')
        result.append(item)
    return result


def require_sdks(selected):
    names = {'gcp': ('google-cloud-storage', 'google-auth', 'requests'),
             'azure': ('azure-storage-blob', 'azure-core', 'requests')}
    versions, missing = {}, []
    for name in sorted({n for p in selected for n in names[p]}):
        try:
            versions[name] = importlib.metadata.version(name)
        except importlib.metadata.PackageNotFoundError:
            missing.append(name)
    if missing:
        raise MissingSDK(missing)
    return versions


@contextlib.contextmanager
def local_network_only(ports):
    """No DNS, proxies, redirects to outside endpoints, or metadata access.

    Guard applies to SDK/health/control traffic in this single-threaded harness.
    The actual SDK serializers and HTTP transports remain in use, unchanged.
    """
    connect, lookup = socket.socket.connect, socket.getaddrinfo
    saved = {k: v for k, v in os.environ.items() if 'proxy' in k.lower()}
    for key in saved:
        os.environ.pop(key, None)

    def checked_connect(sock, address):
        if not isinstance(address, tuple) or address[0] != '127.0.0.1' or address[1] not in ports:
            raise RuntimeError('Non-approved network destination refused')
        return connect(sock, address)

    def checked_lookup(host, port, *args, **kwargs):
        if host != '127.0.0.1' or int(port) not in ports:
            raise RuntimeError('DNS/non-approved destination refused')
        return lookup(host, port, *args, **kwargs)

    socket.socket.connect, socket.getaddrinfo = checked_connect, checked_lookup
    try:
        yield
    finally:
        socket.socket.connect, socket.getaddrinfo = connect, lookup
        os.environ.update(saved)


def control(url, token, path, data):
    request = urllib.request.Request(url + path, data=json.dumps(data).encode(),
                                    headers={'X-Simulator-Token': token, 'Content-Type': 'application/json'},
                                    method='POST')
    with urllib.request.urlopen(request, timeout=3) as response:
        return json.load(response)


class Team:
    def __init__(self, binary, directory, port):
        self.binary, self.directory, self.port = binary, Path(directory), port
        self.url = f'http://127.0.0.1:{port}'
        self.process = None
        self.log = None

    def start(self):
        self.log = tempfile.TemporaryFile()  # never print token-bearing CLI output
        env = {k: v for k, v in os.environ.items() if 'proxy' not in k.lower()}
        env['PATH'] = '/nonexistent'
        self.process = subprocess.Popen(
            [str(self.binary), '--listen', f'127.0.0.1:{self.port}', '--state',
             str(self.directory / 'state.sqlite'), '--token-file', str(self.directory / 'token')],
            cwd=self.directory, env=env, stdin=subprocess.DEVNULL, stdout=self.log, stderr=self.log)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError('CLI exited before readiness (output withheld)')
            try:
                self.token = (self.directory / 'token').read_text().strip()
                req = urllib.request.Request(self.url + '/_simulator/health',
                                             headers={'X-Simulator-Token': self.token})
                with urllib.request.urlopen(req, timeout=1) as response:
                    if response.status == 200:
                        return
            except (OSError, ValueError):
                time.sleep(.05)
        raise TimeoutError('CLI readiness exceeded fixed 25s')

    def stop(self):
        if self.process is None:
            return
        if self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
        try:
            code = self.process.wait(timeout=8)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
            raise AssertionError('CLI failed graceful shutdown') from None
        finally:
            self.log.close()
            self.process = None
        assert code == 0, 'CLI failed graceful shutdown'

    def participant(self):
        return control(self.url, self.token, '/_simulator/v1/access', {'Principal': 'storage-reader'})['AccessToken']


def expect_status(status, operation):
    try:
        operation()
    except Exception as error:
        actual = getattr(error, 'status_code', None)
        if actual is None:
            actual = getattr(error, 'code', None)
            if callable(actual):
                actual = actual()
        assert actual == status, f'Expected HTTP {status}; got {actual} ({type(error).__name__})'
    else:
        raise AssertionError(f'Expected HTTP {status}; request succeeded')


def gcp_client(team, token):
    from google.auth.credentials import AnonymousCredentials
    from google.cloud import storage
    return storage.Client(project=PROJECT, credentials=AnonymousCredentials(),
                          client_options={'api_endpoint': team.url + '/_simulator/providers/gcp'},
                          extra_headers={'X-Simulator-Token': token} if token else {})


def azure_client(team, token):
    from azure.storage.blob import BlobServiceClient
    def hook(request):
        parsed = urlsplit(request.http_request.url)
        assert parsed.hostname == '127.0.0.1' and parsed.port == team.port
        if token:
            request.http_request.headers['X-Simulator-Token'] = token
    return BlobServiceClient(
        account_url=team.url + '/_simulator/providers/azure/' + ACCOUNT,
        credential=None, api_version='2023-11-03', raw_request_hook=hook,
        retry_total=0, connection_timeout=3, read_timeout=3)


def gcp_create(team, label):
    c = gcp_client(team, team.token)
    for name, location in (('sdk-exact-us', 'US'), ('sdk-exact-eu', 'EU')):
        bucket = c.create_bucket(name, location=location, timeout=3, retry=None)
        bucket.reload(projection='noAcl', timeout=3, retry=None)
        assert bucket.location == location
    b = c.bucket('sdk-exact-us')
    for key in KEYS:
        value = (label + ':' + key).encode()
        blob = b.blob(key)
        blob.upload_from_string(value, content_type='application/octet-stream',
                                if_generation_match=0, checksum='md5', timeout=3, retry=None)
        assert blob.md5_hash == base64.b64encode(hashlib.md5(value).digest()).decode()
    probe = b.blob(KEYS[0]); probe.reload(projection='noAcl', timeout=3, retry=None)
    generation = int(probe.generation)
    expect_status(412, lambda: probe.upload_from_string(b'forbidden', if_generation_match=0,
                                                       checksum='md5', timeout=3, retry=None))
    probe = b.blob(KEYS[0]); probe.reload(projection='noAcl', timeout=3, retry=None)
    assert int(probe.generation) == generation
    probe = b.blob(KEYS[0])
    probe.upload_from_string((label + ':' + KEYS[0]).encode(), if_generation_match=generation,
                             checksum='md5', timeout=3, retry=None)
    assert int(probe.generation) > generation
    expect_status(412, lambda: b.blob(KEYS[0]).delete(if_generation_match=generation, timeout=3, retry=None))
    reader = gcp_client(team, team.participant())
    expect_status(403, lambda: reader.bucket('sdk-exact-us').blob('reader-write').upload_from_string(
        b'denied', checksum='md5', timeout=3, retry=None))
    expect_status(401, lambda: bounded_items(gcp_client(team, '').list_buckets(timeout=3, retry=None)))
    c.close(); reader.close()


def gcp_verify_delete(team, label):
    c = gcp_client(team, team.token); b = c.bucket('sdk-exact-us')
    assert set(x.name for x in bounded_items(b.list_blobs(projection='noAcl', page_size=2, timeout=3, retry=None))) == set(KEYS)
    for key in KEYS:
        blob = b.blob(key)
        assert blob.download_as_bytes(checksum='md5', timeout=3, retry=None) == (label + ':' + key).encode()
        blob = b.blob(key)
        blob.reload(projection='noAcl', timeout=3, retry=None)
        b.blob(key).delete(if_generation_match=int(blob.generation), timeout=3, retry=None)
        expect_status(404, lambda: b.blob(key).download_as_bytes(timeout=3, retry=None))
    b.delete(timeout=3, retry=None); c.bucket('sdk-exact-eu').delete(timeout=3, retry=None)
    assert bounded_items(c.list_buckets(timeout=3, retry=None)) == []
    c.close()


def azure_create(team, label):
    from azure.core import MatchConditions
    from azure.storage.blob import ContentSettings
    with azure_client(team, team.token) as service:
        container = service.create_container('sdk-exact')
        for key in KEYS:
            value = (label + ':' + key).encode()
            container.get_blob_client(key).upload_blob(
                value, overwrite=False, content_settings=ContentSettings(
                    content_type='application/octet-stream', content_md5=hashlib.md5(value).digest()))
        probe = container.get_blob_client(KEYS[0]); before = probe.get_blob_properties()
        expect_status(412, lambda: probe.upload_blob(b'forbidden', overwrite=True,
                      etag='"not-current"', match_condition=MatchConditions.IfNotModified))
        assert probe.get_blob_properties().etag == before.etag
        probe.upload_blob((label + ':' + KEYS[0]).encode(), overwrite=True, etag=before.etag,
                          match_condition=MatchConditions.IfNotModified,
                          content_settings=ContentSettings(content_type='application/octet-stream',
                          content_md5=hashlib.md5((label + ':' + KEYS[0]).encode()).digest()))
        assert probe.get_blob_properties().etag != before.etag
        expect_status(412, lambda: probe.delete_blob(etag=before.etag,
                      match_condition=MatchConditions.IfNotModified))
    with azure_client(team, team.participant()) as reader:
        expect_status(403, lambda: reader.get_blob_client('sdk-exact', 'reader-write').upload_blob(b'denied'))
    with azure_client(team, '') as anonymous:
        expect_status(401, lambda: bounded_items(anonymous.list_containers()))


def azure_verify_delete(team, label):
    from azure.core import MatchConditions
    with azure_client(team, team.token) as service:
        container = service.get_container_client('sdk-exact')
        assert set(x.name for x in bounded_items(container.list_blobs(results_per_page=2))) == set(KEYS)
        for key in KEYS:
            blob = container.get_blob_client(key); properties = blob.get_blob_properties()
            # Real SDK download emits Range. Current leaf rejects it; that is a
            # compatibility failure, never strip its header or replace with raw HTTP.
            data = blob.download_blob(max_concurrency=1).readall()
            assert data == (label + ':' + key).encode()
            assert bytes(properties.content_settings.content_md5) == hashlib.md5(data).digest()
            blob.delete_blob(etag=properties.etag, match_condition=MatchConditions.IfNotModified)
            expect_status(404, blob.get_blob_properties)
        container.delete_container()
        assert bounded_items(service.list_containers()) == []


def run(binary, selected):
    versions = require_sdks(selected)  # before processes, sockets, or network
    with tempfile.TemporaryDirectory(prefix='sdk-two-teams-') as directory:
        ports = []
        for _ in range(2):
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', 0)); ports.append(sock.getsockname()[1])
        assert len(set(ports)) == 2, 'Port collision: retry whole harness, not assertions'
        teams = []
        for index, port in enumerate(ports):
            path = Path(directory) / str(index); path.mkdir()
            teams.append(Team(binary, path, port))
        with local_network_only(set(ports)):
            try:
                for team in teams: team.start()
                for provider in selected:
                    factory = gcp_client if provider == 'gcp' else azure_client
                    for own, other in ((teams[0], teams[1]), (teams[1], teams[0])):
                        client = factory(other, own.token)
                        if provider == 'gcp':
                            expect_status(401, lambda: bounded_items(client.list_buckets(timeout=3, retry=None)))
                        else:
                            expect_status(401, lambda: bounded_items(client.list_containers()))
                        client.close()
                    create = gcp_create if provider == 'gcp' else azure_create
                    for index, team in enumerate(teams): create(team, f'team-{index}')
                tokens = [team.token for team in teams]
                for team in teams: team.stop()
                for index, team in enumerate(teams):
                    team.start(); assert team.token == tokens[index]
                for provider in selected:
                    verify = gcp_verify_delete if provider == 'gcp' else azure_verify_delete
                    for index, team in enumerate(teams): verify(team, f'team-{index}')
            finally:
                failures = []
                for team in teams:
                    try: team.stop()
                    except Exception as error: failures.append(type(error).__name__)
                if failures: raise AssertionError('Process cleanup failed: ' + ','.join(failures))
    return {'providers': selected, 'sdkVersions': versions, 'result': 'PASS'}


if __name__ == '__main__':
    if not __debug__:
        raise SystemExit('Acceptance requires enabled assertions; do not use -O')
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--provider', choices=('gcp', 'azure', 'both'), default='both')
    args = parser.parse_args()
    selected = ['gcp', 'azure'] if args.provider == 'both' else [args.provider]
    def deadline_expired(*_):
        raise TimeoutError('Whole acceptance exceeded fixed 300s')
    signal.signal(signal.SIGALRM, deadline_expired)
    signal.alarm(300)
    try:
        result = run(args.binary.resolve(strict=True), selected)
    except MissingSDK as error:
        print(json.dumps({'result': 'FAIL/NOT-RUN', 'missingOfficialSDKs': error.packages}))
        raise SystemExit(2) from None
    except Exception as error:
        # SDK exception strings may contain request headers or response data.
        # Keep failure visible while withholding potentially sensitive details.
        print(json.dumps({'result': 'FAIL/NOT-ACCEPTED', 'errorType': type(error).__name__}))
        raise SystemExit(1) from None
    signal.alarm(0)
    print(json.dumps(result))
