// Parse only the bounded EC2 responses. Missing state is not empty success.
const namespace='http://ec2.amazonaws.com/doc/2016-11-15/';
const invalid=()=>{throw Error('Invalid or incomplete EC2 response.');};
function nodes(node,name){
  const found=[...(node?.children??[])].filter(n=>n.localName===name);
  if(found.some(n=>n.namespaceURI!==namespace))invalid();
  return found;
}
function one(node,name,required=false){
  const found=nodes(node,name);
  if(found.length>1||(required&&found.length!==1))invalid();
  return found[0];
}
function value(node,name,required=false){
  const found=one(node,name,required);
  if(found?.children?.length)invalid();
  return found?.textContent??'';
}
function nameTag(node){
  const tags=nodes(one(node,'tagSet'),'item');
  const names=tags.filter(t=>value(t,'key')==='Name');
  if(names.length>1)invalid();
  return names.length?value(names[0],'value'):'';
}
// managedBy is the simulator extension: 'ec2' VPCs can be deleted natively,
// 'cloudformation' VPCs only with their stack; absent means unconfirmed.
function vpcRow(n){return {id:value(n,'vpcId'),name:nameTag(n),cidr:value(n,'cidrBlock'),managedBy:value(n,'simulatorManagedBy')};}
function instanceRow(n){
  const groups=nodes(one(n,'groupSet'),'item');
  const tagItems=nodes(one(n,'tagSet'),'item');
  if(groups.length>5||tagItems.length>50)invalid();
  const groupIds=groups.map(group=>value(group,'groupId',true));
  const tags=tagItems.map(tag=>({key:value(tag,'key',true),value:value(tag,'value',true)}));
  if(groupIds.some(id=>!id)||new Set(groupIds).size!==groupIds.length||
     tags.some(tag=>!tag.key)||new Set(tags.map(tag=>tag.key)).size!==tags.length)invalid();
  return {id:value(n,'instanceId'),name:tags.find(tag=>tag.key==='Name')?.value??'',
    state:value(n,'simulatorRuntimeState'),type:value(n,'instanceType'),image:value(n,'imageId'),
    vpc:value(n,'vpcId'),subnet:value(n,'subnetId'),groupIds,tags};
}
export function parseEC2Document(document,kind){
  if(kind!=='ec2'&&kind!=='vpc')invalid();
  const instance=kind==='ec2',root=document?.documentElement;
  if(root?.namespaceURI!==namespace||root.localName!==(instance?'DescribeInstancesResponse':'DescribeVpcsResponse'))invalid();
  if(value(root,'nextToken'))invalid();
  const records=instance?nodes(one(root,'reservationSet',true),'item').flatMap(r=>nodes(one(r,'instancesSet',true),'item')):nodes(one(root,'vpcSet',true),'item');
  if(records.length>1000)invalid();
  const rows=records.map(instance?instanceRow:vpcRow);
  if(rows.some(row=>!row.id)||new Set(rows.map(row=>row.id)).size!==rows.length)invalid();
  return rows;
}

export const VPC_NAME_LIMIT=256;
// One optional Name tag; the server validates the CIDR and tag bounds.
export function createVpcParameters({name='',cidr=''}){
  const params=new URLSearchParams({Action:'CreateVpc',Version:'2016-11-15',CidrBlock:cidr.trim()});
  if(name.length>VPC_NAME_LIMIT)throw Error('The Name tag can have up to 256 characters.');
  if(name)for(const [key,v] of [['ResourceType','vpc'],['Tag.1.Key','Name'],['Tag.1.Value',name]])params.set('TagSpecification.1.'+key,v);
  return params;
}
function root(document,name){
  const node=document?.documentElement;
  if(node?.namespaceURI!==namespace||node.localName!==name)invalid();
  return node;
}
export function parseCreateVpc(document){
  const row=vpcRow(one(root(document,'CreateVpcResponse'),'vpc',true));
  if(!row.id)invalid();
  return row;
}
export function parseDeleteVpc(document){
  if(value(root(document,'DeleteVpcResponse'),'return')!=='true')invalid();
  return true;
}
// EC2 Query errors are <Response><Errors><Error><Code/><Message/>. Anything else is null.
export function parseEC2Error(document){
  const node=document?.documentElement;
  if(node?.localName!=='Response')return null;
  const child=(parent,name)=>[...(parent?.children??[])].find(n=>n.localName===name);
  const error=child(child(node,'Errors'),'Error'),code=child(error,'Code')?.textContent??'';
  return code?{code,message:child(error,'Message')?.textContent??''}:null;
}
