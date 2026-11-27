import React,{useEffect,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ButtonDropdown from '@cloudscape-design/components/button-dropdown';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {loadNetworkACLInventory,loadSubnetNetworkACL,replaceSubnetNetworkACL} from './ec2-network-acls.mjs';

const shownID=id=><span className="resource-identifier">{id}</span>;
const aclOption=(acl,t)=>({value:acl.id,label:acl.name?acl.name+' · '+acl.id:acl.id,
  ...(acl.isDefault?{description:t('Default network ACL','デフォルトのネットワーク ACL')}:{})});

export default function SubnetNetworkACLPanel({request,region,vpcId,subnetId,owner,interactive,acquireWrite,writeBlocked}) {
  const {t}=useLocale();
  const [result,setResult]=useState({status:'loading'}),[revision,refresh]=useState(0);
  const [dialog,setDialog]=useState(null),[notice,setNotice]=useState(false),[saving,setSaving]=useState(false);
  const secureScope=owner==='ec2'&&vpcId&&subnetId;
  const active=useRef(null),busy=useRef(null),draft=useRef(null);
  useEffect(()=>{
    draft.current=null;setDialog(null);setNotice(false);
    if(!secureScope)return;
    const controller=new AbortController();active.current=controller;
    setResult({status:'loading'});
    const vpc={id:vpcId,managedBy:'ec2'},subnet={id:subnetId,vpc:vpcId,owner};
    loadSubnetNetworkACL(request,region,vpc,subnet,{signal:controller.signal})
      .then(value=>{if(!controller.signal.aborted)setResult({status:'ready',value});})
      .catch(()=>{if(!controller.signal.aborted)setResult({status:'error'});});
    return ()=>{controller.abort();if(active.current===controller)active.current=null;};
  },[request,region,vpcId,subnetId,owner,secureScope,revision]);

  const current=result.status==='ready'?result.value:null;
  const canOpen=interactive&&current&&!writeBlocked&&!saving;
  const editLabel=t('Edit network ACL association','ネットワーク ACL の関連付けを編集');
  const unavailable=t('An ACL association could not be confirmed. Refresh before making a change.',
    'ACL の関連付けを確認できませんでした。変更する前に再読み込みしてください。');

  function closeDialog() {
    draft.current=null;setDialog(null);
  }

  async function openDialog() {
    if(!canOpen||draft.current||!active.current||busy.current)return;
    const controller=active.current,signal=controller.signal,from=current;
    const selection={};draft.current=selection;
    setNotice(false);setDialog({status:'loading'});
    try{
      const rows=await loadNetworkACLInventory(request,region,vpcId,{signal});
      const match=rows.find(acl=>acl.id===from.acl.id&&acl.associations.some(a=>a.id===from.association.id&&a.subnetId===subnetId));
      const oneDefault=rows.filter(acl=>acl.isDefault).length===1;
      if(signal.aborted||draft.current!==selection)return;
      if(!match||!oneDefault)setDialog({status:'error'});
      else setDialog({status:'ready',rows:rows.filter(acl=>acl.id!==from.acl.id),selected:'',from,nonce:selection});
    }catch{if(!signal.aborted&&draft.current===selection)setDialog({status:'error'});}
  }

  async function save(event) {
    event.preventDefault();
    if(!active.current||busy.current||writeBlocked||!dialog||dialog.status!=='ready'||dialog.from!==current||!interactive)return;
    const target=dialog.rows.find(acl=>acl.id===dialog.selected);
    if(!target)return;
    const release=acquireWrite?.();
    if(!release)return;
    const controller=active.current;
    const vpc={id:vpcId,managedBy:'ec2'},subnet={id:subnetId,vpc:vpcId,owner};
    busy.current=controller;setSaving(true);
    try{
      const value=await replaceSubnetNetworkACL(request,region,vpc,subnet,current,target,{signal:controller.signal});
      if(!controller.signal.aborted){setResult({status:'ready',value});setNotice(true);closeDialog();}
    }catch{
      // A transport/readback failure does not mean the server rolled back.
      if(!controller.signal.aborted){closeDialog();setResult({status:'error'});}
    }finally{
      release();
      if(busy.current===controller){busy.current=null;setSaving(false);}
    }
  }

  const selected=dialog?.rows?.find(acl=>acl.id===dialog.selected);
  const header=<Header variant="h2" actions={canOpen&&<ButtonDropdown
    items={[{id:'edit',text:editLabel}]} onItemClick={({detail})=>{if(detail.id==='edit')void openDialog();}}
    ariaLabel={t('Network ACL actions','ネットワーク ACL の操作')}>{t('Actions','アクション')}</ButtonDropdown>}>
    {t('Network ACL','ネットワーク ACL')}</Header>;
  return <Container header={header}>
    <SpaceBetween size="s">
      {!secureScope?<Box>{t('This subnet is not managed by native EC2. No editable ACL association is available.',
        'このサブネットはネイティブ EC2 管理ではないため、ACL の関連付けを編集できません。')}</Box>
        :result.status==='loading'?<StatusIndicator type="loading">{t('Reading network ACL association','ネットワーク ACL の関連付けを読み込み中')}</StatusIndicator>
        :result.status==='error'?<Alert type="warning" action={<Button disabled={saving||writeBlocked} onClick={()=>refresh(n=>n+1)}>{t('Refresh','更新')}</Button>}>{unavailable}</Alert>
        :!current?<Box>{t('No saved native ACL association was returned for this subnet.',
          'このサブネットについて、保存済みのネイティブ ACL 関連付けは返されませんでした。')}</Box>
        :<KeyValuePairs columns={2} items={[
          {label:t('Network ACL ID','ネットワーク ACL ID'),value:shownID(current.acl.id)},
          {label:t('Association ID','関連付け ID'),value:shownID(current.association.id)},
        ]}/>}
      {notice&&<Alert type="success">{t('The new association was saved and verified.',
        '新しい関連付けを保存し、API で確認しました。')}</Alert>}
      <Box color="text-body-secondary">{t('This changes only the saved subnet configuration. Packet filtering is not emulated.',
        '変更するのはサブネットの保存済み設定だけです。パケットフィルタリングはシミュレートされません。')}</Box>
      {dialog&&<Modal visible header={editLabel} closeAriaLabel={t('Close network ACL association dialog','ネットワーク ACL 関連付けのダイアログを閉じる')} onDismiss={()=>{if(!saving&&!busy.current)closeDialog();}} footer={<Box float="right">
        <SpaceBetween size="xs" direction="horizontal">
          <Button formAction="none" disabled={saving} onClick={closeDialog}>{t('Cancel','キャンセル')}</Button>
          <Button variant="primary" formAction="submit" form="subnet-acl-change"
            disabled={!selected||writeBlocked||saving} loading={saving}>{t('Save','保存')}</Button>
        </SpaceBetween>
      </Box>}>
        <form id="subnet-acl-change" onSubmit={save}>
          <SpaceBetween size="m">
            {dialog.status==='loading'?<StatusIndicator type="loading">{t('Checking available network ACLs','選択できるネットワーク ACL を確認中')}</StatusIndicator>
              :dialog.status==='error'?<Alert type="warning">{unavailable}</Alert>:<FormField label={t('Network ACL ID','ネットワーク ACL ID')}>
              <Select options={dialog.rows.map(acl=>aclOption(acl,t))} selectedOption={selected?aclOption(selected,t):null}
                disabled={saving||writeBlocked} filteringType="auto" placeholder={t('Choose a network ACL','ネットワーク ACL を選択')}
                empty={t('There is no other network ACL in this VPC.','この VPC に他のネットワーク ACL はありません。')}
                onChange={({detail})=>setDialog(value=>value?.status==='ready'&&value.nonce===dialog.nonce&&draft.current===dialog.nonce
                  ?{...value,selected:detail.selectedOption.value}:value)}/>
            </FormField>}
            {selected&&[false,true].map(egress=>{
              const label=egress?t('Outbound rules','アウトバウンドルール'):t('Inbound rules','インバウンドルール');
              return <Table key={label} variant="embedded" trackBy="number" header={<Header variant="h3">{label}</Header>}
                items={selected.entries.filter(rule=>rule.egress===egress)} ariaLabels={{tableLabel:label}}
                columnDefinitions={[
                  {id:'number',header:t('Rule number','ルール番号'),cell:rule=>rule.number===32767?'*':rule.number},
                  {id:'protocol',header:t('Protocol','プロトコル'),cell:rule=>rule.protocol==='-1'?t('All','すべて'):rule.protocol==='6'?'TCP':'UDP'},
                  {id:'ports',header:t('Port range','ポート範囲'),cell:rule=>rule.ports?rule.ports.from+'–'+rule.ports.to:'—'},
                  {id:'cidr',header:egress?t('Destination','送信先'):t('Source','送信元'),cell:rule=>rule.cidr},
                  {id:'action',header:t('Allow / Deny','許可 / 拒否'),cell:rule=>rule.action==='allow'?t('Allow','許可'):t('Deny','拒否')},
                ]}/>;
            })}
          </SpaceBetween>
        </form>
      </Modal>}
    </SpaceBetween>
  </Container>;
}
