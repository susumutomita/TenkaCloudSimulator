import {parseEC2RouteTables} from './ec2-response.mjs';
import {ipv4DefaultRoute} from './ec2-ipv4-route-prefix.mjs';

export function vpcRouteTablesAvailable(capabilities, authenticationRequired) {
  const ec2 = capabilities?.services?.ec2;
  return authenticationRequired === true && !capabilities?.consoleScope && ec2?.console === true
    && ec2.routeTableReadback === 'DescribeRouteTables';
}

export function vpcRouteScope(vpc) {
  if (!/^vpc-[0-9a-f]{17}$/.test(vpc?.id)) return 'unverified';
  return vpc.managedBy === 'cloudformation' ? 'stack' : vpc.managedBy === 'ec2' ? 'native' : 'unverified';
}

export function validRouteTableSubnetId(value) {
  return typeof value === 'string' && /^subnet-(?:[0-9a-f]{8}|[0-9a-f]{17})$/.exec(value)?.[0] === value;
}

export async function loadVpcRouteTables(request, region, vpc, {signal, subnetId = '', parse = text => new DOMParser().parseFromString(text, 'application/xml')} = {}) {
  if (vpcRouteScope(vpc) !== 'stack') throw Error('Select a verified CloudFormation-managed VPC.');
  if (subnetId !== '' && !validRouteTableSubnetId(subnetId)) throw Error('Enter an exact subnet ID.');
  signal?.throwIfAborted();
  const body = new URLSearchParams({Action:'DescribeRouteTables',Version:'2016-11-15'});
  if (subnetId) {
    body.set('Filter.1.Name', 'association.subnet-id');
    body.set('Filter.1.Value.1', subnetId);
  }
  const response = await request('/', {
    method:'POST', credentials:'same-origin', redirect:'error', signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body,
  });
  if (!response.ok) throw Error(response.status + ': Could not read route tables.');
  const text = await response.text();
  signal?.throwIfAborted();
  const tables=parseEC2RouteTables(parse(text), vpc.id, subnetId);
  // Other VPCs may contain multiple native routes; a verified CloudFormation
  // view retains its existing bounded one-default-route projection.
  if(tables.some(row=>row.routes.length>1||row.routes[0]&&row.routes[0].cidr!==ipv4DefaultRoute))
    throw Error('Invalid CloudFormation-managed route table.');
  return tables;
}

export function routeTableFailure(error) {
  return /^(?:401:|403:|UnauthorizedOperation:|Sign in to continue\.)/.test(error?.message ?? '') ? 'denied' : 'error';
}
