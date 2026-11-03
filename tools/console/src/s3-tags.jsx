import React, {useEffect,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import TagEditor from '@cloudscape-design/components/tag-editor';
const escapeXML=value=>value.replace(/[<>&"']/g,c=>({'<':'&lt;','>':'&gt;','&':'&amp;','"':'&quot;',"'":'&apos;'}[c]));
export default function S3Tags({bucket,request,onSaving,disabled=false}){
 const [tags,setTags]=useState([]),[draft,setDraft]=useState([]),[valid,setValid]=useState(true),[editing,setEditing]=useState(false),[loading,setLoading]=useState(true),[saving,setSaving]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState('');
 const editor=useRef();
 useEffect(()=>{const controller=new AbortController();setLoading(true);setError('');
  request('/'+bucket+'?tagging',{signal:controller.signal}).then(r=>r.text()).then(body=>{if(controller.signal.aborted)return;const xml=new DOMParser().parseFromString(body,'application/xml');setTags([...xml.getElementsByTagName('Tag')].map(n=>({key:n.getElementsByTagName('Key')[0]?.textContent||'',value:n.getElementsByTagName('Value')[0]?.textContent||'',existing:true})))}).catch(e=>{if(!controller.signal.aborted){if(e.message.startsWith('NoSuchTagSet:'))setTags([]);else setError(e.message)}}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});return()=>controller.abort();
 },[bucket]);
 function edit(){setDraft(tags.map(t=>({...t})));setValid(true);setEditing(true);setMessage('');setError('')}
 async function save(){if(disabled)return;if(!valid){editor.current?.focus();return}setSaving(true);onSaving(true);setError('');try{
  const next=draft.filter(t=>!t.markedForRemoval);const body='<Tagging><TagSet>'+next.map(t=>`<Tag><Key>${escapeXML(t.key)}</Key><Value>${escapeXML(t.value)}</Value></Tag>`).join('')+'</TagSet></Tagging>';
  if(!crypto.subtle)throw Error('Use loopback or HTTPS to edit tags.');
  const digest=await crypto.subtle.digest('SHA-256',new TextEncoder().encode(body));const checksum=btoa(String.fromCharCode(...new Uint8Array(digest)));
  await request('/'+bucket+'?tagging',{method:'PUT',headers:{'Content-Type':'application/xml','x-amz-checksum-sha256':checksum},body});setTags(next.map(t=>({key:t.key,value:t.value,existing:true})));setEditing(false);setMessage('Tags saved.');
 }catch(e){setError(e.message)}finally{setSaving(false);onSaving(false)}}
 const actions=editing?<SpaceBetween direction="horizontal" size="xs"><Button disabled={saving||disabled} onClick={()=>{setEditing(false);setError('')}}>Cancel</Button><Button variant="primary" disabled={disabled} loading={saving} onClick={save}>Save tags</Button></SpaceBetween>:<Button disabled={loading||disabled||!!error} onClick={edit}>Edit tags</Button>;
 return <Container header={<Header variant="h2" counter={'('+tags.length+')'} actions={actions}>Tags</Header>}><SpaceBetween size="m">{error&&<Alert type="error">{error}</Alert>}{message&&<Alert type="success">{message}</Alert>}{editing?<Form><TagEditor loading={saving||disabled} ref={editor} tags={draft} tagLimit={50} onChange={({detail})=>{setDraft(detail.tags);setValid(detail.valid)}} i18nStrings={{keyHeader:'Key',valueHeader:'Value',keyPlaceholder:'Enter key',valuePlaceholder:'Enter value',optional:'optional',addButton:'Add tag',removeButton:'Remove',undoButton:'Undo',undoPrompt:'This tag will be removed.',emptyTags:'No tags',loading:'Loading tags',emptyKeyError:'Enter a tag key.',maxKeyCharLengthError:'Keys can have up to 128 characters.',maxValueCharLengthError:'Values can have up to 256 characters.',duplicateKeyError:'Use a unique key.',invalidKeyError:'Use supported tag characters.',invalidValueError:'Use supported tag characters.',awsPrefixError:'The aws: prefix is reserved.',errorIconAriaLabel:'Error',clearAriaLabel:'Clear',itemRemovedAriaLive:'Tag marked for removal.',tagLimit:(available,total)=>`${available} of ${total} tags available`,tagLimitReached:total=>`The ${total}-tag limit is reached.`,tagLimitExceeded:total=>`Use at most ${total} tags.`,enteredKeyLabel:text=>`Use key ${text}`,enteredValueLabel:text=>`Use value ${text}`,removeButtonAriaLabel:tag=>`Remove tag ${tag.key}`}}/></Form>:<Table variant="embedded" loading={loading} loadingText="Loading tags" trackBy="key" columnDefinitions={[{id:'key',header:'Key',cell:t=>t.key},{id:'value',header:'Value',cell:t=>t.value}]} items={tags} empty={<Box textAlign="center">{error?'Tags unavailable':'No tags'}</Box>} ariaLabels={{tableLabel:'Bucket tags'}}/>}<Box color="text-body-secondary">User-defined bucket tags only. Tag-based permissions, billing and AWS-generated tags are unavailable.</Box></SpaceBetween></Container>;
}
