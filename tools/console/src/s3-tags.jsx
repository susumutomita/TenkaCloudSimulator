import {useLocale} from './i18n.jsx';
import {localizeAwsNotice} from './aws-child-notices.js';
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
 const {t}=useLocale();
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
 const actions=editing?<SpaceBetween direction="horizontal" size="xs"><Button disabled={saving||disabled} onClick={()=>{setEditing(false);setError('')}}>{t("Cancel", "キャンセル")}</Button><Button variant="primary" disabled={disabled} loading={saving} onClick={save}>{t("Save tags", "タグを保存")}</Button></SpaceBetween>:<Button disabled={loading||disabled||!!error} onClick={edit}>{t("Edit tags", "タグを編集")}</Button>;
 return <Container header={<Header variant="h2" counter={'('+tags.length+')'} actions={actions}>{t("Tags", "タグ")}</Header>}><SpaceBetween size="m">{error&&<Alert type="error">{error}</Alert>}{message&&<Alert type="success">{localizeAwsNotice(message,t)}</Alert>}{editing?<Form><TagEditor loading={saving||disabled} ref={editor} tags={draft} tagLimit={50} onChange={({detail})=>{setDraft(detail.tags);setValid(detail.valid)}} i18nStrings={{keyHeader:t("Key", "キー"),valueHeader:t("Value", "値"),keyPlaceholder:t("Enter key", "キーを入力"),valuePlaceholder:t("Enter value", "値を入力"),optional:t("optional", "任意"),addButton:t("Add tag", "タグを追加"),removeButton:t("Remove", "削除"),undoButton:t("Undo", "元に戻す"),undoPrompt:t("This tag will be removed.", "このタグは削除されます。"),emptyTags:t("No tags", "タグがありません"),loading:t("Loading tags", "タグを読み込み中"),emptyKeyError:t("Enter a tag key.", "タグキーを入力してください。"),maxKeyCharLengthError:t("Keys can have up to 128 characters.", "キーは最大 128 文字です。"),maxValueCharLengthError:t("Values can have up to 256 characters.", "値は最大 256 文字です。"),duplicateKeyError:t("Use a unique key.", "一意のキーを使用してください。"),invalidKeyError:t("Use supported tag characters.", "対応するタグ文字を使用してください。"),invalidValueError:t("Use supported tag characters.", "対応するタグ文字を使用してください。"),awsPrefixError:t("The aws: prefix is reserved.", "aws: プレフィックスは予約されています。"),errorIconAriaLabel:t("Error", "エラー"),clearAriaLabel:t("Clear", "クリア"),itemRemovedAriaLive:t("Tag marked for removal.", "タグを削除対象にしました。"),tagLimit:(available,total)=>t(`${available} of ${total} tags available`, `${total} 件中 ${available} 件のタグを追加できます`),tagLimitReached:total=>t(`The ${total}-tag limit is reached.`, `${total} 件のタグ上限に達しました。`),tagLimitExceeded:total=>t(`Use at most ${total} tags.`, `タグは ${total} 件以下にしてください。`),enteredKeyLabel:text=>t(`Use key ${text}`, `キー ${text} を使用`),enteredValueLabel:text=>t(`Use value ${text}`, `値 ${text} を使用`),removeButtonAriaLabel:tag=>t(`Remove tag ${tag.key}`, `タグ ${tag.key} を削除`)}}/></Form>:<Table variant="embedded" loading={loading} loadingText={t("Loading tags", "タグを読み込み中")} trackBy="key" columnDefinitions={[{id:'key',header:t("Key", "キー"),cell:t=>t.key},{id:'value',header:t("Value", "値"),cell:t=>t.value}]} items={tags} empty={<Box textAlign="center">{error?t("Tags unavailable", "タグを取得できません"):t("No tags", "タグがありません")}</Box>} ariaLabels={{tableLabel:t("Bucket tags", "バケットのタグ")}}/>}<Box color="text-body-secondary">{t("User-defined bucket tags only. Tag-based permissions, billing and AWS-generated tags are unavailable.", "ユーザー定義のバケットタグのみ対応しています。タグベースの許可、請求、AWS が生成するタグは未対応です。")}</Box></SpaceBetween></Container>;
}
