import {validInternetGatewayName,validInternetGatewayVPC} from './ec2-internet-gateways.mjs';
import {parseEC2Document} from './ec2-response.mjs';
import {readEC2RouteTableReply} from './ec2-route-table-pages.mjs';

const invalid=()=>{throw new Error('Could not confirm the saved VPC configuration.');};
const regionalID=/^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/;
const parseXML=source=>new DOMParser().parseFromString(source,'application/xml');

export function internetGatewayPageOperations(capabilities, authenticationRequired) {
  const ec2=capabilities?.services?.ec2, declared=ec2?.internetGatewayLifecycle, operations=ec2?.operations;
  const supports=id=>Array.isArray(declared)&&declared.includes(id)&&Array.isArray(operations)&&operations.includes(id);
  const read=authenticationRequired===true&&!capabilities?.consoleScope&&ec2?.console===true&&
    ec2.internetGatewayPageLimit===100&&supports('DescribeInternetGateways');
  const vpcs=read&&operations.includes('DescribeVpcs');
  return Object.freeze({read,vpcs,create:read&&supports('CreateInternetGateway'),delete:read&&supports('DeleteInternetGateway'),
    attach:vpcs&&supports('AttachInternetGateway'),detach:vpcs&&supports('DetachInternetGateway')});
}

export async function loadInternetGatewayVPCs(request,region,{signal,parse=parseXML}={}) {
  if (typeof region!=='string'||region.length>32||regionalID.exec(region)?.[0]!==region) invalid();
  signal?.throwIfAborted();
  // No principal parameters or URL beyond the ordinary same-origin EC2 Query path.
  const response=await request('/',{
    method:'POST',credentials:'same-origin',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:'DescribeVpcs',Version:'2016-11-15'}),
  });
  signal?.throwIfAborted();
  if (!response?.ok) throw new Error((response?.status??0)+': VPC inventory request failed.');
  const raw=await readEC2RouteTableReply(response,signal);
  signal?.throwIfAborted();
  const doc=parse(raw);
  if (!doc||doc.doctype) invalid();
  return Object.freeze(parseEC2Document(doc,'vpc').map(vpc=>{
    if (!validInternetGatewayVPC(vpc.id)||!validInternetGatewayName(vpc.name)||
        !['ec2','cloudformation'].includes(vpc.managedBy)) invalid();
    return Object.freeze(vpc);
  }));
}

// Keep the original gateway object with its native adapter transport binding;
// VPC names are merely a separately verified display projection.
export function filterInternetGateways(rows,vpcs,filter,{attached='Attached',detached='Detached'}={}) {
  const byID=new Map(vpcs.map(row=>[row.id,row]));
  const terms=filter.trim().toLocaleLowerCase().split(/\s+/u).filter(Boolean);
  if (!terms.length) return rows;
  return rows.filter(row=>{
    const haystack=[row.id,row.name,row.vpc,byID.get(row.vpc)?.name??'',row.vpc?attached:detached,
      row.vpc?'attached':'detached',row.managedBy].join(' ').toLocaleLowerCase();
    return terms.every(term=>haystack.includes(term));
  });
}

export function availableInternetGatewayVPCs(rows,vpcs) {
  const occupied=new Set(rows.map(row=>row.vpc).filter(Boolean));
  return vpcs.filter(vpc=>vpc.managedBy==='ec2'&&!occupied.has(vpc.id));
}

export function internetGatewayActions(row,inventory,vpcs) {
  if (!inventory.includes(row)||row?.managedBy!=='ec2') {
    return {attachTargets:[],detach:false,delete:false};
  }
  return {
    attachTargets:!row.vpc?availableInternetGatewayVPCs(inventory,vpcs):[],
    detach:!!row.vpc&&vpcs.some(vpc=>vpc.id===row.vpc&&vpc.managedBy==='ec2'),
    delete:!row.vpc,
  };
}
