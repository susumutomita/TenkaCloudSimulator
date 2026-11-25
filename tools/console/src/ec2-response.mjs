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
function value(node,name){
  const found=one(node,name);
  if(found?.children?.length)invalid();
  return found?.textContent??'';
}
function nameTag(node){
  const tags=nodes(one(node,'tagSet'),'item');
  const names=tags.filter(t=>value(t,'key')==='Name');
  if(names.length>1)invalid();
  return names.length?value(names[0],'value'):'';
}
// managedBy is the simulator extension: 'ec2' resources can be deleted natively,
// 'cloudformation' resources only with their stack; absent means unconfirmed.
function vpcRow(n){return {id:value(n,'vpcId'),name:nameTag(n),cidr:value(n,'cidrBlock'),managedBy:value(n,'simulatorManagedBy')};}
function subnetRow(n){return {id:value(n,'subnetId'),name:nameTag(n),vpc:value(n,'vpcId'),cidr:value(n,'cidrBlock'),zone:value(n,'availabilityZone'),managedBy:value(n,'simulatorManagedBy')};}
// state is the recorded simulatorRuntimeState. AWS instanceState is never read:
// the simulator does not report one.
function instanceRow(n){return {id:value(n,'instanceId'),name:nameTag(n),state:value(n,'simulatorRuntimeState'),type:value(n,'instanceType'),image:value(n,'imageId'),vpc:value(n,'vpcId'),subnet:value(n,'subnetId'),managedBy:value(n,'simulatorManagedBy')};}
const collections={
  ec2:['DescribeInstancesResponse',root=>nodes(one(root,'reservationSet',true),'item').flatMap(r=>nodes(one(r,'instancesSet',true),'item')),instanceRow],
  vpc:['DescribeVpcsResponse',root=>nodes(one(root,'vpcSet',true),'item'),vpcRow],
  subnets:['DescribeSubnetsResponse',root=>nodes(one(root,'subnetSet',true),'item'),subnetRow],
};
export const describeAction=kind=>collections[kind]?.[0].replace(/Response$/,'')??invalid();
export function parseEC2Document(document,kind){
  if(!Object.hasOwn(collections,kind))invalid();
  const [name,items,row]=collections[kind],root=document?.documentElement;
  if(root?.namespaceURI!==namespace||root.localName!==name)invalid();
  if(value(root,'nextToken'))invalid();
  const records=items(root);
  if(records.length>1000)invalid();
  const rows=records.map(row);
  if(rows.some(row=>!row.id)||new Set(rows.map(row=>row.id)).size!==rows.length)invalid();
  return rows;
}

export const VPC_NAME_LIMIT=256;
export const INSTANCE_TYPES=Object.freeze(['t3.micro','t3.small']);
function parameters(action,fields,kind,name){
  if(name.length>VPC_NAME_LIMIT)throw Error('The Name tag can have up to 256 characters.');
  const params=new URLSearchParams({Action:action,Version:'2016-11-15',...fields});
  if(name)for(const [key,v] of [['ResourceType',kind],['Tag.1.Key','Name'],['Tag.1.Value',name]])params.set('TagSpecification.1.'+key,v);
  return params;
}
// One optional Name tag; the server validates the CIDR and tag bounds.
export function createVpcParameters({name='',cidr=''}){return parameters('CreateVpc',{CidrBlock:cidr.trim()},'vpc',name);}
// An empty zone lets the server choose, as "No preference" does in AWS.
export function createSubnetParameters({vpc='',name='',cidr='',zone=''}){
  return parameters('CreateSubnet',{VpcId:vpc,CidrBlock:cidr.trim(),...(zone?{AvailabilityZone:zone}:{})},'subnet',name);
}
// Exactly one launch record. token makes a repeated submission return the
// record it already created instead of a second one.
export function runInstancesParameters({name='',image='',type='',subnet='',token=''}){
  return parameters('RunInstances',{ImageId:image.trim(),InstanceType:type,MinCount:'1',MaxCount:'1',SubnetId:subnet,...(token?{ClientToken:token}:{})},'instance',name);
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
export function parseCreateSubnet(document){
  const row=subnetRow(one(root(document,'CreateSubnetResponse'),'subnet',true));
  if(!row.id)invalid();
  return row;
}
function returned(document,name){
  if(value(root(document,name),'return')!=='true')invalid();
  return true;
}
export function parseDeleteVpc(document){return returned(document,'DeleteVpcResponse');}
export function parseDeleteSubnet(document){return returned(document,'DeleteSubnetResponse');}
export function parseRunInstances(document){
  const records=nodes(one(root(document,'RunInstancesResponse'),'instancesSet',true),'item');
  if(records.length!==1)invalid();
  const row=instanceRow(records[0]);
  if(!row.id)invalid();
  return row;
}
// Success is exactly the requested instance reported as terminated.
export function parseTerminateInstances(document,id){
  const records=nodes(one(root(document,'TerminateInstancesResponse'),'instancesSet',true),'item');
  if(!id||records.length!==1||value(records[0],'instanceId')!==id||value(one(records[0],'currentState',true),'name')!=='terminated')invalid();
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
