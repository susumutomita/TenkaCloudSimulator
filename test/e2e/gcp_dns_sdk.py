#!/usr/bin/env python3
"""Opt-in DNS v1 discovery-client acceptance against a local simulator binary.

No browser, Terraform, Google credentials, external DNS, or live Google API.
Run with the repository's pinned E2E dependencies after building the binary:
    python test/e2e/gcp_dns_sdk.py
"""
from contextlib import contextmanager
import http.client
import importlib.metadata
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time
from urllib.parse import urlsplit


SDK_VERSION = '2.199.0'
PROVIDER = '/_simulator/providers/gcp'
DNS = PROVIDER + '/dns/v1/'
ZONE = 'sdk-zone'
RECORD = {
    'name': 'www.example.test.',
    'type': 'A',
    'ttl': 300,
    'rrdatas': ['192.0.2.18'],
}


def require(check, message):
    if not check:
        raise AssertionError(message)


def local_control(port, token, method, path, body=None):
    require(type(port) is int and 0 < port <= 65535, 'invalid local control port')
    require(re.fullmatch(r'/_simulator/(?:health|v1/(?:capabilities|access|iam/principals/dns-sdk-outsider/policy))',
                         path), 'unexpected local control route')
    data = None if body is None else json.dumps(body).encode()
    # HTTPConnection opens a direct socket; it has no proxy or redirect policy
    # and does not consult ambient proxy variables.
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=1)
    try:
        connection.request(method, path, body=data,
                           headers={'X-Simulator-Token': token, 'Content-Type': 'application/json'})
        response = connection.getresponse()
        require(not 300 <= response.status < 400, 'local control route tried to redirect')
        require(200 <= response.status < 300, f'local control returned HTTP {response.status}')
        body = response.read((1 << 20) + 1)
        require(len(body) <= 1 << 20, 'oversize local control response')
        return json.loads(body) if body else {}
    finally:
        connection.close()


class LocalTeam:
    def __init__(self, directory, binary):
        self.directory = Path(directory)
        self.binary = Path(binary)
        self.database = self.directory / 'state.sqlite'
        self.token_file = self.directory / 'operator-token'
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        self.endpoint = f'http://127.0.0.1:{self.port}'
        self.process = None
        self.token = None

    def start(self):
        self.process = subprocess.Popen(
            [str(self.binary), '--listen', f'127.0.0.1:{self.port}', '--state', str(self.database),
             '--token-file', str(self.token_file)], cwd=self.directory,
            env={k: v for k, v in os.environ.items() if k in ('HOME', 'TMPDIR', 'SYSTEMROOT')}
                | {'PATH': '/nonexistent'},
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            require(self.process.poll() is None, 'simulator exited before readiness')
            if self.token_file.is_file():
                self.token = self.token_file.read_text().strip()
                if self.token:
                    try:
                        self.control('GET', '/_simulator/health')
                    except OSError:
                        pass  # The listener may not yet accept a direct connection.
                    else:
                        return
            time.sleep(0.05)
        raise AssertionError('simulator did not become ready')

    def control(self, method, path, body=None):
        return local_control(self.port, self.token, method, path, body)

    def stop(self):
        process, self.process = self.process, None
        if process is None or process.poll() is not None:
            return
        process.terminate()
        try:
            process.wait(timeout=8)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=2)


@contextmanager
def started(team):
    try:
        team.start()
        yield team
    finally:
        team.stop()


def project_and_endpoint(team, capabilities):
    gcp = capabilities['cloudStorage']['gcp']
    project = gcp['project']
    require(isinstance(project, str) and re.fullmatch(r'[a-z][a-z0-9-]{0,62}', project),
            'invalid configured GCP project')
    require(gcp['endpoint'] == PROVIDER, 'unexpected GCP provider route')
    origin = f'http://127.0.0.1:{team.port}'
    require(type(team.port) is int and 0 < team.port <= 65535 and team.endpoint == origin,
            'DNS SDK target must be the owned numeric IPv4 loopback')
    return project, origin + DNS


def checked_uri(uri, port, collection):
    require(isinstance(uri, str) and not any(ord(c) <= 32 or ord(c) == 127 for c in uri),
            'DNS SDK URI is not canonical')
    target = urlsplit(uri)
    require(target.scheme == 'http' and target.netloc == f'127.0.0.1:{port}' and not target.fragment,
            'DNS SDK request left the owned loopback origin')
    require(target.path == collection or target.path.startswith(collection + '/'),
            'DNS SDK request left its configured project or has the wrong service path')
    require(all(segment and segment not in ('.', '..') and '\\' not in segment and '%' not in segment
                for segment in target.path.split('/')[1:]),
            'DNS SDK request contains a noncanonical path segment')
    return target.path


