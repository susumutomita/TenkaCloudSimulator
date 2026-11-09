// Only native, allowlisted CloudFormation response fields enter this view.
// Template/property/parameter values and arbitrary failure text are not events.
const text = (node, name) => [...node.children].find(child => child.localName === name)?.textContent || '';
function result(xml, operation) {
  if (xml.getElementsByTagName('parsererror').length || xml.documentElement?.localName !== operation + 'Response') {
    throw Error('Invalid CloudFormation response.');
  }
  const node = [...xml.documentElement.children].find(child => child.localName === operation + 'Result');
  if (!node) throw Error('Invalid CloudFormation response.');
  return node;
}
export function stackEvents(xml) {
  const node = result(xml, 'DescribeStackEvents');
  const list = [...node.children].find(child => child.localName === 'StackEvents');
  if (!list) throw Error('Invalid CloudFormation event response.');
  return {
    events: [...list.children].filter(child => child.localName === 'member').map(event => ({
      id: text(event, 'EventId'), stackId: text(event, 'StackId'),
      timestamp: text(event, 'Timestamp'), status: text(event, 'ResourceStatus'),
      logicalId: text(event, 'LogicalResourceId'), physicalId: text(event, 'PhysicalResourceId'),
      resourceType: text(event, 'ResourceType'),
    })),
    nextToken: text(node, 'NextToken'),
  };
}
export function stackOutputs(xml) {
  const node = result(xml, 'DescribeStacks');
  const stacks = [...node.children].find(child => child.localName === 'Stacks');
  const stack = [...(stacks?.children || [])].find(child => child.localName === 'member');
  if (!stack) throw Error('The stack is not available. Deleted stacks have event history only.');
  const outputs = [...stack.children].find(child => child.localName === 'Outputs');
  return [...(outputs?.children || [])].filter(child => child.localName === 'member')
    .map(output => ({label: text(output, 'OutputKey'), value: text(output, 'OutputValue')}));
}
export async function queryStack(request, region, operation, stackName, {nextToken, signal} = {}) {
  if (!['DescribeStackEvents', 'DescribeStacks'].includes(operation)) throw Error('Unsupported stack read.');
  if (!stackName.trim()) throw Error('Enter a stack name or ID.');
  const response = await request('/', {
    method: 'POST', signal,
    headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Region': region},
    body: new URLSearchParams({Action: operation, Version: '2010-05-15', StackName: stackName.trim(), ...(nextToken ? {NextToken: nextToken} : {})}),
  });
  return new DOMParser().parseFromString(await response.text(), 'application/xml');
}
