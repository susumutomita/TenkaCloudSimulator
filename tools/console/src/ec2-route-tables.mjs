import {parseEC2RouteTables} from './ec2-response.mjs';

export function vpcRouteTablesAvailable(capabilities, authenticationRequired) {
  const ec2 = capabilities?.services?.ec2;
  return authenticationRequired === true && !capabilities?.consoleScope && ec2?.console === true
    && ec2.routeTableReadback === 'DescribeRouteTables';
}

export function vpcRouteScope(vpc) {
  if (!/^vpc-[0-9a-f]{17}$/.test(vpc?.id)) return 'unverified';
  return vpc.managedBy === 'cloudformation' ? 'stack' : vpc.managedBy === 'ec2' ? 'native' : 'unverified';
}

export async function loadVpcRouteTables(request, region, vpc, {signal, parse = text => new DOMParser().parseFromString(text, 'application/xml')} = {}) {
  if (vpcRouteScope(vpc) !== 'stack') throw Error('Select a verified CloudFormation-managed VPC.');
  signal?.throwIfAborted();
  const response = await request('/', {
    method:'POST', credentials:'same-origin', redirect:'error', signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:'DescribeRouteTables',Version:'2016-11-15'}),
  });
  if (!response.ok) throw Error(response.status + ': Could not read route tables.');
  const text = await response.text();
  signal?.throwIfAborted();
  return parseEC2RouteTables(parse(text), vpc.id);
}

export function routeTableFailure(error) {
  return /^(?:401:|403:|UnauthorizedOperation:|Sign in to continue\.)/.test(error?.message ?? '') ? 'denied' : 'error';
}
