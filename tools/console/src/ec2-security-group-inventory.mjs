import {parseEC2Document} from './ec2-response.mjs';
import {vpcSecurityGroupsAvailable} from './ec2-security-groups.mjs';
import {vpcNativeSecurityGroupsAvailable, vpcNativeSecurityGroupRulesAvailable, nativeSGTarget} from './ec2-native-security-groups.mjs';

const localVpcID = /^vpc-[0-9a-f]{17}$/;
const validRegion = /^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$/;

export function securityGroupInventoryAvailable(capabilities, authenticationRequired) {
  const ops = capabilities?.services?.ec2?.operations;
  return vpcSecurityGroupsAvailable(capabilities, authenticationRequired) &&
    Array.isArray(ops) && ops.includes('DescribeVpcs') && ops.includes('DescribeSecurityGroups');
}

export function securityGroupVPCProfile(vpc, capabilities, authenticationRequired) {
  if (!securityGroupInventoryAvailable(capabilities, authenticationRequired) || typeof vpc?.id !== 'string' || vpc.id.trim() !== vpc.id || !localVpcID.test(vpc.id)) return null;
  if (vpc.managedBy === 'cloudformation') return 'stack-read-only';
  if (nativeSGTarget(vpc) && vpcNativeSecurityGroupsAvailable(capabilities, authenticationRequired))
    return vpcNativeSecurityGroupRulesAvailable(capabilities, authenticationRequired) ? 'native-rules' : 'native-groups';
  return null;
}

export async function securityGroupInventoryVPCs(request, region, {signal, parse = text =>
  new DOMParser().parseFromString(text, 'application/xml')} = {}) {
  if (typeof region !== 'string' || region.length > 32 || region.trim() !== region || !validRegion.test(region)) throw Error('Invalid EC2 region.');
  signal?.throwIfAborted();
  const reply = await request('/', {
    method: 'POST', redirect: 'error', signal,
    headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'ec2', 'X-Simulator-Region': region},
    body: new URLSearchParams({Action: 'DescribeVpcs', Version: '2016-11-15'}),
  });
  if (!reply.ok) throw Error(reply.status + ': VPC inventory was refused.');
  const text = await reply.text();
  signal?.throwIfAborted();
  const doc = parse(text);
  if (doc?.doctype) throw Error('Unexpected VPC inventory.');
  return parseEC2Document(doc, 'vpc');
}
