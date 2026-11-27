// This presentation is only for the advertised AWS Batch metadata slice.
const namePattern=/^[A-Za-z0-9_-]{1,128}$/;
const imagePattern=/^[a-z0-9]+([._-][a-z0-9]+)*(\/[a-z0-9]+([._-][a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?$/;
const quantity=(value,min)=>/^[1-9][0-9]{0,9}$/.test(value)&&Number(value)>=min&&Number(value)<=2147483647;

export function isBatchJobDefinition(service,resource){
 return service?.model==='batch'&&service?.signingName==='batch'&&service?.protocol==='rest-json'&&resource?.kind==='jobDefinition';
}

export function batchDefinitionOperations(service,resource){
 if(!isBatchJobDefinition(service,resource))return {};
 const {create,describe,update}=resource.operations||{};
 const matches=(op,name,uri)=>op?.name===name&&op.method==='POST'&&op.uri===uri;
 const member=(op,name,type)=>op.members?.some(m=>m.name===name&&m.type===type&&(!m.locationName||m.locationName===name)&&!m.location);
 return {
  create:matches(create,'RegisterJobDefinition','/v1/registerjobdefinition')&&
   member(create,'jobDefinitionName','string')&&member(create,'type','string')&&member(create,'platformCapabilities','list')&&
   member(create,'containerProperties','structure')?create:null,
  describe:matches(describe,'DescribeJobDefinitions','/v1/describejobdefinitions')&&describe.names==='jobDefinitions'&&
   describe.items==='jobDefinitions'&&member(describe,describe.names,'list')?describe:null,
  deregister:matches(update,'DeregisterJobDefinition','/v1/deregisterjobdefinition')&&update.lookup==='jobDefinition'&&
   member(update,update.lookup,'string')?update:null,
 };
}

// The family is never a console action target. Require a matching exact
// revision ARN from the current service/region and the row's saved identity.
export function batchDefinitionARN(service,resource,row,region){
 if(!isBatchJobDefinition(service,resource)||resource.arnWire!=='jobDefinitionArn'||
  typeof region!=='string'||!/^[a-z0-9-]{1,32}$/.test(region)||
  typeof service.account!=='string'||!/^\d{12}$/.test(service.account)||!row||
  typeof row.jobDefinitionName!=='string'||!namePattern.test(row.jobDefinitionName)||
  !Number.isSafeInteger(row.revision)||row.revision<1||row.revision>2147483647)return '';
 const expected='arn:aws:batch:'+region+':'+service.account+':job-definition/'+row.jobDefinitionName+':'+row.revision;
 return row.jobDefinitionArn===expected?expected:'';
}

export function batchDefinitionInput(values){
 const [jobDefinitionName,image,vcpus,memoryMiB]=['jobDefinitionName','image','vcpus','memoryMiB'].map(
  key=>typeof values[key]==='string'?values[key].trim():'');
 const invalid=[];
 if(!namePattern.test(jobDefinitionName))invalid.push('jobDefinitionName');
 if(image.length>255||!imagePattern.test(image))invalid.push('image');
 if(!quantity(vcpus,1))invalid.push('vcpus');
 if(!quantity(memoryMiB,4))invalid.push('memoryMiB');
 return invalid.length?{invalid,input:null}:{invalid,input:{jobDefinitionName,type:'container',platformCapabilities:['EC2'],
  containerProperties:{image,resourceRequirements:[{type:'VCPU',value:vcpus},{type:'MEMORY',value:memoryMiB}]}}};
}
