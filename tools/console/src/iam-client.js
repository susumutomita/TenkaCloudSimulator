// The console uses exactly the same IAM Query API as SDK callers.
export const xmlValue=(node,name)=>node.getElementsByTagName(name)[0]?.textContent||'';
export async function iamQuery(request,action,fields={},signal){
 const response=await request('/',{method:'POST',signal,headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'iam'},body:new URLSearchParams({Action:action,Version:'2010-05-08',...fields})});
 const xml=new DOMParser().parseFromString(await response.text(),'application/xml');
 if(xml.querySelector('parsererror'))throw Error('Invalid local IAM response.');
 return xml;
}
export const roleFromXML=node=>Object.fromEntries(['RoleName','RoleId','Arn','CreateDate','Description','MaxSessionDuration','AssumeRolePolicyDocument'].map(name=>[name,name==='AssumeRolePolicyDocument'?JSON.parse(decodeURIComponent(xmlValue(node,name))):xmlValue(node,name)]));
export async function listNativeRoles(request,signal){
 const roles=[];let marker='';
 do {const xml=await iamQuery(request,'ListRoles',{MaxItems:'100',...(marker?{Marker:marker}:{})},signal);roles.push(...[...xml.getElementsByTagName('Roles')[0].children].map(roleFromXML));marker=xmlValue(xml,'IsTruncated')==='true'?xmlValue(xml,'Marker'):'';}while(marker);
 return roles;
}

// Managed-policy responses have their own typed containers. Never infer empty
// success from a missing result, malformed document, or truncated page token.
export async function managedIAMQuery(request,action,fields={},signal){const xml=await iamQuery(request,action,fields,signal);if(xml.documentElement.localName!==action+'Response'||xml.documentElement.namespaceURI!=='https://iam.amazonaws.com/doc/2010-05-08/')throw Error('Invalid local IAM managed response.');return xml}
const direct=(node,name)=>[...node.children].find(child=>child.localName===name);
const required=(node,name)=>{const value=direct(node,name)?.textContent;if(!value)throw Error('Invalid local IAM '+name+' response.');return value};
export function iamResult(xml,action){const root=xml.documentElement;if(root.localName!==action+'Response')throw Error('Invalid local IAM action response.');const result=direct(root,action+'Result');if(!result)throw Error('Missing local IAM result.');return result}
export function managedPolicyFromXML(node){if(!node)throw Error('Missing local IAM policy.');const item={};for(const name of ['PolicyName','PolicyId','Arn','Path','DefaultVersionId'])item[name]=required(node,name);for(const name of ['Description','CreateDate','UpdateDate','AttachmentCount','IsAttachable']){const value=direct(node,name)?.textContent;if(value!==undefined)item[name]=value}return item}
function versionFromXML(node,document=false){if(!node)throw Error('Missing local IAM policy version.');const flag=required(node,'IsDefaultVersion');if(!['true','false'].includes(flag))throw Error('Invalid local IAM default version flag.');const item={VersionId:required(node,'VersionId'),IsDefaultVersion:flag==='true',CreateDate:direct(node,'CreateDate')?.textContent||''};if(document){try{item.Document=JSON.parse(decodeURIComponent(required(node,'Document')))}catch{throw Error('Invalid local IAM version document.')}}return item}
function pageResult(xml,action,container,parse){const result=iamResult(xml,action),list=direct(result,container);if(!list)throw Error('Missing local IAM collection.');const truncated=required(result,'IsTruncated');if(!['true','false'].includes(truncated))throw Error('Invalid local IAM pagination.');const marker=truncated==='true'?required(result,'Marker'):'';return {items:[...list.children].filter(n=>n.localName==='member').map(parse),marker}}
export async function getManagedPolicy(request,arn,signal){const xml=await managedIAMQuery(request,'GetPolicy',{PolicyArn:arn},signal);return managedPolicyFromXML(direct(iamResult(xml,'GetPolicy'),'Policy'))}
export async function managedPolicyPage(request,marker='',signal){const xml=await managedIAMQuery(request,'ListPolicies',{Scope:'Local',MaxItems:'20',...(marker?{Marker:marker}:{})},signal);const result=pageResult(xml,'ListPolicies','Policies',managedPolicyFromXML);if(marker&&result.marker===marker)throw Error('Repeated local IAM pagination marker.');return result}
export async function managedVersionPage(request,arn,marker='',signal){const xml=await managedIAMQuery(request,'ListPolicyVersions',{PolicyArn:arn,MaxItems:'100',...(marker?{Marker:marker}:{})},signal);return pageResult(xml,'ListPolicyVersions','Versions',n=>versionFromXML(n))}
export async function getManagedVersion(request,arn,version,signal){const xml=await managedIAMQuery(request,'GetPolicyVersion',{PolicyArn:arn,VersionId:version},signal);return versionFromXML(direct(iamResult(xml,'GetPolicyVersion'),'PolicyVersion'),true)}
export async function managedRolePage(request,name,marker='',signal){const xml=await managedIAMQuery(request,'ListAttachedRolePolicies',{RoleName:name,MaxItems:'100',...(marker?{Marker:marker}:{})},signal);return pageResult(xml,'ListAttachedRolePolicies','AttachedPolicies',n=>({PolicyName:required(n,'PolicyName'),PolicyArn:required(n,'PolicyArn')}))}
export async function allManagedPages(read){const items=[],seen=new Set();let marker='';do{const page=await read(marker);items.push(...page.items);marker=page.marker;if(marker){if(seen.has(marker))throw Error('Repeated local IAM pagination marker.');seen.add(marker)}}while(marker);return items}
