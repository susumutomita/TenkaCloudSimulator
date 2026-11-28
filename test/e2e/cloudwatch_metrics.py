"""Exercise custom CloudWatch samples through the normal binary and pinned Query SDK."""
from collections import Counter
from contextlib import ExitStack, contextmanager
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
from types import SimpleNamespace
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

from sakura_cli import NoRedirect

BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve()
NAMESPACE = 'Training/CustomMetrics'
STATS = ['SampleCount', 'Average', 'Sum', 'Minimum', 'Maximum']
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())


def control(team, path, method='GET', body=None):
    url = team.origin + path
    headers = {'X-Simulator-Token': team.token}
    if body is not None:
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(),
                                 headers=headers, method=method)
    with OPENER.open(req, timeout=5) as response:
        return json.load(response)


@contextmanager
def running(directory):
    directory.mkdir(exist_ok=True)
    token_path, database = directory / 'operator-token', directory / 'samples.db'
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    with subprocess.Popen([str(BINARY), '--listen', f'127.0.0.1:{port}', '--state', str(database),
                           '--token-file', str(token_path)], cwd=directory,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as process:
        try:
            deadline = time.monotonic() + 20
            team = SimpleNamespace(origin=origin, db=database, token=None)
            while time.monotonic() < deadline:
                assert process.poll() is None, 'Local metrics instance exited before readiness'
                if token_path.is_file():
                    team.token = token_path.read_text().strip()
                    if team.token:
                        try:
                            control(team, '/_simulator/health')
                            break
                        except OSError:
                            pass
                time.sleep(0.05)
            else:
                raise AssertionError('Local metrics instance did not become ready')
            yield team
        finally:
            process.terminate()
            try:
                assert process.wait(timeout=10) == 0
            except subprocess.TimeoutExpired:
                process.kill()
                raise


def client(stack, team, counts, encodings, *, token=None, region='us-east-1'):
    sdk = boto3.client('cloudwatch', endpoint_url=team.origin, region_name=region,
                       aws_access_key_id='TCSIMMETRICS', aws_secret_access_key='dummy-local-only',
                       config=Config(proxies={}, retries={'total_max_attempts': 1},
                                     connect_timeout=3, read_timeout=10))
    stack.callback(sdk.close)

    def before_call(**_):
        counts['calls'] += 1

    def select_token(request, **_):
        request.headers['X-Simulator-Token'] = team.token if token is None else token

    def before_send(request, **_):
        assert request.url == team.origin + '/' and request.method == 'POST'
        counts['sends'] += 1
        encoding = request.headers.get('Content-Encoding', b'identity')
        encodings.add(encoding.decode() if isinstance(encoding, bytes) else encoding)

    def after_call(**_):
        counts['responses'] += 1

    for event, observer in [('before-call', before_call), ('before-sign', select_token),
                            ('before-send', before_send), ('after-call', after_call)]:
        sdk.meta.events.register(event, observer)
    return sdk


def denied(status, operation, **kwargs):
    try:
        operation(**kwargs)
    except ClientError as error:
        assert error.response['ResponseMetadata']['HTTPStatusCode'] == status
    else:
        raise AssertionError('Expected refusal from the owned simulator')


def run():
    counts, encodings = Counter(), set()
    start = datetime.now(timezone.utc).replace(second=0, microsecond=0) - timedelta(minutes=3)
    dims = [{'Name': 'Site', 'Value': 'one'}, {'Name': 'Node', 'Value': 'test|unit'}]
    query = dict(Namespace=NAMESPACE, MetricName='Duration', Dimensions=dims,
                 StartTime=start + timedelta(seconds=23), EndTime=start + timedelta(minutes=2),
                 Period=60, Unit='Milliseconds', Statistics=STATS)

    def datum(value, seconds):
        return dict(MetricName='Duration', Dimensions=list(reversed(dims)), Unit='Milliseconds',
                    Value=value, Timestamp=start + timedelta(seconds=seconds))

    batch = dict(Namespace=NAMESPACE, MetricData=[datum(2, 0), datum(4, 31), datum(10, 7), datum(-2, 60),
                                                  datum(99, 120)])

    def get(cloudwatch):
        return sorted(cloudwatch.get_metric_statistics(**query)['Datapoints'],
                      key=lambda point: point['Timestamp'])

    with tempfile.TemporaryDirectory(prefix='cloudwatch-samples-') as directory, ExitStack() as stack:
        def team(name):
            return running(Path(directory) / name)

        peer = stack.enter_context(team('b'))
        b = client(stack, peer, counts, encodings)
        with team('a') as owner:
            assert not os.path.samefile(owner.db, peer.db)
            a = client(stack, owner, counts, encodings)
            caps = control(owner, '/_simulator/v1/capabilities')['services']['cloudwatch']
            assert set(caps['operations']) == {'PutMetricData', 'GetMetricStatistics'} and caps['alarms'] is False
            a.put_metric_data(**batch)
            assert get(b) == []
            b.put_metric_data(Namespace=NAMESPACE, MetricData=[datum(7, 0)])
            assert get(b)[0]['Sum'] == 7
            assert get(client(stack, owner, counts, encodings, region='us-west-2')) == []
            points = get(a)
            assert len(points) == 2
            assert [points[0][key] for key in STATS] == [3, 16 / 3, 16, 2, 10]
            assert points[0]['Timestamp'] == start and points[1]['Sum'] == -2
            merged = a.get_metric_statistics(**{**query, 'Period': 120})['Datapoints']
            assert len(merged) == 1 and merged[0]['Average'] == 3.5 and merged[0]['SampleCount'] == 4

            # Unchanged default SDK compression activates above its ordinary 10 KiB threshold.
            large = [dict(MetricName='Compression', Value=i,
                          Dimensions=[{'Name': 'Group', 'Value': 'a' * 150}]) for i in range(100)]
            a.put_metric_data(Namespace=NAMESPACE, MetricData=large)
            compressed = a.get_metric_statistics(**{**query, 'MetricName': 'Compression',
                'Dimensions': large[0]['Dimensions'], 'Unit': 'None',
                'StartTime': start, 'EndTime': datetime.now(timezone.utc) + timedelta(minutes=1)})['Datapoints']
            assert sum(p['SampleCount'] for p in compressed) == 100
            assert sum(p['Sum'] for p in compressed) == 4950
            assert {'identity', 'gzip'} <= encodings

            statements = [{'Effect': 'Allow', 'Action': 'cloudwatch:GetMetricStatistics', 'Resource': '*'},
                          {'Effect': 'Deny', 'Action': 'cloudwatch:PutMetricData', 'Resource': '*'}]
            control(owner, '/_simulator/v1/iam/principals/metric-reader/policy', 'PUT',
                    {'Version': '2012-10-17', 'Statement': statements})
            token = control(owner, '/_simulator/v1/access', 'POST', {'Principal': 'metric-reader'})['AccessToken']
            reader = client(stack, owner, counts, encodings, token=token)
            assert get(reader) == points
            denied(403, reader.put_metric_data, **batch)
            writer_policy = {'Version': '2012-10-17', 'Statement': [
                {'Effect': 'Allow', 'Action': 'cloudwatch:PutMetricData', 'Resource': '*'}]}
            control(owner, '/_simulator/v1/iam/principals/metric-writer/policy', 'PUT', writer_policy)
            writer_token = control(owner, '/_simulator/v1/access', 'POST', {'Principal': 'metric-writer'})['AccessToken']
            writer = client(stack, owner, counts, encodings, token=writer_token)
            denied(403, writer.get_metric_statistics, **query)
            writer.put_metric_data(Namespace=NAMESPACE, MetricData=[dict(MetricName='Writer', Value=5, Dimensions=[])])
            optional = dict(query, MetricName='Writer', Unit='None',
                            EndTime=datetime.now(timezone.utc) + timedelta(minutes=1))
            optional.pop('Dimensions')
            assert a.get_metric_statistics(**optional)['Datapoints'][0]['Sum'] == 5
            # Token authentication precedes AWS Query. Its 401 response uses
            # the simulator's JSON envelope and cannot be parsed as Query XML.
            wrong_team = urllib.request.Request(
                peer.origin + '/', method='POST',
                headers={'Content-Type': 'application/x-www-form-urlencoded',
                         'X-Simulator-Service': 'monitoring', 'X-Simulator-Token': owner.token},
                data=b'Action=PutMetricData&Version=2010-08-01&Namespace=WrongTeam&'
                     b'MetricData.member.1.MetricName=Rejected&MetricData.member.1.Value=1')
            try:
                OPENER.open(wrong_team, timeout=5).close()
            except urllib.error.HTTPError as error:
                with error:
                    assert error.code == 401 and 'error' in json.load(error)
            else:
                raise AssertionError('Team A token reached Team B metrics')
            assert not b.get_metric_statistics(**{**query, 'Namespace': 'WrongTeam',
                'MetricName': 'Rejected', 'Dimensions': [], 'Unit': 'None',
                'EndTime': datetime.now(timezone.utc) + timedelta(minutes=1)})['Datapoints']
            denied(400, a.put_metric_data, Namespace=NAMESPACE, MetricData=[datum(25, 0), datum(0, 0) | {'StorageResolution': 1}])
            assert get(a) == points
        with team('a') as restarted:
            assert get(client(stack, restarted, counts, encodings)) == points
            assert get(b)[0]['Sum'] == 7
    assert not Path(directory).exists()
    assert counts['calls'] == counts['sends'] == counts['responses'] > 0
    print(json.dumps({'operations': ['PutMetricData', 'GetMetricStatistics'], 'transport': 'AWS Query 2010-08-01',
                      'counts': dict(counts), 'contentEncodings': sorted(encodings),
                      'twoTeamState': True, 'peerTokenHTTP': 401, 'restartPersisted': True, 'cleanup': 'owned temporary databases removed'}))


if __name__ == '__main__':
    run()
