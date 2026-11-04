import React,{useEffect,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import TextFilter from '@cloudscape-design/components/text-filter';

const root='/_simulator/v1/iam/principals';
const arn=name=>'arn:aws:iam::000000000000:role/'+name;
const denyAll=JSON.stringify({Version:'2012-10-17',Statement:[{Effect:'Deny',Action:'*',Resource:'*'}]},null,2);
const identifier=text=><span className="resource-identifier">{text}</span>;

export default function IAMView({request,onSaving,onLambda}){
 const [roles,setRoles]=useState([]),[loading,setLoading]=useState(true),[error,setError]=useState(''),[message,setMessage]=useState(''),[filter,setFilter]=useState(''),[creating,setCreating]=useState(false),[name,setName]=useState(''),[policy,setPolicy]=useState(denyAll),[detail,setDetail]=useState(null),[editing,setEditing]=useState(false),[saving,setSaving]=useState(false);
 const revision=useRef(0),alive=useRef(true),controller=useRef(new AbortController());
 const api=async(path='',options={})=>{const response=await request(root+path,{...options,signal:controller.current.signal,headers:{'Content-Type':'application/json',...options.headers}});return response.json()};
 async function load(){setLoading(true);setError('');try{const data=await api();if(alive.current)setRoles(data.principals.filter(name=>name!=='simulator-admin'))}catch(e){if(alive.current)setError(e.message)}finally{if(alive.current)setLoading(false)}}
 useEffect(()=>{load();return()=>{alive.current=false;controller.current.abort();onSaving(false)}},[]);
 useEffect(()=>{onSaving(saving)},[saving]);
 async function inspect(role){const current=++revision.current;setError('');try{const document=await api('/'+encodeURIComponent(role)+'/policy');if(alive.current&&current===revision.current){setDetail(role);setPolicy(JSON.stringify(document,null,2));setEditing(false)}}catch(e){if(alive.current)setError(e.message)}}
 async function save(){setSaving(true);setError('');setMessage('');try{
  const role=creating?name:detail;
  if(!/^[a-zA-Z0-9_-]{1,64}$/.test(role)||role==='simulator-admin')throw Error('Use a non-reserved role name with 1–64 letters, digits, hyphens or underscores.');
  let document;try{document=JSON.parse(policy)}catch{throw Error('Enter valid permissions policy JSON.');}
  await api('/'+encodeURIComponent(role)+'/policy',{method:'PUT',headers:creating?{'If-None-Match':'*'}:{},body:JSON.stringify(document)});
  if(alive.current){setCreating(false);setEditing(false);setDetail(role);setMessage('Role permissions saved.');await load()}
 }catch(e){if(alive.current)setError(e.message)}finally{if(alive.current)setSaving(false)}}
 const start=()=>{revision.current++;setCreating(true);setName('');setPolicy(denyAll);setDetail(null);setError('');setMessage('')};
 const fields=<SpaceBetween size="l">{creating&&<FormField label="Role name" constraintText="1–64 letters, digits, hyphens or underscores. Existing identities cannot be overwritten."><Input autoFocus value={name} onChange={({detail})=>setName(detail.value)}/></FormField>}<FormField label="Permissions policy JSON" description="The default explicit Deny grants no service access. Replace it with the least permissions your function needs; an explicit Deny takes precedence over Allow."><Textarea rows={12} value={policy} onChange={({detail})=>setPolicy(detail.value)}/></FormField></SpaceBetween>;
 const visible=roles.filter(role=>role.toLowerCase().includes(filter.toLowerCase()));
 return <SpaceBetween size="l">
  <Alert type="info" header="Local IAM subset">These global execution roles use the simulator's existing identity policies. Users and roles share one local name space. STS, trust-policy evaluation, AWS managed policies and the native IAM Query API are not implemented. This creates no AWS role or CloudWatch permissions.</Alert>
  {error&&<Alert type="error">{error}</Alert>}{message&&<Alert type="success" dismissible dismissAriaLabel="Dismiss notification" onDismiss={()=>setMessage('')}>{message}</Alert>}
  {creating||editing?<ContentLayout header={<Header variant="h1">{creating?'Create role':'Edit permissions'}</Header>}><form onSubmit={e=>{e.preventDefault();save()}}><Form actions={<SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={()=>{setCreating(false);setEditing(false);setError('')}}>Cancel</Button><Button variant="primary" formAction="submit" loading={saving}>{creating?'Create role':'Save changes'}</Button></SpaceBetween>}><Container header={<Header variant="h2">{creating?'Role details':'Permissions'}</Header>}>{fields}</Container></Form></form></ContentLayout>:detail?<ContentLayout header={<Header variant="h1" actions={<Button onClick={()=>{setDetail(null);setError('');setMessage('')}}>Roles</Button>}>{identifier(detail)}</Header>}><SpaceBetween size="l"><Container header={<Header variant="h2">Summary</Header>}><KeyValuePairs columns={1} items={[{label:'Role ARN',value:identifier(arn(detail))},{label:'Scope',value:'Global local identity'},{label:'Trusted entities',value:'Not evaluated by this simulator'}]}/></Container><Container header={<Header variant="h2" actions={<Button onClick={()=>setEditing(true)}>Edit permissions</Button>}>Permissions</Header>}><FormField label="Permissions policy JSON"><Textarea readOnly rows={12} value={policy}/></FormField></Container><Button variant="primary" onClick={onLambda}>Create a Lambda function</Button></SpaceBetween></ContentLayout>:<Table variant="full-page" loading={loading} loadingText="Loading roles" items={visible} trackBy={role=>role} ariaLabels={{tableLabel:'IAM roles'}} columnDefinitions={[{id:'name',header:'Role name',cell:role=><Link href="#" onFollow={e=>{e.preventDefault();inspect(role)}}>{identifier(role)}</Link>},{id:'arn',header:'ARN',cell:role=>identifier(arn(role))}]} header={<Header variant="awsui-h1-sticky" counter={'('+roles.length+')'} actions={<SpaceBetween direction="horizontal" size="xs"><Button iconName="refresh" ariaLabel="Refresh roles" onClick={load}/><Button variant="primary" onClick={start}>Create role</Button></SpaceBetween>}>Roles</Header>} filter={<TextFilter filteringText={filter} filteringPlaceholder="Find roles" filteringAriaLabel="Find roles" onChange={({detail})=>setFilter(detail.filteringText)}/>} empty={<Box textAlign="center" margin={{vertical:'l'}}><SpaceBetween size="m"><Box variant="h2">{filter?'No matching roles':'No roles yet'}</Box><Button onClick={start}>Create role</Button></SpaceBetween></Box>}/>}
 </SpaceBetween>
}
