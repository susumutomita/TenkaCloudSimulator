import React,{useEffect,useMemo,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {changeVpcTag,loadVpcTagSnapshot,validVpcTagKey,validVpcTagValue,vpcTagFailure} from './ec2-vpc-tags.mjs';

export default function VpcTagsPanel({vpc,region,request,acquireWrite,writeBlocked,onConfirmed}) {
  const {t}=useLocale();
  const {id,managedBy}=vpc;
  const scope=useMemo(()=>({region,request,vpc:{id,managedBy}}),[region,request,id,managedBy]);
  const active=useRef(null);
  const [view,setView]=useState(null),[dialog,setDialog]=useState(null),[notice,setNotice]=useState(null);
  const current=view?.scope===scope?view:null, draft=dialog?.scope===scope?dialog:null;
  const ready=current?.status==='ready', writing=current?.status==='writing';
  const native=managedBy==='ec2';
  const tags=current?.snapshot?.tags??[];

  function retire() {
    const operation=active.current;
    if(!operation)return;
    active.current=null;operation.abort.abort();operation.release?.();
  }
  function finish(operation) {
    if(active.current!==operation||operation.abort.signal.aborted)return false;
    active.current=null;operation.release?.();
    return true;
  }
  async function refresh() {
    if(!native||active.current?.release)return;
    retire();
    const operation={abort:new AbortController()};
    active.current=operation;
    setView({scope,status:'loading'});setDialog(null);setNotice(null);
    try {
      const snapshot=await loadVpcTagSnapshot(scope.request,scope.region,scope.vpc,{signal:operation.abort.signal});
      if(finish(operation))setView({scope,status:'ready',snapshot});
    }catch(error) {
      if(finish(operation))setView({scope,status:vpcTagFailure(error)});
    }
  }
  useEffect(()=>{void refresh();return retire;},[scope]);

  const adding=draft?.action==='add', deleting=draft?.action==='remove';
  const duplicate=adding&&tags.some(row=>row.key===draft.key);
  const valid=draft&&ready&&!writeBlocked&&(deleting||validVpcTagKey(draft.key)&&validVpcTagValue(draft.value)&&!duplicate&&
      (adding||draft.value!==tags.find(row=>row.key===draft.key)?.value));
  function open(action,row={key:'',value:''}) {
    if(!ready||writeBlocked||active.current)return;
    setDialog({scope,action,...row});
  }
  async function submit(event) {
    event.preventDefault();
    if(!valid||active.current)return;
    const release=acquireWrite();
    if(!release)return;
    const operation={abort:new AbortController(),release};
    active.current=operation;
    setView({...current,status:'writing'});setNotice(null);
    try {
      const change=deleting?{kind:'remove',key:draft.key}:{kind:'upsert',key:draft.key,value:draft.value};
      const snapshot=await changeVpcTag(scope.request,scope.region,scope.vpc,current.snapshot,change,{signal:operation.abort.signal});
      if(!finish(operation))return;
      setView({scope,status:'ready',snapshot});setDialog(null);setNotice({scope,key:draft.key});
      onConfirmed(snapshot);
    }catch(error) {
      if(!finish(operation))return;
      setView({scope,status:vpcTagFailure(error),afterWrite:true});setDialog(null);
    }
  }
  const title=t('Tags','タグ');
  if(!native)return <SpaceBetween size="xs"><Header variant="h3">{title}</Header><Box color="text-body-secondary">
    {t('This VPC is not EC2-managed. Change its tags through its owner.','この VPC は EC2 管理ではありません。タグは管理元で変更してください。')}</Box></SpaceBetween>;
  const add=t('Add tag','タグを追加'),edit=t('Edit tag','タグを編集'),remove=t('Delete tag','タグを削除');
  const loading=!current||current.status==='loading';
  const errors={
    denied:t('This identity cannot complete the tag request. Check its EC2 permissions.','現在の ID ではタグのリクエストを完了できません。EC2 の権限を確認してください。'),
    'tag-limit':t('This VPC already has 50 tags. Edit or remove one of them first.','この VPC にはタグが 50 件あります。先に既存のタグを編集または削除してください。'),
    'response-too-large':t('The regional VPC response is too large for the Console. No tags could be confirmed.','リージョンの VPC 応答がコンソールの上限を超えたため、タグを確認できませんでした。'),
    unconfirmed:t('Could not confirm the saved tags. No change is shown as complete.','保存済みのタグを確認できませんでした。変更を完了として表示していません。'),
  };
  const modalTitle=adding?add:deleting?remove:edit;
  return <SpaceBetween size="m">
    <Table trackBy="key" variant="embedded" items={tags} loading={loading||writing}
      ariaLabels={{tableLabel:title}} loadingText={t('Reading tags','タグを読み込み中')}
      header={<Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
        <Button onClick={refresh} disabled={writing||writeBlocked}>{t('Refresh tags','タグを更新')}</Button>
        <Button onClick={()=>open('add')} disabled={!ready||writeBlocked||tags.length>=50}>{add}</Button>
      </SpaceBetween>}>{title}</Header>}
      columnDefinitions={[
        {id:'key',header:t('Key','キー'),cell:row=>row.key},
        {id:'value',header:t('Value','値'),cell:row=>row.value===''?t('(empty)','（空）'):row.value},
        {id:'actions',header:t('Actions','操作'),cell:row=><SpaceBetween direction="horizontal" size="xs">
          <Button ariaLabel={edit+' '+row.key} disabled={!ready||writeBlocked} onClick={()=>open('edit',row)}>{t('Edit','編集')}</Button>
          <Button ariaLabel={remove+' '+row.key} disabled={!ready||writeBlocked} onClick={()=>open('remove',row)}>{t('Delete','削除')}</Button>
        </SpaceBetween>},
      ]} empty={ready?t('No tags are saved for this VPC.','この VPC に保存済みのタグはありません。'):t('Tags are unavailable.','タグを取得できません。')}/>
    {notice?.scope===scope&&<Alert type="success">{t('Confirmed saved tag change: ','タグの変更を確認しました: ')}{notice.key}</Alert>}
    {errors[current?.status]&&<Alert type="error">{errors[current.status]} {current.afterWrite&&t('Refresh tags before trying again.','再試行する前にタグを更新してください。')}</Alert>}
    {draft&&<Modal visible header={modalTitle} closeAriaLabel={t('Close tag dialog','タグのダイアログを閉じる')}
      onDismiss={()=>{if(!writing)setDialog(null);}}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={writing} onClick={()=>setDialog(null)}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" formAction="submit" loading={writing} disabled={!valid}>{deleting?remove:t('Save tag','タグを保存')}</Button>
      </SpaceBetween>}><SpaceBetween size="m">
        <Box>{t('Selected VPC: ','選択済み VPC: ')}{id}</Box>
        {deleting?<Box>{t('Delete this saved tag? ','この保存済みタグを削除しますか？ ')}{draft.key}</Box>:<>
          <FormField label={t('Tag key','タグのキー')} constraintText={t('1–128 UTF-8 bytes; aws: is reserved.','UTF-8 で 1〜128 バイト。aws: は予約済みです。')}
            errorText={duplicate?t('This key already exists. Edit its row instead.','同じキーが存在します。その行を編集してください。'):undefined}>
            <Input readOnly={!adding} disabled={writing} value={draft.key} onChange={({detail})=>setDialog({...draft,key:detail.value})}/>
          </FormField>
          <FormField label={t('Tag value','タグの値')} constraintText={t('Up to 256 UTF-8 bytes. An empty value is allowed.','UTF-8 で 256 バイトまで。空の値も指定できます。')}>
            <Input disabled={writing} value={draft.value} onChange={({detail})=>setDialog({...draft,value:detail.value})}/>
          </FormField>
        </>}
      </SpaceBetween></Form></form>
    </Modal>}
  </SpaceBetween>;
}
