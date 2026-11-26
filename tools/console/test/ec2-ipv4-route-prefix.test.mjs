import assert from 'node:assert/strict';
import test from 'node:test';
import {compareIPv4RoutePrefixes,ec2IPv4Prefix,nativeIPv4RouteDestination,nativeIPv4RouteLimit} from '../src/ec2-ipv4-route-prefix.mjs';

test('IPv4 route masking and range math use unsigned, exact CIDRs',()=>{
  assert.equal(nativeIPv4RouteLimit,16);
  for(const [input,canonical] of [
    ['198.51.100.99/24','198.51.100.0/24'],['198.51.100.128/25','198.51.100.128/25'],
    ['192.0.2.1/0','0.0.0.0/0'],['255.255.255.255/32','255.255.255.255/32'],
  ]) assert.equal(ec2IPv4Prefix(input)?.masked,canonical);
  for(const invalid of ['','010.0.0.1/16','10.0.0.1/016','198.51.100.256/25','198.51.100.1/33',
    '::ffff:198.51.100.1/32','198.51.100.1/32\n','10.0.1/16','10.0.0.1/16.0'])
    assert.equal(ec2IPv4Prefix(invalid),null);
  const ordered=['0.0.0.0/0','192.0.2.0/24','198.51.100.0/24','198.51.100.0/25','198.51.100.128/25'];
  assert.deepEqual([...ordered].reverse().map(ec2IPv4Prefix).sort(compareIPv4RoutePrefixes).map(item=>item.masked),ordered);
});

test('the optional default is compatible; non-defaults must be outside both protected ranges',()=>{
  const vpc='10.91.0.0/16';
  for(const cidr of ['10.91.0.0/16','10.91.0.1/32','10.90.0.0/15','169.254.168.0/22','169.254.169.1/32',
    '169.254.160.0/19']) assert.equal(nativeIPv4RouteDestination(cidr,vpc),null);
  assert.equal(nativeIPv4RouteDestination('10.91.0.1/0',vpc),'0.0.0.0/0');
  assert.equal(nativeIPv4RouteDestination('198.51.100.9/24',vpc),'198.51.100.0/24');
  assert.equal(nativeIPv4RouteDestination('198.51.100.9/24','10.91.0.1/16'),null);
});
