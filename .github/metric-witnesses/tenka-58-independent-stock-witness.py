"""Independent stock boto3 witness for the bounded #58 submit/read contract."""
import argparse
from collections import Counter
from datetime import datetime, timedelta, timezone
import json
import math
from pathlib import Path
import tempfile
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request

import boto3
import botocore
from botocore.config import Config
from botocore.exceptions import ClientError
from tenka_review_loopback import running, control, local_open


NAMESPACE = 'Independent/Statistics'
METRIC = 'MeasuredDuration'
UNITS = 'Milliseconds'
DIM = [{'Name': 'Area', 'Value': 'blue'}, {'Name': 'Tier', 'Value': 'qa'}]
STATS = ['SampleCount', 'Sum', 'Minimum', 'Maximum', 'Average']
AUDIT = Counter()


def client(endpoint, token, region='us-east-1'):
    result = boto3.client('cloudwatch', endpoint_url=endpoint, region_name=region,
                          aws_access_key_id='TCSIMMETRICS', aws_secret_access_key='simulator-only',
                          config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=3, read_timeout=10))
    def before_send(request, **kwargs):
        assert request.url == endpoint + '/' and request.method == 'POST'
        enc = request.headers.get('Content-Encoding', b'')
        if isinstance(enc, bytes):
            enc = enc.decode('ascii')
        assert enc in ('', 'gzip')
        AUDIT['gzip' if enc == 'gzip' else 'plain'] += 1
        AUDIT['send'] += 1
    result.meta.events.register('before-sign', lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', token))
    result.meta.events.register('before-call', lambda **kwargs: AUDIT.update(['call']))
    result.meta.events.register('before-send', before_send)
    result.meta.events.register('after-call', lambda **kwargs: AUDIT.update(['response']))
    return result


def point(value, timestamp, **overrides):
    return {'MetricName': METRIC, 'Timestamp': timestamp, 'Value': value,
            'Dimensions': DIM, 'Unit': UNITS, **overrides}


def get(sdk, start, **changes):
    args = {'Namespace': NAMESPACE, 'MetricName': METRIC, 'StartTime': start + timedelta(seconds=12),
            'EndTime': start + timedelta(minutes=2), 'Period': 60, 'Statistics': STATS, 'Dimensions': DIM, 'Unit': UNITS}
    args.update(changes)
    response = sdk.get_metric_statistics(**{k: v for k, v in args.items() if v is not None})
    assert response['Label'] == args['MetricName'] and response['ResponseMetadata']['HTTPStatusCode'] == 200
    points = {(v['Timestamp'], v['Unit']): v for v in response['Datapoints']}
    assert len(points) == len(response['Datapoints'])
    return points


def reject(expected_status, function):
    try:
        function()
    except ClientError as e:
        status = e.response['ResponseMetadata']['HTTPStatusCode']
        assert status == expected_status, (expected_status, status, e.response['Error']['Code'])
        return e.response['Error']['Code']
    raise AssertionError('accepted refused operation')


def assert_outer_peer_denied(endpoint, foreign_token, t):
    fields = {'Action': 'GetMetricStatistics', 'Version': '2010-08-01', 'Namespace': NAMESPACE,
              'MetricName': METRIC, 'StartTime': t.isoformat(), 'EndTime': (t + timedelta(minutes=2)).isoformat(),
              'Period': '60', 'Statistics.member.1': 'Sum'}
    request = Request(endpoint + '/', method='POST', data=urlencode(fields).encode(),
                      headers={'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'monitoring',
                               'X-Simulator-Token': foreign_token})
    try:
        with local_open(request) as response:
            raise AssertionError(('peer token accepted', response.status))
    except HTTPError as error:
        with error:
            assert error.status == 401 and error.headers.get_content_type() == 'application/json'
            assert json.load(error) == {'error': 'A valid simulator access token is required'}


def verify_stats(sdk, t):
    v = get(sdk, t)
    assert set(v) == {(t, UNITS), (t + timedelta(minutes=1), UNITS)}
    p = v[t, UNITS]
    assert (p['SampleCount'], p['Sum'], p['Minimum'], p['Maximum']) == (3, 16, 2, 10), p
    assert math.isclose(p['Average'], 16/3)
    p = v[t + timedelta(minutes=1), UNITS]
    assert (p['SampleCount'], p['Sum'], p['Minimum'], p['Maximum'], p['Average']) == (1, -2, -2, -2, -2)
    two = get(sdk, t, Period=120, Statistics=['Average', 'SampleCount'])
    assert set(two) == {(t, UNITS)}
    assert two[t, UNITS] == {'Timestamp': t, 'Unit': UNITS, 'Average': 3.5, 'SampleCount': 4}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--baseline', action='store_true')
    config = parser.parse_args()
    assert (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    now = datetime.now(timezone.utc).replace(second=0, microsecond=0) - timedelta(minutes=8)
    t = now - timedelta(minutes=now.minute % 2)
    with tempfile.TemporaryDirectory(prefix='tenka-58-independent-') as d:
        a_db, b_db = Path(d)/'A.sqlite', Path(d)/'B.sqlite'
        with running(db=a_db, authenticated=True) as (a_url, a_token):
            a = client(a_url, a_token)
            if config.baseline:
                failed = []
                for opname, f in [('PutMetricData',lambda: a.put_metric_data(Namespace=NAMESPACE, MetricData=[point(2, t)])), ('GetMetricStatistics',lambda: get(a, t))]:
                    try:
                        f()
                    except ClientError as e:
                        failed.append([opname,e.response['ResponseMetadata']['HTTPStatusCode'],e.response['Error']['Code']])
                    else:
                        raise AssertionError('baseline unexpectedly supported '+opname)
                print(json.dumps({'unsupportedBaseline':failed,'audit':AUDIT}));return
            with running(db=b_db, authenticated=True) as (b_url, b_token):
                b = client(b_url, b_token)
                a.put_metric_data(Namespace=NAMESPACE, MetricData=[point(2,t), point(4,t), point(10,t+timedelta(seconds=59),Dimensions=list(reversed(DIM))), point(-2,t+timedelta(seconds=60)), point(200,t+timedelta(minutes=2)), point(83,t,Unit='Count'), point(77,t,Dimensions=[DIM[0]])])
                b.put_metric_data(Namespace=NAMESPACE, MetricData=[point(91,t)])
                verify_stats(a,t)
                assert get(b,t)[t,UNITS]['Sum'] == 91
                assert get(client(a_url,a_token,'us-west-2'),t) == {}
                assert get(a,t,Dimensions=[]) == {} and get(a,t,Namespace='Independent/Absent') == {}
                assert get(a,t,Dimensions=[DIM[0]])[t,UNITS]['Sum'] == 77
                assert get(a,t,Unit='Bytes') == {} and get(a,t,MetricName='Unmeasured') == {}
                assert get(a,t,Unit=None)[t,'Count']['Sum'] == 83
                assert get(a,t,Unit=None)[t,UNITS]['Sum'] == 16
                assert_outer_peer_denied(b_url,a_token,t)
                policy = {'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'cloudwatch:GetMetricStatistics','Resource':'*'}]}
                path = '/_simulator/v1/iam/principals/metric-reader/policy'
                control(a_url,a_token,path,policy)
                participant = control(a_url,a_token,'/_simulator/v1/access',{'Principal':'metric-reader'})['AccessToken']
                reader = client(a_url,participant)
                assert get(reader,t)[t,UNITS]['Sum'] == 16
                reject(403,lambda: reader.put_metric_data(Namespace=NAMESPACE,MetricData=[point(111,t)]))
                policy['Statement'].append({'Effect':'Deny','Action':'cloudwatch:GetMetricStatistics','Resource':'*'})
                control(a_url,a_token,path,policy)
                reject(403,lambda: get(reader,t))
                reject(400,lambda: a.put_metric_data(Namespace=NAMESPACE,MetricData=[point(11,t),point(22,t,StorageResolution=1)]))
                verify_stats(a,t)
                big = [{'Name':'Batch','Value':'x'*900}]
                a.put_metric_data(Namespace=NAMESPACE,MetricData=[point(i,t,Dimensions=big) for i in range(15)])
                assert get(a,t,Dimensions=big)[t,UNITS]['Sum'] == 105
                assert AUDIT['gzip'] >= 1 and AUDIT['plain'] >= 1
        with running(db=a_db,authenticated=True) as (endpoint,token):
            verify_stats(client(endpoint,token),t)
    assert not a_db.exists() and not b_db.exists()
    assert AUDIT['call'] == AUDIT['send'] == AUDIT['response']
    print(json.dumps({'stockSDK':[boto3.__version__,botocore.__version__],'audited':AUDIT,'firstMinute':[3,16,2,10,16/3], 'fourSampleAverage':3.5,'processes':2,'databases':2,'authenticated':True,'peerToken':401,'peerTokenTransport':'raw protocol at simulator authentication boundary','implicitAndExplicitDenials':403,'restart':True,'cleanup':True},sort_keys=True))

if __name__ == '__main__':
    main()
