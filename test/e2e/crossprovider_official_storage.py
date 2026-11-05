"""Real official Python storage SDKs against two authenticated local CLIs.
No SDK install, skip, mocked client, cloud credential discovery, or cloud endpoint.
Azure's two literal dot-segment exclusions are explicitly measured and reported;
the full exact-key oracle remains required for Google Cloud Storage.
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
import traceback
import urllib.error
import urllib.request
from urllib.parse import unquote, urlsplit

KEYS = ('a/b', 'a%2Fb', 'a//b', 'a/./b', 'a/../b', 'a+b', 'a b',
        '日本語/雪', 'a%252Fb', '/leading', 'tail/', 'a/acl/entity')
PROJECT = 'tenkacloud-local'
ACCOUNT = 'tenkacloudlocal'
# The official synchronous Azure SDK uses Requests, which normalizes these paths
# before HTTP transfer. Do not rewrite that transport or pretend the server saw
# the original keys. This explicit, independently asserted subset is not full
# native exact-key parity; all other KEYS must still survive without alteration.
AZURE_NORMALIZED_KEYS = {'a/./b': 'a/b', 'a/../b': 'b'}
AZURE_KEYS = tuple(key for key in KEYS if key not in AZURE_NORMALIZED_KEYS)


class AcceptanceFailure(RuntimeError):
    """Bounded diagnostics without exception text, tokens, headers, or bodies."""

    def __init__(self, phase, error):
        self.phase = phase
        self.error_type = type(error).__name__
        self.status_failure = error.diagnostic if isinstance(error, UnexpectedHTTPStatus) else None
        self.source = next(
            ({'file': Path(frame.filename).name, 'line': frame.lineno,
              'function': frame.name}
             for frame in reversed(traceback.extract_tb(error.__traceback__))
             if Path(frame.filename).resolve() == Path(__file__).resolve()), None)
        super().__init__('Official SDK acceptance failed')


class UnexpectedHTTPStatus(AssertionError):
    def __init__(self, expected, actual, error):
        # An exception's arbitrary text/attributes may include request secrets.
        # Only retain known numeric HTTP status values and the exception class.
        self.diagnostic = {'expected_http_status': expected,
                           'actual_http_status': actual if isinstance(actual, int) else None,
                           'sdk_error_type': type(error).__name__,
                           'sdk_frames': [
                               {'file': Path(frame.filename).name, 'line': frame.lineno,
                                'function': frame.name}
                               for frame in traceback.extract_tb(error.__traceback__)[-8:]
                           ]}
        super().__init__('Official SDK did not report the expected HTTP status')


@contextlib.contextmanager
def acceptance_phase(name):
    try:
        yield
    except AcceptanceFailure:
        raise
    except Exception as error:
        raise AcceptanceFailure(name, error) from None


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
        if actual != status:
            raise UnexpectedHTTPStatus(status, actual, error) from None
    else:
        raise AssertionError(f'Expected HTTP {status}; request succeeded')


def gcp_client(team, token):
    from google.auth.credentials import AnonymousCredentials
    from google.auth.transport.requests import AuthorizedSession
    from google.cloud import storage

    credentials = AnonymousCredentials()
    # These are local simulator tokens, not refreshable Google OAuth credentials.
    # The default AuthorizedSession refreshes on 401 and AnonymousCredentials
    # then raises InvalidOperation, hiding the original HTTP denial. Use the
    # official transport with refresh disabled; its serialization/error parsing
    # stay unchanged. Client's documented _http injection is version-pinned here.
    session = AuthorizedSession(credentials, refresh_status_codes=(), max_refresh_attempts=0)
    return storage.Client(project=PROJECT, credentials=credentials, _http=session,
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


def azure_sdk_key_contract():
    """Inspect real SDK + Requests preparation without sending any request.

    Unexpected normalization fails acceptance; exclusions are not discovered and
    silently applied. If the pinned SDK changes, review this contract explicitly.
    """
    from azure.storage.blob import BlobServiceClient
    from requests import Request

    prefix = f'/_simulator/providers/azure/{ACCOUNT}/sdk-exact/'
    excluded = []
    with local_network_only(set()), BlobServiceClient(
            account_url=f'http://127.0.0.1:1/_simulator/providers/azure/{ACCOUNT}',
            credential=None, api_version='2023-11-03') as service:
        for key in KEYS:
            client = service.get_blob_client('sdk-exact', key)
            prepared = Request('PUT', client.url).prepare()
            parsed = urlsplit(prepared.url)
            assert parsed.hostname == '127.0.0.1' and parsed.port == 1
            path = unquote(parsed.path)
            assert path.startswith(prefix), 'SDK changed the provider/container route'
            actual_key = path[len(prefix):]
            expected_key = AZURE_NORMALIZED_KEYS.get(key, key)
            assert actual_key == expected_key, 'Unexpected SDK key normalization'
            if key in AZURE_NORMALIZED_KEYS:
                assert actual_key != key
                excluded.append({
                    'provider': 'azure', 'key': key, 'prepared_key': actual_key,
                    'reason': 'Requests PreparedRequest normalizes literal dot path segments before HTTP transfer',
                })
    assert len(excluded) == 2 and len(AZURE_KEYS) + len(excluded) == len(KEYS)
    return excluded


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
        for key in AZURE_KEYS:
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
        assert set(x.name for x in bounded_items(container.list_blobs(results_per_page=2))) == set(AZURE_KEYS)
        for key in AZURE_KEYS:
            blob = container.get_blob_client(key); properties = blob.get_blob_properties()
            # Keep the real SDK Range request on the native bounded-range path.
            # Any compatibility failure must fail this test, without stripping
            # headers or substituting raw HTTP for the SDK operation.
            data = blob.download_blob(max_concurrency=1).readall()
            assert data == (label + ':' + key).encode()
            assert bytes(properties.content_settings.content_md5) == hashlib.md5(data).digest()
            blob.delete_blob(etag=properties.etag, match_condition=MatchConditions.IfNotModified)
            expect_status(404, blob.get_blob_properties)
        container.delete_container()
        assert bounded_items(service.list_containers()) == []


def run(binary, selected):
    versions = require_sdks(selected)  # before processes, sockets, or network
    excluded = []
    if 'azure' in selected:
        with acceptance_phase('azure:official-sdk-key-preparation'):
            excluded = azure_sdk_key_contract()
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
                for index, team in enumerate(teams):
                    with acceptance_phase(f'cli:start:team-{index}'):
                        team.start()
                for provider in selected:
                    factory = gcp_client if provider == 'gcp' else azure_client
                    for index, (own, other) in enumerate(((teams[0], teams[1]), (teams[1], teams[0]))):
                        with acceptance_phase(f'{provider}:cross-team-token:team-{index}'):
                            client = factory(other, own.token)
                            try:
                                if provider == 'gcp':
                                    expect_status(401, lambda: bounded_items(client.list_buckets(timeout=3, retry=None)))
                                else:
                                    expect_status(401, lambda: bounded_items(client.list_containers()))
                            finally:
                                client.close()
                    create = gcp_create if provider == 'gcp' else azure_create
                    for index, team in enumerate(teams):
                        with acceptance_phase(f'{provider}:create-and-conditions:team-{index}'):
                            create(team, f'team-{index}')
                tokens = [team.token for team in teams]
                for index, team in enumerate(teams):
                    with acceptance_phase(f'cli:stop:team-{index}'):
                        team.stop()
                for index, team in enumerate(teams):
                    with acceptance_phase(f'cli:restart:team-{index}'):
                        team.start()
                        assert team.token == tokens[index]
                for provider in selected:
                    verify = gcp_verify_delete if provider == 'gcp' else azure_verify_delete
                    for index, team in enumerate(teams):
                        with acceptance_phase(f'{provider}:persisted-read-and-cleanup:team-{index}'):
                            verify(team, f'team-{index}')
            finally:
                failures = []
                for team in teams:
                    try: team.stop()
                    except Exception as error: failures.append(type(error).__name__)
                if failures: raise AssertionError('Process cleanup failed: ' + ','.join(failures))
    return {'providers': selected, 'sdkVersions': versions, 'result': 'PASS',
            'exact_key_cases_per_team': {provider: len(KEYS if provider == 'gcp' else AZURE_KEYS)
                                         for provider in selected},
            'excluded_sdk_key_cases': excluded,
            'two_cli_processes': True, 'restart_persistence': True,
            'conditional_writes_and_deletes': True, 'checksums_and_exact_bytes': True,
            'missing_cross_team_and_nonowner_denial': True, 'cleanup': 'DELETED'}


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
    except AcceptanceFailure as error:
        print(json.dumps({'result': 'FAIL/NOT-ACCEPTED', 'phase': error.phase,
                          'errorType': error.error_type, 'source': error.source,
                          'http_status_failure': error.status_failure}))
        raise SystemExit(1) from None
    except Exception as error:
        # SDK exception strings may contain request headers or response data.
        # Keep failure visible while withholding potentially sensitive details.
        print(json.dumps({'result': 'FAIL/NOT-ACCEPTED', 'errorType': type(error).__name__}))
        raise SystemExit(1) from None
    signal.alarm(0)
    print(json.dumps(result))
