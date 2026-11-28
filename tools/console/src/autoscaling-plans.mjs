const planner = 'autoscaling-plans';
const prefix = 'AnyScaleScalingPlannerFrontendService.';
export const dimensions = {read: 'dynamodb:table:ReadCapacityUnits', write: 'dynamodb:table:WriteCapacityUnits'};
const operations = ['CreateScalingPlan', 'DescribeScalingPlans', 'DescribeScalingPlanResources', 'UpdateScalingPlan', 'DeleteScalingPlan'];
const tablePattern = /^[A-Za-z0-9_.-]{3,255}$/;

export function scalingPlansAvailable(capabilities) {
  const service = capabilities?.services?.[planner];
  return !capabilities?.consoleScope && service?.console === true && service.targetBilling === 'PROVISIONED'
    && service.forecastData === false && service.predictiveScaling === false
    && Array.isArray(service.targets) && Object.values(dimensions).every(d => service.targets.includes(d))
    && Array.isArray(service.operations) && operations.every(op => service.operations.includes(op))
    && capabilities.services.dynamodb?.operations?.includes('DescribeTable') === true;
}

export function capacityBounds(minimum, maximum) {
  const parse = s => typeof s === 'string' && /^[1-9][0-9]{0,4}$/.test(s) ? Number(s) : 0;
  const min = parse(minimum), max = parse(maximum);
  return min > 0 && min <= max && max <= 40000 ? {MinCapacity: min, MaxCapacity: max} : null;
}

export function newPlan({name, tag, tagValue, table, dimension, minimum, maximum}) {
  const bounds = capacityBounds(minimum, maximum);
  if (!bounds || !/^[\x20-\x7e]{1,128}$/.test(name) || /[|:/]/.test(name) || !name.trim()
      || !tag.trim() || [...tag].length > 128 || !tagValue.trim() || [...tagValue].length > 256
      || !tablePattern.test(table) || !Object.hasOwn(dimensions, dimension)) return null;
  return {ScalingPlanName: name, ApplicationSource: {TagFilters: [{Key: tag, Values: [tagValue]}]},
    ScalingInstructions: [{ServiceNamespace: 'dynamodb', ResourceId: 'table/' + table,
      ScalableDimension: dimensions[dimension], ...bounds, DisableDynamicScaling: true}]};
}

function validName(x) { return typeof x === 'string' && x.length > 0; }
function instruction(x) {
  return x?.ServiceNamespace === 'dynamodb' && x.ResourceId?.startsWith('table/') && tablePattern.test(x.ResourceId.slice(6))
    && Object.values(dimensions).includes(x.ScalableDimension) && Number.isSafeInteger(x.MinCapacity) && Number.isSafeInteger(x.MaxCapacity);
}
function plan(x) {
  return validName(x?.ScalingPlanName) && x.ScalingPlanVersion === 1 && Number.isFinite(x.CreationTime)
    && validName(x.StatusCode) && Array.isArray(x.ScalingInstructions) && x.ScalingInstructions.length > 0 && x.ScalingInstructions.every(instruction);
}
function requireResponse(condition) { if (!condition) throw Error('Unexpected native Auto Scaling Plans response.'); }

export function scalingPlanClient(request, region) {
  async function call(service, target, body, signal) {
    const response = await request('/', {method: 'POST', signal, headers: {
      'Content-Type': service === 'dynamodb' ? 'application/x-amz-json-1.0' : 'application/x-amz-json-1.1', 'X-Simulator-Service': service, 'X-Simulator-Region': region, 'X-Amz-Target': target}, body: JSON.stringify(body)});
    const value = await response.json();
    signal.throwIfAborted();
    return value;
  }
  const scaling = (op, body, signal) => call(planner, prefix + op, body, signal);
  async function plans(body, signal) {
    const data = await scaling('DescribeScalingPlans', body, signal);
    requireResponse(Array.isArray(data.ScalingPlans) && data.ScalingPlans.every(plan) && (!data.NextToken || validName(data.NextToken)));
    return data;
  }
  return {
    list: (token, signal) => plans({MaxResults: 20, ...(token && {NextToken: token})}, signal),
    async detail(name, signal) {
      const data = await plans({ScalingPlanNames: [name], ScalingPlanVersion: 1}, signal);
      if (data.ScalingPlans.length === 0) return null;
      requireResponse(data.ScalingPlans.length === 1 && data.ScalingPlans[0].ScalingPlanName === name);
      const current = data.ScalingPlans[0];
      const result = await scaling('DescribeScalingPlanResources', {ScalingPlanName: name, ScalingPlanVersion: 1, MaxResults: 50}, signal);
      requireResponse(!result.NextToken && Array.isArray(result.ScalingPlanResources) && result.ScalingPlanResources.length === current.ScalingInstructions.length);
      const resources = current.ScalingInstructions.map(item => {
        const resource = result.ScalingPlanResources.find(r => r?.ResourceId === item.ResourceId && r.ScalableDimension === item.ScalableDimension);
        requireResponse(resource?.ScalingPlanName === name && resource.ScalingPlanVersion === 1 && validName(resource.ScalingStatusCode));
        return {...item, state: resource.ScalingStatusCode, policies: resource.ScalingPolicies?.length || 0};
      });
      return {plan: current, resources};
    },
    async capacity(table, signal) {
      const {Table: t} = await call('dynamodb', 'DynamoDB_20120810.DescribeTable', {TableName: table}, signal);
      requireResponse(t?.TableName === table && t.TableArn === `arn:aws:dynamodb:${region}:000000000000:table/${table}`
        && Number.isSafeInteger(t.ProvisionedThroughput?.ReadCapacityUnits) && Number.isSafeInteger(t.ProvisionedThroughput?.WriteCapacityUnits));
      return t.ProvisionedThroughput;
    },
    async create(body, signal) {
      const result = await scaling('CreateScalingPlan', body, signal);
      requireResponse(result.ScalingPlanVersion === 1);
    },
    async update(current, minimum, maximum, signal) {
      const bounds = capacityBounds(minimum, maximum);
      requireResponse(bounds && current.ScalingInstructions.length === 1);
      const item = {...current.ScalingInstructions[0], ...bounds};
      if (item.TargetTrackingConfigurations == null) delete item.TargetTrackingConfigurations;
      await scaling('UpdateScalingPlan', {ScalingPlanName: current.ScalingPlanName, ScalingPlanVersion: 1, ScalingInstructions: [item]}, signal);
    },
    remove: (name, signal) => scaling('DeleteScalingPlan', {ScalingPlanName: name, ScalingPlanVersion: 1}, signal),
  };
}
