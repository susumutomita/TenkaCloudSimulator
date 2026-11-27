import {describeInternetGateways} from './ec2-internet-gateways.mjs';
import {internetGatewayPageOperations} from './ec2-internet-gateway-console.mjs';
import {ipv4DefaultRoute,nativeIPv4RouteDestination} from './ec2-ipv4-route-prefix.mjs';
import {nativeSubnetCIDR,loadVpcSubnets,subnetTarget} from './ec2-native-subnets.mjs';
import {loadEC2RouteTablePages} from './ec2-route-table-pages.mjs';
import {vpcRouteTablesAvailable} from './ec2-route-tables.mjs';

const regionID=/^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/;
const vpcID=/^vpc-(?:[a-f0-9]{8}|[a-f0-9]{17})$/;
const exact=(value,pattern)=>typeof value==='string'&&pattern.exec(value)?.[0]===value;
const scopes=new WeakSet();
const empty=Object.freeze([]);
const result=(status,rows=empty)=>Object.freeze({status,rows});
const ready=rows=>result('ready',Object.freeze(rows.map(Object.freeze)));
const permits=(ops,name)=>Array.isArray(ops)&&ops.includes(name);
const denied=error=>/^(?:401:|403:|UnauthorizedOperation:|AccessDenied(?:Exception)?:|Sign in to continue\.)/.test(error?.message??'');

// The caller supplies the owner/CIDR witnessed by its current DescribeVpcs
// view. Report derived state only in the ordinary, signed-in Console.
export function createVpcResourceMapScope(request,region,vpc,capabilities,authenticationRequired) {
  const ec2=capabilities?.services?.ec2, ops=ec2?.operations;
  if (typeof request!=='function'||!exact(region,regionID)||region.length>32||!exact(vpc?.id,vpcID)||!subnetTarget(vpc)||
      nativeSubnetCIDR(vpc,vpc.cidr)!==vpc.cidr||authenticationRequired!==true||capabilities?.consoleScope||
      ec2?.console!==true||!permits(ops,'DescribeVpcs')) return null;
  const features=Object.freeze({
    subnets:permits(ops,'DescribeSubnets')&&permits(ec2.subnetLifecycle,'DescribeSubnets'),
    tables:permits(ops,'DescribeRouteTables')&&vpcRouteTablesAvailable(capabilities,authenticationRequired),
    gateways:internetGatewayPageOperations(capabilities,authenticationRequired).read,
  });
  if (!Object.values(features).some(Boolean)) return null;
  const scope=Object.freeze({request,region,features,vpc:Object.freeze({id:vpc.id,cidr:vpc.cidr,managedBy:vpc.managedBy})});
  scopes.add(scope);
  return scope;
}

async function read(enabled,task,signal) {
  if (!enabled) return result('unsupported');
  signal?.throwIfAborted();
  try {
    const rows=await task();
    signal?.throwIfAborted();
    return result('ready',rows);
  } catch (error) {
    signal?.throwIfAborted();
    return result(denied(error)?'denied':'error');
  }
}

