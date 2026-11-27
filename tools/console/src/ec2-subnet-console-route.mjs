const subnetId = /^subnet-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const vpcId = /^vpc-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const regionId = /^[a-z]{2}(?:-[a-z0-9]+)+-\d+$/;

export function subnetConsoleRoute(hash) {
  const match = /^#\/vpc\/subnets(?:\/([^/?#]+))?(?:\?([^#]*))?$/.exec(hash);
  if (!match) return null;
  const target = match[1] || '';
  if (target && target !== 'create' && !subnetId.test(target)) return null;
  const vpc = new URLSearchParams(match[2]).get('vpc');
  return {target, vpc:vpcId.test(vpc) ? vpc : ''};
}

export function subnetConsoleHref(region, target = '', vpc = '') {
  if (target && target !== 'create' && !subnetId.test(target)) throw Error('Invalid subnet console location.');
  const query = new URLSearchParams();
  if (typeof region==='string' && region.length<=32 && regionId.test(region)) query.set('region',region);
  if (vpcId.test(vpc)) query.set('vpc',vpc);
  return '#/vpc/subnets' + (target ? '/'+target : '') + (query.size?'?'+query:'');
}

// These tables come from the existing complete, typed regional read. An
// empty explicit association result does not by itself identify a main table.
export function subnetRouteTable(tables, subnet) {
  const matching = tables.filter(table => table.associations.some(assoc => assoc.subnet === subnet.id));
  if (matching.length > 1 || matching.some(table => table.vpc !== subnet.vpc)) throw Error('Unconfirmed subnet association.');
  if (matching.length === 1) return {id:matching[0].id, kind:'explicit'};
  const main = tables.filter(table => table.vpc === subnet.vpc && table.mainAssociation);
  if (main.length > 1 || main.length && subnet.owner !== 'ec2') throw Error('Unconfirmed main route table.');
  return main.length === 1 ? {id:main[0].id,kind:'main'} : null;
}
