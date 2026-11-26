import {parseEC2SecurityGroups} from './ec2-response.mjs';

export function vpcSecurityGroupsAvailable(capabilities, authenticationRequired) {
  const ec2 = capabilities?.services?.ec2;
  return authenticationRequired === true && !capabilities?.consoleScope && ec2?.console === true
    && ec2.securityGroupReadback === 'DescribeSecurityGroups';
}

export async function loadVpcSecurityGroups(request, region, vpc, {signal, parse = text => new DOMParser().parseFromString(text, 'application/xml')} = {}) {
  if (vpc?.managedBy !== 'cloudformation' || !/^vpc-[0-9a-f]{17}$/.test(vpc.id)) {
    throw Error('Select a verified CloudFormation-managed VPC.');
  }
  signal?.throwIfAborted();
  const response = await request('/', {
    method: 'POST', redirect: 'error', signal,
    headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'ec2', 'X-Simulator-Region': region},
    // The existing pane is still CFN-only. Ask for its specific VPC so native
    // detached SG metadata is not confused with legacy persisted rule detail.
    body: new URLSearchParams({Action: 'DescribeSecurityGroups', Version: '2016-11-15',
      'Filter.1.Name': 'vpc-id', 'Filter.1.Value.1': vpc.id}),
  });
  if (!response.ok) throw Error(response.status + ': Could not read security groups.');
  const xml = await response.text();
  signal?.throwIfAborted();
  return parseEC2SecurityGroups(parse(xml), vpc.id);
}

export function vpcSecurityGroupsFailure(error) {
  return /^(401|403|UnauthorizedOperation):/.test(error?.message ?? '') ? 'denied' : 'error';
}
