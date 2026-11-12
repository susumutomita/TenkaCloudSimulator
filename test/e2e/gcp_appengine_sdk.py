"""App Engine through official Google clients against two real binaries.

Sources are uploaded with google-cloud-storage to the native Cloud Storage
provider, the application is managed with google-api-python-client and its
pinned static appengine.v1 discovery document, and the deployed Python WSGI
versions answer real HTTP requests through the traffic split and firewall.
Another subject and another project are refused without changing state, a
second team stays empty, state survives restart and cleanup leaves only the
application, its default service and the one version that serves it, which
the official API cannot delete. No Google account, network or billing.
"""
import hashlib
import io
import json
import sys
import tempfile
import urllib.error
import urllib.request
import zipfile

from google.auth.credentials import AnonymousCredentials
from google.auth.transport.requests import AuthorizedSession
from google.cloud import storage
from googleapiclient.errors import HttpError

from aws_catalog import Team
from gcp_catalog_fixtures import client

PROJECT = 'tenkacloud-local'
APP = 'apps/' + PROJECT
HOST = PROJECT + '.an.r.appspot.com'
SOURCE = '''import os
def app(environ, start_response):
    start_response("200 OK", [("Content-Type", "text/plain; charset=utf-8")])
    return [("%s/%s %s %s" % (os.environ["GAE_SERVICE"], os.environ["GAE_VERSION"], os.environ.get("COLOR", "-"), environ["PATH_INFO"])).encode()]
'''


def zipped(files):
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, 'w') as archive:
        for name, content in files.items():
            archive.writestr(name, content)
    return buffer.getvalue()


def gcs(team, token=None):
    credentials = AnonymousCredentials()
    session = AuthorizedSession(credentials, refresh_status_codes=(), max_refresh_attempts=0)
    return storage.Client(project=PROJECT, credentials=credentials, _http=session,
                          client_options={'api_endpoint': team.endpoint + '/_simulator/providers/gcp'},
                          extra_headers={'X-Simulator-Token': token or team.token})


def appengine(team, token=None):
    return client(team, {'api': 'appengine.v1'}, token).apps()


def status_of(call):
    try:
        call.execute(num_retries=0)
    except HttpError as error:
        return error.resp.status, json.loads(error.content)['error']['status']
    raise AssertionError('call unexpectedly succeeded')


