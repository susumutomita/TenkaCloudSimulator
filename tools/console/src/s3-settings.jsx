import React, {useEffect, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Box from '@cloudscape-design/components/box';
import Header from '@cloudscape-design/components/header';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import RadioGroup from '@cloudscape-design/components/radio-group';
import SpaceBetween from '@cloudscape-design/components/space-between';

const labels={BlockPublicAcls:'Block public access to buckets and objects granted through new access control lists (ACLs)',IgnorePublicAcls:'Block public access to buckets and objects granted through any access control lists (ACLs)',BlockPublicPolicy:'Block public access to buckets and objects granted through new public bucket or access point policies',RestrictPublicBuckets:'Block public and cross-account access to buckets and objects through any public bucket or access point policies'};
const text=(xml,key)=>xml.getElementsByTagName(key)[0]?.textContent||'';
const parse=body=>new DOMParser().parseFromString(body,'application/xml');
export default function S3Settings({bucket,tab,request,onSaving}){
 const kind=tab==='properties'?'versioning':'publicAccessBlock';
 const [state,setState]=useState(null),[draft,setDraft]=useState(null),[editing,setEditing]=useState(false),[loading,setLoading]=useState(true),[saving,setSaving]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState(''),[confirming,setConfirming]=useState(false),[confirmation,setConfirmation]=useState('');
 useEffect(()=>{const controller=new AbortController();setLoading(true);setError('');setEditing(false);setMessage('');
  request('/'+bucket+'?'+kind,{signal:controller.signal}).then(r=>r.text()).then(body=>{if(controller.signal.aborted)return;const xml=parse(body);setState(kind==='versioning'?(text(xml,'Status')||'Disabled'):Object.fromEntries(Object.keys(labels).map(k=>[k,text(xml,k)==='true'])))}).catch(e=>{if(!controller.signal.aborted){if(kind==='publicAccessBlock'&&e.message.startsWith('NoSuchPublicAccessBlockConfiguration:')){setState(Object.fromEntries(Object.keys(labels).map(k=>[k,false])))}else{setState(null);setError(e.message)}}}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});return()=>controller.abort();
 },[bucket,kind]);
 const edit=()=>{setDraft(kind==='versioning'?(state==='Disabled'?'Enabled':state):{...state});setEditing(true);setError('');setMessage('')};
 async function save(){if(kind==='publicAccessBlock'&&!confirming){setConfirmation('');setConfirming(true);return}if(kind==='publicAccessBlock'&&confirmation!=='confirm'){setError('Enter confirm to save these settings.');return}setSaving(true);onSaving(true);setError('');try{
  const body=kind==='versioning'?`<VersioningConfiguration><Status>${draft}</Status></VersioningConfiguration>`:`<PublicAccessBlockConfiguration>${Object.entries(draft).map(([k,v])=>`<${k}>${v}</${k}>`).join('')}</PublicAccessBlockConfiguration>`;
  await request('/'+bucket+'?'+kind,{method:'PUT',body,headers:{'Content-Type':'application/xml'}});setState(draft);setEditing(false);setConfirming(false);setMessage('Changes saved.');
 }catch(e){setError(e.message)}finally{setSaving(false);onSaving(false)}}
 const actions=editing?<SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={()=>{setEditing(false);setError('')}}>Cancel</Button><Button variant="primary" loading={saving} onClick={save}>Save changes</Button></SpaceBetween>:<Button disabled={loading||state===null} onClick={edit}>Edit</Button>;
 return <SpaceBetween size="l"><Modal visible={confirming} onDismiss={()=>!saving&&setConfirming(false)} header="Edit Block public access (bucket settings)" closeAriaLabel="Close confirmation" footer={<Box float="right"><SpaceBetween direction="horizontal" size="xs"><Button disabled={saving} onClick={()=>setConfirming(false)}>Cancel</Button><Button variant="primary" loading={saving} onClick={save}>Confirm</Button></SpaceBetween></Box>}><Form errorText={error}><FormField label="Type confirm to save changes"><Input value={confirmation} onChange={({detail})=>setConfirmation(detail.value)}/></FormField></Form></Modal>{error&&<Alert type="error">{error}</Alert>}{message&&<Alert type="success">{message}</Alert>}<Container header={<Header variant="h2" actions={actions}>{kind==='versioning'?'Bucket Versioning':'Block public access (bucket settings)'}</Header>}>
 {loading?'Loading bucket settings':state===null?'Bucket settings could not be read.':kind==='versioning'?editing?<Form><FormField label="Bucket Versioning" description="Preserve, retrieve, and restore every version of an object. Suspending versioning retains existing versions."><RadioGroup value={draft} onChange={({detail})=>setDraft(detail.value)} items={[{value:'Enabled',label:'Enable'},{value:'Suspended',label:'Suspend',disabled:state==='Disabled'}]}/></FormField></Form>:<KeyValuePairs columns={1} items={[{label:'Bucket Versioning',value:state}]}/>:editing?<SpaceBetween size="m"><Checkbox checked={Object.values(draft).every(Boolean)} onChange={({detail})=>setDraft(Object.fromEntries(Object.keys(labels).map(k=>[k,detail.checked])))}>Block all public access</Checkbox>{Object.entries(labels).map(([k,label])=><Checkbox key={k} checked={draft[k]} onChange={({detail})=>setDraft({...draft,[k]:detail.checked})}>{label}</Checkbox>)}</SpaceBetween>:<KeyValuePairs columns={1} items={[{label:'Block all public access',value:Object.values(state).every(Boolean)?'On':'Off'},...Object.entries(labels).map(([k,label])=>({label,value:state[k]?'On':'Off'}))]}/>}</Container>
 <Alert type="info">{kind==='versioning'?'Version changes take effect immediately in this simulator. MFA Delete, lifecycle expiration, replication and Object Lock are unavailable.':'These are bucket-level settings. Public ACLs, resource policies, access points and cross-account grants are unavailable in this simulator, so turning a setting off does not grant public access.'}</Alert></SpaceBetween>;
}
