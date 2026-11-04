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
