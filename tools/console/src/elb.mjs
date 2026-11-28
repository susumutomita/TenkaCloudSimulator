const service = 'elasticloadbalancing';
const version = '2015-12-01';
const namespace = 'http://elasticloadbalancing.amazonaws.com/doc/' + version + '/';
const reads = ['DescribeLoadBalancers', 'DescribeListeners', 'DescribeTargetGroups', 'DescribeTargetHealth'];

export function elbAvailable(capabilities) {
  const native = capabilities?.services?.[service];
  return !capabilities?.consoleScope && native?.console === true && native.protocol === 'Query/XML ' + version &&
    Array.isArray(native.operations) && [...reads, 'ModifyTargetGroup'].every(operation => native.operations.includes(operation));
}

export function validHealthPath(value) {
  return typeof value === 'string' && value.startsWith('/') && value.length <= 1024 && /^[!-~]+$/.test(value);
}

export class TargetGroupUnlinked extends Error {}

const invalid = () => { throw Error('Invalid Elastic Load Balancing response.'); };
function children(node, name) {
  const matches = [...node.children].filter(child => child.localName === name);
  if (matches.some(child => child.namespaceURI !== namespace)) invalid();
  return matches;
}
function child(node, name, optional = false) {
  const matches = children(node, name);
  if (matches.length > 1 || (!optional && matches.length === 0)) invalid();
  return matches[0];
}
function value(node, name, optional = false) {
  const entry = child(node, name, optional);
  if (!entry) return '';
  if (entry.children.length || !entry.textContent) invalid();
  return entry.textContent;
}
const members = (node, name) => children(child(node, name), 'member');
function exactly(items, arn) {
  if (items.length !== 1 || items[0].arn !== arn) invalid();
  return items[0];
}

export function elbClient(request, region) {
  const prefix = 'arn:aws:elasticloadbalancing:' + region + ':000000000000:';
  function identifier(node, key, kind) {
    const arn = value(node, key);
    if (!arn.startsWith(prefix + kind + '/') || arn.length <= prefix.length + kind.length + 1) invalid();
    return arn;
  }
  async function call(action, fields, signal) {
    const result = await request('/', {method: 'POST', signal,
      headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': service, 'X-Simulator-Region': region},
      body: new URLSearchParams({Action: action, Version: version, ...fields}).toString()});
    const raw = await result.text();
    signal.throwIfAborted();
    const root = new DOMParser().parseFromString(raw, 'application/xml').documentElement;
    if (root.namespaceURI !== namespace || root.localName !== action + 'Response') invalid();
    if (!value(child(root, 'ResponseMetadata'), 'RequestId')) invalid();
    return child(root, action + 'Result');
  }
  function loadBalancer(node) {
    return {arn: identifier(node, 'LoadBalancerArn', 'loadbalancer/app'), name: value(node, 'LoadBalancerName'),
      state: value(child(node, 'State'), 'Code'), vpc: value(node, 'VpcId', true),
      subnets: members(node, 'AvailabilityZones').map(z => ({id: value(z, 'SubnetId'), zone: value(z, 'ZoneName', true)})),
      groups: members(node, 'SecurityGroups').map(group => {
        if (group.children.length || !group.textContent) invalid();
        return group.textContent;
      })};
  }
  function targetGroup(node) {
    const enabled = value(node, 'HealthCheckEnabled'), path = value(node, 'HealthCheckPath');
    if (!['true','false'].includes(enabled) || !validHealthPath(path) || value(node, 'TargetType') !== 'lambda') invalid();
    const parents = members(node, 'LoadBalancerArns');
    if (parents.length > 1) invalid();
    const parent = parents[0]?.textContent ?? '';
    if (parents.length && (parents[0].children.length || !parent.startsWith(prefix + 'loadbalancer/app/'))) invalid();
    return {arn: identifier(node, 'TargetGroupArn', 'targetgroup'), name: value(node, 'TargetGroupName'), path, enabled: enabled === 'true', parent};
  }
  function linked(group, parent) {
    if (group.parent !== parent) throw new TargetGroupUnlinked();
    return group;
  }
  async function linkedGroup(arn, parent, signal) {
    const result = await call('DescribeTargetGroups', {'TargetGroupArns.member.1': arn}, signal);
    return linked(exactly(members(result, 'TargetGroups').map(targetGroup), arn), parent);
  }
  function listener(node, arn) {
    if (value(node, 'LoadBalancerArn') !== arn) invalid();
    const action = members(node, 'DefaultActions');
    if (action.length !== 1) invalid();
    const type = value(action[0], 'Type');
    if (!['forward','fixed-response'].includes(type)) invalid();
    return {arn: identifier(node, 'ListenerArn', 'listener/app'), protocol: value(node, 'Protocol'), port: value(node, 'Port'), type,
      destination: type === 'forward' ? identifier(action[0], 'TargetGroupArn', 'targetgroup')
        : value(child(action[0], 'FixedResponseConfig'), 'StatusCode')};
  }
  return {
    linkedGroup,
    async list(name, marker, signal) {
      const fields = {PageSize: '20'};
      if (name) fields['Names.member.1'] = name;
      if (marker) fields.Marker = marker;
      let doc;
      try { doc = await call('DescribeLoadBalancers', fields, signal); }
      catch (error) {
        if (name && !marker && /^LoadBalancerNotFound:/.test(error.message)) return {rows: [], next: ''};
        throw error;
      }
      const rows = members(doc, 'LoadBalancers').map(loadBalancer);
      if (name && rows.some(row => row.name !== name)) invalid();
      return {rows, next: value(doc, 'NextMarker', true)};
    },
    async balancer(arn, signal) {
      const [lbs, listeners, groups] = await Promise.all([
        call('DescribeLoadBalancers', {'LoadBalancerArns.member.1': arn}, signal),
        call('DescribeListeners', {LoadBalancerArn: arn, PageSize: '100'}, signal),
        call('DescribeTargetGroups', {LoadBalancerArn: arn, PageSize: '100'}, signal),
      ]);
      if ([lbs, listeners, groups].some(result => value(result, 'NextMarker', true))) invalid();
      return {lb: exactly(members(lbs,'LoadBalancers').map(loadBalancer),arn),
        listeners: members(listeners, 'Listeners').map(item => listener(item, arn)),
        groups: members(groups, 'TargetGroups').map(node => linked(targetGroup(node), arn))};
    },
    async group(arn, parent, signal) {
      const [group, targets] = await Promise.all([
        linkedGroup(arn, parent, signal),
        call('DescribeTargetHealth', {TargetGroupArn: arn}, signal),
      ]);
      const health = members(targets,'TargetHealthDescriptions').map(item => {
        const id = value(child(item, 'Target'), 'Id'), result = child(item, 'TargetHealth');
        if (!id.startsWith('arn:aws:lambda:' + region + ':000000000000:function:')) invalid();
        return {id, state: value(result,'State'), reason: value(result,'Reason',true)};
      });
      return {...group, health};
    },
    async modify(arn, parent, path, signal) {
      if (!validHealthPath(path)) throw Error('Invalid health check path.');
      const result = await call('ModifyTargetGroup',{TargetGroupArn: arn, HealthCheckPath: path},signal);
      if (linked(exactly(members(result,'TargetGroups').map(targetGroup),arn), parent).path !== path) invalid();
    },
  };
}
