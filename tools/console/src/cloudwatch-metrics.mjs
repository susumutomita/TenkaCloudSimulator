const namespace = 'http://monitoring.amazonaws.com/doc/2010-08-01/';
const states = ['OK', 'ALARM', 'INSUFFICIENT_DATA'];
const statistics = ['SampleCount', 'Average', 'Sum', 'Minimum', 'Maximum'];
const invalid = () => new Error('Invalid CloudWatch response.');
export const alarmPageSize = 20;

function children(node, name) {
  return [...node.children].filter(child => child.namespaceURI === namespace && child.localName === name);
}

function value(node, name, optional = false) {
  const matches = children(node, name);
  if (!matches.length && optional) return '';
  if (matches.length !== 1 || matches[0].children.length || !matches[0].textContent) throw invalid();
  return matches[0].textContent;
}

function container(node, name) {
  const matches = children(node, name);
  if (matches.length !== 1) throw invalid();
  return matches[0];
}

function readAlarm(node, region) {
  const name = value(node, 'AlarmName');
  const arn = value(node, 'AlarmArn');
  const state = value(node, 'StateValue');
  const statistic = value(node, 'Statistic');
  if (arn !== 'arn:aws:cloudwatch:' + region + ':000000000000:alarm:' + name
      || !states.includes(state) || !statistics.includes(statistic) || value(node, 'Period') !== '60') throw invalid();
  const dimensions = children(container(node, 'Dimensions'), 'member').map(member => ({
    name: value(member, 'Name'), value: value(member, 'Value'),
  }));
  if (new Set(dimensions.map(d => d.name)).size !== dimensions.length) throw invalid();
  const reasonData = JSON.parse(value(node, 'StateReasonData'));
  const start = Date.parse(reasonData.Start), end = Date.parse(reasonData.End);
  if (!Number.isFinite(start) || start % 60000 || end - start !== 60000 || reasonData.Statistic !== statistic) throw invalid();
  return {name, arn, state, statistic, dimensions, start: new Date(start).toISOString(), end: new Date(end).toISOString(),
    namespace: value(node, 'Namespace'), metric: value(node, 'MetricName'), unit: value(node, 'Unit'),
    operator: value(node, 'ComparisonOperator'), threshold: value(node, 'Threshold'), reason: value(node, 'StateReason')};
}

export function cloudWatchMetrics(request, region, parse = source => new DOMParser().parseFromString(source, 'text/xml')) {
  async function query(action, fields, signal) {
    signal.throwIfAborted();
    const response = await request('/', {
      method: 'POST', credentials: 'same-origin', redirect: 'error', signal,
      headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'monitoring', 'X-Simulator-Region': region},
      body: new URLSearchParams({Action: action, Version: '2010-08-01', ...fields}),
    });
    if (!response.ok || !['text/xml', 'application/xml'].includes(response.headers.get('content-type')?.split(';')[0])) throw invalid();
    const root = parse(await response.text()).documentElement;
    signal.throwIfAborted();
    if (root.namespaceURI !== namespace || root.localName !== action + 'Response') throw invalid();
    value(container(root, 'ResponseMetadata'), 'RequestId');
    return container(root, action + 'Result');
  }

  async function describe(fields, signal) {
    const result = await query('DescribeAlarms', fields, signal);
    const alarms = children(container(result, 'MetricAlarms'), 'member').map(node => readAlarm(node, region));
    if (new Set(alarms.map(a => a.arn)).size !== alarms.length) throw invalid();
    return {alarms, next: value(result, 'NextToken', true)};
  }

  return {
    async list(token, signal) {
      const page = await describe({MaxRecords: String(alarmPageSize), ...(token ? {NextToken: token} : {})}, signal);
      if (page.alarms.length > alarmPageSize || (page.next && (page.next === token || page.next.length > 2048))) throw invalid();
      return page;
    },
    async named(name, signal) {
      const page = await describe({'AlarmNames.member.1': name}, signal);
      if (page.next || page.alarms.length > 1 || (page.alarms.length && page.alarms[0].name !== name)) throw invalid();
      return page.alarms[0] ?? null;
    },
    async measurement(alarm, signal) {
      const fields = {Namespace: alarm.namespace, MetricName: alarm.metric, Unit: alarm.unit, Period: '60',
        StartTime: alarm.start, EndTime: alarm.end, 'Statistics.member.1': alarm.statistic};
      alarm.dimensions.forEach((dim, index) => {
        fields['Dimensions.member.' + (index + 1) + '.Name'] = dim.name;
        fields['Dimensions.member.' + (index + 1) + '.Value'] = dim.value;
      });
      const result = await query('GetMetricStatistics', fields, signal);
      if (value(result, 'Label') !== alarm.metric) throw invalid();
      const points = children(container(result, 'Datapoints'), 'member');
      if (!points.length) return null;
      if (points.length !== 1 || value(points[0], 'Unit') !== alarm.unit
          || Date.parse(value(points[0], 'Timestamp')) !== Date.parse(alarm.start)) throw invalid();
      const text = value(points[0], alarm.statistic);
      if (text.trim() !== text || !/^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$/.test(text)) throw invalid();
      const measured = Number(text);
      if (!Number.isFinite(measured)) throw invalid();
      return measured;
    },
  };
}
