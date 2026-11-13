// Parse only the bounded readonly EC2 response. Missing state is not empty success.
const namespace='http://ec2.amazonaws.com/doc/2016-11-15/';
const invalid=()=>{throw Error('Invalid or incomplete EC2 discovery response.');};
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
export function parseEC2Document(document,kind){
  if(kind!=='ec2'&&kind!=='vpc')invalid();
  const instance=kind==='ec2',root=document?.documentElement;
  if(root?.namespaceURI!==namespace||root.localName!==(instance?'DescribeInstancesResponse':'DescribeVpcsResponse'))invalid();
  if(value(root,'nextToken'))invalid();
  const records=instance?nodes(one(root,'reservationSet',true),'item').flatMap(r=>nodes(one(r,'instancesSet',true),'item')):nodes(one(root,'vpcSet',true),'item');
  if(records.length>1000)invalid();
  const rows=records.map(n=>instance?{id:value(n,'instanceId'),name:nameTag(n),state:value(n,'simulatorRuntimeState'),type:value(n,'instanceType'),image:value(n,'imageId'),vpc:value(n,'vpcId'),subnet:value(n,'subnetId')}:{id:value(n,'vpcId'),name:nameTag(n),cidr:value(n,'cidrBlock')});
  if(rows.some(row=>!row.id)||new Set(rows.map(row=>row.id)).size!==rows.length)invalid();
  return rows;
}