def serve(team, host, path='/', token=None, cookie=None):
    headers = {'X-Simulator-Token': token or team.token}
    if cookie:
        headers['Cookie'] = cookie
    request = urllib.request.Request(team.endpoint + '/_simulator/providers/gcp/appspot/' + host + path, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.status, response.read().decode(), dict(response.headers)
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode(), dict(error.headers)


def state(team):
    apps = appengine(team)
    services = apps.services().list(appsId=PROJECT).execute()['services']
    versions = {s['id']: sorted(v['id'] for v in apps.services().versions().list(appsId=PROJECT, servicesId=s['id']).execute().get('versions', []))
                for s in services}
    rules = apps.firewall().ingressRules().list(appsId=PROJECT).execute()['ingressRules']
    return {'app': apps.get(appsId=PROJECT).execute(), 'services': services, 'versions': versions, 'rules': rules}


def run():
    with tempfile.TemporaryDirectory(prefix='tcsim-gae-a-') as a_dir, tempfile.TemporaryDirectory(prefix='tcsim-gae-b-') as b_dir:
        a, b = Team(a_dir), Team(b_dir)
        a.start()
        b.start()
        try:
            capability = a.control('GET', '/_simulator/v1/capabilities')['gcpAppEngine']
            assert capability['runtimes'] == ['python312'] and capability['officialMethods'] == 59 and len(capability['methods']) == 22, capability
            bucket = gcs(a).create_bucket('appengine-sources')
            bucket.blob('v1.zip').upload_from_string(zipped({'main.py': SOURCE}), content_type='application/zip')
            bucket.blob('v2/main.py').upload_from_string(SOURCE, content_type='text/x-python')
            sha1 = hashlib.sha1(SOURCE.encode()).hexdigest()
            apps = appengine(a)
            created = apps.create(body={'id': PROJECT, 'locationId': 'asia-northeast1'}).execute()
            assert created['done'] and created['response']['defaultHostname'] == HOST, created
            assert status_of(apps.create(body={'id': PROJECT, 'locationId': 'asia-northeast1'})) == (409, 'ALREADY_EXISTS')
            assert [l['locationId'] for l in apps.locations().list(appsId=PROJECT).execute()['locations']] == ['asia-northeast1', 'europe-west', 'us-central', 'us-east1']
            versions = apps.services().versions()
            v1 = versions.create(appsId=PROJECT, servicesId='default', body={
                'id': 'v1', 'runtime': 'python312', 'envVariables': {'COLOR': 'blue'},
                'deployment': {'zip': {'sourceUrl': 'https://storage.googleapis.com/appengine-sources/v1.zip'}}}).execute()
            assert v1['response']['versionUrl'] == 'https://v1-dot-default-dot-' + HOST, v1
            versions.create(appsId=PROJECT, servicesId='default', body={
                'id': 'v2', 'runtime': 'python312', 'envVariables': {'COLOR': 'green'},
                'deployment': {'files': {'main.py': {'sourceUrl': 'https://storage.googleapis.com/appengine-sources/v2/main.py', 'sha1Sum': sha1}}}}).execute()
            versions.create(appsId=PROJECT, servicesId='api', body={
                'id': 'v1', 'runtime': 'python312',
                'deployment': {'zip': {'sourceUrl': 'https://storage.googleapis.com/appengine-sources/v1.zip', 'filesCount': 1}}}).execute()
            # Malformed, unsupported, conflicting and missing requests.
            assert status_of(versions.create(appsId=PROJECT, servicesId='default', body={'id': 'v3', 'runtime': 'nodejs22',
                             'deployment': {'zip': {'sourceUrl': 'https://storage.googleapis.com/appengine-sources/v1.zip'}}})) == (400, 'INVALID_ARGUMENT')
            assert status_of(versions.create(appsId=PROJECT, servicesId='default', body={'id': 'v2', 'runtime': 'python312',
                             'deployment': {'zip': {'sourceUrl': 'https://storage.googleapis.com/appengine-sources/v1.zip'}}})) == (409, 'ALREADY_EXISTS')
            assert status_of(versions.create(appsId=PROJECT, servicesId='default', body={'id': 'v3', 'runtime': 'python312',
                             'deployment': {'zip': {'sourceUrl': 'https://storage.googleapis.com/appengine-sources/missing.zip'}}})) == (400, 'INVALID_ARGUMENT')
            assert status_of(versions.get(appsId=PROJECT, servicesId='default', versionsId='v9')) == (404, 'NOT_FOUND')
            assert status_of(apps.repair(appsId=PROJECT, body={})) == (501, 'UNIMPLEMENTED')
            assert status_of(apps.services().delete(appsId=PROJECT, servicesId='default')) == (400, 'FAILED_PRECONDITION')
            assert status_of(versions.delete(appsId=PROJECT, servicesId='default', versionsId='v1')) == (400, 'FAILED_PRECONDITION')
            full = versions.get(appsId=PROJECT, servicesId='default', versionsId='v2', view='FULL').execute()
            assert full['envVariables'] == {'COLOR': 'green'} and full['deployment']['files']['main.py']['sha1Sum'] == sha1, full
            # Real requests: the default split, explicit versions and services.
            assert serve(a, HOST, '/hello')[:2] == (200, 'default/v1 blue /hello')
            assert serve(a, 'v2-dot-default-dot-' + PROJECT + '.appspot.com')[:2] == (200, 'default/v2 green /')
            assert serve(a, 'api-dot-' + HOST)[:2] == (200, 'api/v1 - /')
            assert serve(a, 'v9-dot-' + HOST)[0] == 404
            apps.services().patch(appsId=PROJECT, servicesId='default', updateMask='split',
                                  body={'split': {'shardBy': 'COOKIE', 'allocations': {'v1': 0.5, 'v2': 0.5}}}).execute()
            assert serve(a, HOST, cookie='GOOGAPPUID=10')[1].startswith('default/v1')
            assert serve(a, HOST, cookie='GOOGAPPUID=990')[1].startswith('default/v2')
            assert 'GOOGAPPUID=' in serve(a, HOST)[2].get('Set-Cookie', '')
            # The firewall decides before code runs.
            rules = apps.firewall().ingressRules()
            rules.create(appsId=PROJECT, body={'priority': 10, 'action': 'DENY', 'sourceRange': '127.0.0.1', 'description': 'block loopback'}).execute()
            assert [r['priority'] for r in rules.list(appsId=PROJECT, matchingAddress='127.0.0.1').execute()['ingressRules']] == [10, 2147483647]
            assert serve(a, HOST)[0] == 403
            rules.patch(appsId=PROJECT, ingressRulesId='10', updateMask='action', body={'action': 'ALLOW'}).execute()
            assert serve(a, HOST)[0] == 200
            rules.batchUpdate(appsId=PROJECT, body={'ingressRules': [{'priority': 20, 'action': 'DENY', 'sourceRange': '10.0.0.0/8'}]}).execute()
            assert [r['priority'] for r in rules.list(appsId=PROJECT).execute()['ingressRules']] == [20, 2147483647]
            # Stopped versions and disabled applications stop serving.
            versions.patch(appsId=PROJECT, servicesId='api', versionsId='v1', updateMask='servingStatus', body={'servingStatus': 'STOPPED'}).execute()
            assert serve(a, 'api-dot-' + HOST)[0] == 503
            versions.patch(appsId=PROJECT, servicesId='api', versionsId='v1', updateMask='servingStatus', body={'servingStatus': 'SERVING'}).execute()
            apps.patch(appsId=PROJECT, updateMask='servingStatus', body={'servingStatus': 'USER_DISABLED'}).execute()
            assert serve(a, HOST)[0] == 503
            apps.patch(appsId=PROJECT, updateMask='servingStatus', body={'servingStatus': 'SERVING'}).execute()
            # Another subject and another project are refused without changes.
            before = state(a)
            a.control('PUT', '/_simulator/v1/iam/principals/gcp-outsider/policy',
                      {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': '*', 'Resource': '*'}]})
            outsider = a.control('POST', '/_simulator/v1/access', {'Principal': 'gcp-outsider'})['AccessToken']
            denied = appengine(a, outsider)
            assert status_of(denied.get(appsId=PROJECT)) == (403, 'PERMISSION_DENIED')
            assert status_of(denied.services().versions().delete(appsId=PROJECT, servicesId='default', versionsId='v2')) == (403, 'PERMISSION_DENIED')
            assert status_of(denied.firewall().ingressRules().create(appsId=PROJECT, body={'priority': 1, 'action': 'DENY', 'sourceRange': '*'})) == (403, 'PERMISSION_DENIED')
            assert status_of(apps.get(appsId='other-project')) == (403, 'PERMISSION_DENIED')
            assert status_of(apps.services().get(appsId='other-project', servicesId='default')) == (403, 'PERMISSION_DENIED')
            assert state(a) == before, 'a refused request changed state'
            # Requests to the application need only an authenticated session.
            assert serve(a, 'v1-dot-' + HOST, token=outsider)[:2] == (200, 'default/v1 blue /')
            # Another team's binary holds nothing of this one.
            assert status_of(appengine(b).get(appsId=PROJECT)) == (404, 'NOT_FOUND')
            assert serve(b, HOST)[0] == 404
            assert serve(b, HOST, token=a.token)[0] == 401
            # State and the deployed copies survive a restart.
            bucket.blob('v1.zip').delete()
            a.stop()
            a.start()
            assert state(a) == before
            assert serve(a, 'v1-dot-' + HOST)[:2] == (200, 'default/v1 blue /')
            apps = appengine(a)
            versions = apps.services().versions()
            # Cleanup in dependency order. The application, its default service
            # and the version receiving its traffic remain, as in App Engine.
            apps.services().patch(appsId=PROJECT, servicesId='default', updateMask='split', body={'split': {'allocations': {'v1': 1}}}).execute()
            deleted = versions.delete(appsId=PROJECT, servicesId='default', versionsId='v2').execute()
            assert deleted['done'] and apps.operations().get(appsId=PROJECT, operationsId=deleted['name'].split('/')[-1]).execute()['done']
            apps.services().delete(appsId=PROJECT, servicesId='api').execute()
            apps.firewall().ingressRules().delete(appsId=PROJECT, ingressRulesId='20').execute()
            remaining = state(a)
            assert remaining['versions'] == {'default': ['v1']} and [r['priority'] for r in remaining['rules']] == [2147483647], remaining
            for blob in gcs(a).list_blobs('appengine-sources'):
                blob.delete()
            gcs(a).get_bucket('appengine-sources').delete()
            assert list(gcs(a).list_buckets()) == []
            assert serve(a, HOST)[:2] == (200, 'default/v1 blue /')
            operations = apps.operations().list(appsId=PROJECT).execute()['operations']
            assert operations[0]['metadata']['method'] == 'google.appengine.v1.Applications.CreateApplication', operations[0]
        finally:
            for team in (a, b):
                if team.process:
                    team.stop()
    print('App Engine official clients: deploy, serve, split, firewall, refusal, isolation, restart and cleanup verified.')


if __name__ == '__main__':
    sys.exit(run())