class LocalTokenHttp:
    def __init__(self, http, port, collection, token):
        self.http = http
        self.port = port
        self.collection = collection
        self.token = token
        self.http.follow_redirects = False

    def request(self, uri, method='GET', body=None, headers=None, **kwargs):
        checked_uri(uri, self.port, self.collection)
        # No redirect is followed by the underlying client. Never send ambient
        # auth from a discovery request or let it override this local token.
        local = {k: v for k, v in (headers or {}).items()
                 if k.lower() not in ('authorization', 'x-simulator-token')}
        local['X-Simulator-Token'] = self.token
        return self.http.request(uri, method, body=body, headers=local, **kwargs)


def execute(req, port, collection, expected_path):
    require(checked_uri(req.uri, port, collection) == expected_path,
            'DNS SDK built an unexpected operation path')
    return req.execute(num_retries=0)


def run():
    require(importlib.metadata.version('google-api-python-client') == SDK_VERSION,
            'use the repository-pinned google-api-python-client')
    import httplib2
    from googleapiclient.discovery import build
    from googleapiclient.errors import HttpError
    from aws_catalog import BINARY

    with tempfile.TemporaryDirectory(prefix='tcsim-gcp-dns-sdk-') as work:
        with started(LocalTeam(work, BINARY)) as team:
            caps = team.control('GET', '/_simulator/v1/capabilities')
            project, endpoint = project_and_endpoint(team, caps)
            collection = DNS + 'projects/' + project + '/managedZones'
            zone_path = collection + '/' + ZONE
            record_path = zone_path + '/rrsets/' + RECORD['name'] + '/' + RECORD['type']
            changes_path = zone_path + '/changes'

            def service(token):
                # An explicit transport and static discovery avoid ADC and remote
                # discovery. Disable environment httplib2 proxies for the owned host.
                http = httplib2.Http(timeout=5, proxy_info=None)
                return build('dns', 'v1', static_discovery=True,
                             http=LocalTokenHttp(http, team.port, collection, token),
                             client_options={'api_endpoint': endpoint})

            def call(req, path):
                return execute(req, team.port, collection, path)

            owner = service(team.token)
            zones = owner.managedZones()
            changes = owner.changes()
            require(call(zones.list(project=project), collection).get('managedZones', []) == [],
                    'test DNS project is not empty at start')
            created = call(zones.create(project=project, body={
                'name': ZONE, 'dnsName': 'example.test.', 'description': 'local SDK acceptance',
            }), collection)
            require(created.get('name') == ZONE and created.get('id') is not None,
                    'managed zone identity missing')
            committed = call(changes.create(project=project, managedZone=ZONE,
                                            body={'additions': [RECORD]}), changes_path)
            change_id = str(committed['id'])
            require(change_id.isascii() and change_id.isdecimal(), 'invalid local change identity')
            receipt = call(changes.get(project=project, managedZone=ZONE, changeId=change_id),
                           changes_path + '/' + change_id)
            require(receipt == committed, 'persisted change receipt differs')

            def read_record(svc):
                return call(svc.resourceRecordSets().get(project=project, managedZone=ZONE,
                                                         name=RECORD['name'], type=RECORD['type']),
                            record_path)

            observed = read_record(owner)
            require(all(observed.get(k) == v for k, v in RECORD.items()),
                    'official DNS client read a different record')
            team.control('PUT', '/_simulator/v1/iam/principals/dns-sdk-outsider/policy', {
                'Version': '2012-10-17',
                'Statement': [{'Effect': 'Allow', 'Action': '*', 'Resource': '*'}],
            })
            outsider_token = team.control('POST', '/_simulator/v1/access', {
                'Principal': 'dns-sdk-outsider',
            })['AccessToken']
            try:
                read_record(service(outsider_token))
            except HttpError as error:
                require(error.resp.status == 403, 'wrong local identity was not refused as expected')
            else:
                raise AssertionError('wrong local identity read DNS data')
            require(read_record(owner) == observed, 'refusal changed DNS state')
            require(call(changes.get(project=project, managedZone=ZONE, changeId=change_id),
                         changes_path + '/' + change_id) == receipt, 'refusal changed receipt')

            call(changes.create(project=project, managedZone=ZONE,
                                body={'deletions': [RECORD]}), changes_path)
            call(zones.delete(project=project, managedZone=ZONE), zone_path)
            require(call(zones.list(project=project), collection).get('managedZones', []) == [],
                    'managed zone remained after SDK cleanup')
    print('Local Cloud DNS v1 official discovery-client acceptance passed')


if __name__ == '__main__':
    run()
