"""Local PCS metadata associations through stock boto3 and the real Console.

No scheduler, provisioned nodes, jobs or cloud calls are exercised. Inherited
SDK identity/profile bindings are retained; a non-fixture profile is not reset.
"""
import contextlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit
import urllib.request

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import expect, sync_playwright

BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve(strict=True)


@contextlib.contextmanager
def instance(directory):
    directory = Path(directory)
    directory.mkdir(exist_ok=True)
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    endpoint = f'http://127.0.0.1:{port}'
    token_file = directory / 'fixture-token'
    process = subprocess.Popen([str(BINARY), '--listen', f'127.0.0.1:{port}',
                                '--state', str(directory / 'state.db'), '--token-file', str(token_file)],
                               cwd=directory, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(300):
            assert process.poll() is None, 'fixture CLI exited before readiness'
            if token_file.exists():
                token = token_file.read_text().strip()
                try:
                    with urllib.request.urlopen(urllib.request.Request(endpoint + '/_simulator/health', headers={'X-Simulator-Token': token}), timeout=1):
                        break
                except OSError:
                    pass
            time.sleep(0.05)
        else:
            raise AssertionError('fixture CLI readiness deadline')
        yield endpoint, token
    finally:
        original = sys.exception()
        try:
            if process.poll() is None:
                process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                raise AssertionError('fixture CLI required forced cleanup')
            assert process.returncode == 0, 'fixture CLI shutdown failed'
        except BaseException as cleanup_error:
            if original is not None:
                original.add_note('Owned fixture CLI cleanup failed: ' + type(cleanup_error).__name__)
            else:
                raise


def client(endpoint, token, region='us-east-1'):
    c = boto3.client('pcs', endpoint_url=endpoint, region_name=region,
                     aws_access_key_id='TCSIMPCSLOCAL', aws_secret_access_key='fixture-local-only',
                     config=Config(retries={'max_attempts': 0}, connect_timeout=2, read_timeout=10))
    c.meta.events.register('before-sign.pcs', lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', token))

    def local_only(request, **kwargs):
        assert (urlsplit(request.url).scheme, urlsplit(request.url).netloc) == (urlsplit(endpoint).scheme, urlsplit(endpoint).netloc)
        assert request.method == 'POST' and urlsplit(request.url).path == '/'
    c.meta.events.register('before-send.pcs', local_only)
    return c


def refused(code, call):
    try:
        call()
    except ClientError as error:
        assert error.response['Error']['Code'] == code
    else:
        raise AssertionError('required native refusal missing')


def seed(c, name):
    cluster = c.create_cluster(clusterName=name, scheduler={'type': 'SLURM', 'version': '24.11'},
                               size='SMALL', networking={'subnetIds': ['subnet-0a1b2c3d']})['cluster']
    group = c.create_compute_node_group(clusterIdentifier=cluster['id'], computeNodeGroupName='compute',
                                       subnetIds=['subnet-0a1b2c3d'], instanceConfigs=[{'instanceType': 'c6i.large'}],
                                       scalingConfiguration={'minInstanceCount': 0, 'maxInstanceCount': 2},
                                       customLaunchTemplate={'id': 'lt-0123456789abcdef0', 'version': '1'},
                                       iamInstanceProfileArn='arn:aws:iam::000000000000:instance-profile/pcs')['computeNodeGroup']
    assert cluster['status'] == group['status'] == 'CREATING'
    assert not cluster.get('endpoints') and not cluster.get('authKey')
    return cluster['id'], group['id']


def console_detach(endpoint, token, cluster, queue_id, c):
    with sync_playwright() as p:
        executable = os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
        with p.chromium.launch(executable_path=executable, chromium_sandbox=True) as browser:
            context = browser.new_context(service_workers='block')
            outside, errors, writes = [], [], []

            def guard(route):
                if (urlsplit(route.request.url).scheme, urlsplit(route.request.url).netloc) == (urlsplit(endpoint).scheme, urlsplit(endpoint).netloc):
                    route.continue_()
                else:
                    outside.append(True)
                    route.abort()
            context.route('**/*', guard)
            page = context.new_page()
            page.on('pageerror', lambda _: errors.append(True))
            page.on('request', lambda request: writes.append(request) if request.headers.get('x-amz-target', '').endswith('.UpdateQueue') else None)
            page.goto(endpoint + '/_simulator/console/')
            page.get_by_label('Access token', exact=True).fill(token)
            page.get_by_role('button', name='Open sandbox', exact=True).click()
            page.get_by_role('button', name='Services', exact=True).click()
            page.get_by_role('menuitem', name='Service catalog', exact=True).click()
            page.get_by_role('button', name=re.compile('^Catalog service')).click()
            page.get_by_role('option', name=re.compile('^AWS Parallel Computing Service')).click()
            page.get_by_role('tab', name='Queues', exact=True).click()
            page.get_by_role('button', name=re.compile('^Cluster')).click()
            page.get_by_role('option', name='owner-cluster', exact=True).click()
            page.get_by_role('link', name='owner-queue', exact=True).click()
            page.get_by_role('button', name='Edit', exact=True).click()
            form = page.locator('form').filter(has=page.get_by_role('button', name='Save changes', exact=True))
            form.locator('[id="catalog-field-computeNodeGroupConfigurations"]').fill('[]')
            form.get_by_role('button', name='Save changes', exact=True).click()
            expect(page.get_by_text('Resource updated.', exact=True)).to_be_visible()
            assert len(writes) == 1
            request = writes[0]
            assert request.url == endpoint + '/' and request.method == 'POST'
            assert request.post_data_json == {'clusterIdentifier': cluster, 'queueIdentifier': queue_id, 'computeNodeGroupConfigurations': []}
            headers = request.all_headers()
            assert headers['x-amz-target'] == c.meta.service_model.metadata['targetPrefix'] + '.UpdateQueue'
            assert headers['content-type'].split(';', 1)[0] == 'application/x-amz-json-1.0'
            assert request.redirected_from is None and request.redirected_to is None
            assert headers.get('cookie') and not {'authorization', 'x-simulator-token', 'x-simulator-principal'}.intersection(headers)
            assert c.get_queue(clusterIdentifier=cluster, queueIdentifier=queue_id)['queue']['computeNodeGroupConfigurations'] == []
            assert not outside and not errors
            context.close()


def main():
    # Fail closed on an inherited SDK identity/configuration. Never replace it
    # with fixture defaults or consult real credential files.
    assert not any(key.startswith(('AWS_', 'BOTO_')) for key in os.environ)
    assert not any(path.exists() or path.is_symlink() for path in
                   [Path.home() / '.aws/config', Path.home() / '.aws/credentials',
                    Path.home() / '.boto', Path('/etc/boto.cfg')])
    with tempfile.TemporaryDirectory(prefix='pcs-associations-') as root:
        owner, peer = Path(root) / 'owner', Path(root) / 'peer'
        with instance(peer) as (peer_endpoint, peer_token):
            peer_client = client(peer_endpoint, peer_token)
            peer_cluster, peer_group = seed(peer_client, 'owner-cluster')
            peer_queue = peer_client.create_queue(clusterIdentifier=peer_cluster, queueName='owner-queue', computeNodeGroupConfigurations=[{'computeNodeGroupId': peer_group}])['queue']
            peer_snapshot = peer_client.get_queue(clusterIdentifier=peer_cluster, queueIdentifier=peer_queue['id'])['queue']
            with instance(owner) as (endpoint, token):
                c = client(endpoint, token)
                cluster, group = seed(c, 'owner-cluster')
                other, foreign = seed(c, 'other-cluster')
                group_ref = [{'computeNodeGroupId': group}]
                refused('ResourceNotFoundException', lambda: c.create_queue(clusterIdentifier=cluster, queueName='foreign-queue', computeNodeGroupConfigurations=[{'computeNodeGroupId': foreign}]))
                queue = c.create_queue(clusterIdentifier=cluster, queueName='owner-queue', computeNodeGroupConfigurations=group_ref)['queue']
                assert queue['clusterId'] == cluster and queue['status'] == 'CREATING'
                snapshot = c.get_queue(clusterIdentifier=cluster, queueIdentifier=queue['id'])['queue']
                refused('ResourceNotFoundException', lambda: c.update_queue(clusterIdentifier=cluster, queueIdentifier=queue['id'], computeNodeGroupConfigurations=group_ref + [{'computeNodeGroupId': foreign}]))
                assert c.get_queue(clusterIdentifier=cluster, queueIdentifier=queue['id'])['queue'] == snapshot
                refused('ConflictException', lambda: c.delete_compute_node_group(clusterIdentifier=cluster, computeNodeGroupIdentifier=group))
                refused('ResourceNotFoundException', lambda: peer_client.get_cluster(clusterIdentifier=cluster))
                assert peer_client.get_queue(clusterIdentifier=peer_cluster, queueIdentifier=peer_queue['id'])['queue'] == peer_snapshot
                refused('ResourceNotFoundException', lambda: client(endpoint, token, 'ap-northeast-1').get_cluster(clusterIdentifier=cluster))
                c.delete_compute_node_group(clusterIdentifier=other, computeNodeGroupIdentifier=foreign)
                c.delete_cluster(clusterIdentifier=other)
            with instance(owner) as (endpoint, token):
                c = client(endpoint, token)
                assert c.get_queue(clusterIdentifier=cluster, queueIdentifier=queue['id'])['queue']['computeNodeGroupConfigurations'] == group_ref
                refused('ConflictException', lambda: c.delete_compute_node_group(clusterIdentifier=cluster, computeNodeGroupIdentifier=group))
                console_detach(endpoint, token, cluster, queue['id'], c)
                c.delete_compute_node_group(clusterIdentifier=cluster, computeNodeGroupIdentifier=group)
                c.delete_queue(clusterIdentifier=cluster, queueIdentifier=queue['id'])
                c.delete_cluster(clusterIdentifier=cluster)
                assert c.list_clusters()['clusters'] == []
            assert peer_client.get_queue(clusterIdentifier=peer_cluster, queueIdentifier=peer_queue['id'])['queue'] == peer_snapshot
            peer_client.delete_queue(clusterIdentifier=peer_cluster, queueIdentifier=peer_queue['id'])
            peer_client.delete_compute_node_group(clusterIdentifier=peer_cluster, computeNodeGroupIdentifier=peer_group)
            peer_client.delete_cluster(clusterIdentifier=peer_cluster)
            assert peer_client.list_clusters()['clusters'] == []
    print('PCS association SDK/Console: persistence, native refusals, peer/region isolation, detach and cleanup passed; no scheduler or compute provisioning')


if __name__ == '__main__':
    main()