function project(vpc,subnets,regionalTables,regionalGateways) {
  const tables=regionalTables.rows.filter(table=>table.vpc===vpc.id);
  const main=tables.find(table=>table.mainAssociation);
  // A local route under a different CIDR or native system markers seen under
  // a stack-managed VPC cannot witness the selected network.
  const invalidTableScope=tables.some(table=>vpc.managedBy==='ec2'
    ?table.localRoute&&table.localRoute.cidr!==vpc.cidr||
      table.routes.some(route=>nativeIPv4RouteDestination(route.cidr,vpc.cidr)!==route.cidr)
    :table.localRoute||table.mainAssociation||table.routes.length>1||
      table.routes.some(route=>route.cidr!==ipv4DefaultRoute));
  const tableStatus=invalidTableScope?'inconsistent':regionalTables.status;
  const attached=regionalGateways.rows.filter(gateway=>gateway.vpc===vpc.id);
  const invalidGatewayScope=attached.some(gateway=>gateway.managedBy!==vpc.managedBy);
  const gatewayStatus=invalidGatewayScope?'inconsistent':regionalGateways.status;

  const explicit=new Map(), known=new Set(subnets.rows.map(subnet=>subnet.id));
  let subnetAssociationStatus='unconfirmed';
  if (subnets.status==='ready'&&tableStatus==='ready') {
    subnetAssociationStatus='ready';
    // Read tables from the complete regional sequence. An orphan or a peer
    // table claiming one of these subnets makes the join inconsistent. In
    // particular, never emit a fallback after seeing an ambiguous association.
    for (const table of regionalTables.rows) {
      const ours=table.vpc===vpc.id;
      for (const association of table.associations) {
        const localSubnet=known.has(association.subnet);
        if (localSubnet!==ours || ours&&explicit.has(association.subnet)) {
          subnetAssociationStatus='inconsistent';
        }
        if (ours&&localSubnet) explicit.set(association.subnet,table.id);
      }
    }
  }
  function association(subnet) {
    if (subnetAssociationStatus!=='ready') return null;
    const id=explicit.get(subnet.id);
    if (id) return Object.freeze({id,kind:'explicit'});
    // Both typed inventories completed, and the native paired main/local row
    // matches the selected VPC. Legacy or absent markers are not inferred.
    if (main&&vpc.managedBy==='ec2'&&main.localRoute?.cidr===vpc.cidr) return Object.freeze({id:main.id,kind:'main'});
    return null;
  }

  const targetIDs=new Set(attached.map(row=>row.id)), gatewayRoutes=[];
  let routeTargetStatus='unconfirmed';
  if (tableStatus==='ready'&&gatewayStatus==='ready') {
    routeTargetStatus='ready';
    for (const table of tables) {
      for (const route of table.routes) {
        if (!targetIDs.has(route.gateway)) {routeTargetStatus='partial'; continue;}
        gatewayRoutes.push({table:table.id,cidr:route.cidr,gateway:route.gateway});
      }
    }
  }

  return Object.freeze({
    vpc,
    subnets:subnets.status==='ready'?ready(subnets.rows.map(s=>({id:s.id,cidr:s.cidr,az:s.az,association:association(s)}))):result(subnets.status),
    routeTables:tableStatus==='ready'?ready(tables.map(table=>({id:table.id,main:!!table.mainAssociation}))):result(tableStatus),
    gateways:gatewayStatus==='ready'?ready(attached.map(gateway=>({id:gateway.id}))):result(gatewayStatus),
    subnetAssociationStatus,
    gatewayRoutes:routeTargetStatus==='unconfirmed'?result(routeTargetStatus):
      result(routeTargetStatus,ready(gatewayRoutes).rows),
  });
}

// These are independent observed control-plane reads, not one transaction.
// DNS, SG, ACL, transport, guest instances and every mutation are out of scope.
export async function loadVpcResourceMap(scope,{signal}={}) {
  if (!scopes.has(scope)) throw Error('Invalid VPC resource map scope.');
  const {request,region,vpc,features}=scope;
  const [subnets,tables,gateways]=await Promise.all([
    read(features.subnets,()=>loadVpcSubnets(request,region,vpc,{signal}),signal),
    read(features.tables,async()=>(await loadEC2RouteTablePages(request,region,{signal})).tables,signal),
    read(features.gateways,()=>describeInternetGateways(request,region,signal),signal),
  ]);
  signal?.throwIfAborted();
  return project(vpc,subnets,tables,gateways);
}

export function startVpcResourceMapRead(scope,receive) {
  const controller=new AbortController();
  const done=loadVpcResourceMap(scope,{signal:controller.signal}).then(snapshot=>{
    if (!controller.signal.aborted) receive({scope,snapshot});
  },()=>{
    if (!controller.signal.aborted) receive({scope,error:true});
  });
  return {done,cancel:()=>controller.abort()};
}
