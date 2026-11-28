"""Isolate the five monitoring Query forms before checking older reference copies."""
import hashlib
import json


ACTIONS = ('DescribeAlarms', 'GetMetricStatistics', 'PutMetricAlarm', 'PutMetricData', 'DeleteAlarms')
FIELDS = (
    'AlarmNames.member.1', 'Namespace', 'MetricName', 'Unit', 'StartTime', 'EndTime', 'Period',
    'Statistics.member.1', 'AlarmName', 'Statistic', 'Threshold', 'ComparisonOperator', 'EvaluationPeriods',
    'MetricData.member.1.MetricName', 'MetricData.member.1.Unit', 'MetricData.member.1.Value',
    'MetricData.member.1.Timestamp', 'Dimensions.member.1.Name', 'Dimensions.member.1.Value',
    'MetricData.member.1.Dimensions.member.1.Name', 'MetricData.member.1.Dimensions.member.1.Value',
)


def project_monitoring(spec):
    """Validate and remove only these exact additions from the caller's private copy."""
    post = spec['paths']['/']['post']
    form = post['requestBody']['content']['application/x-www-form-urlencoded']
    props = form['schema']['properties']
    enums = next(p for p in post['parameters'] if p.get('name') == 'X-Simulator-Service')['schema']['enum']
    assert enums.count('monitoring') == 1
    enums.remove('monitoring')
    assert props['Version']['enum'].count('2010-08-01') == 1
    props['Version']['enum'].remove('2010-08-01')
    actions = props['Action']['enum']
    assert [name for name in actions if name in ACTIONS] == list(ACTIONS)
    assert [name for name in form['examples'] if name in ACTIONS] == list(ACTIONS)
    examples = {name: form['examples'].pop(name) for name in ACTIONS}
    canonical = json.dumps(examples, sort_keys=True, separators=(',', ':'), ensure_ascii=False)
    assert hashlib.sha256(canonical.encode()).hexdigest() == '1404a695fdf67566a350fa7cd17d2f11b4387cac3d75d6231a4609c81686a7ae'
    for name in ACTIONS:
        actions.remove(name)
    for name in FIELDS:
        assert props.pop(name) == {'type': 'string'}
