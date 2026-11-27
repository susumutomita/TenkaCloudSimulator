import React,{useEffect,useMemo,useRef,useState} from 'react';
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
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import {useLocale} from './i18n.jsx';
import {createVpcNetworkACL,deleteVpcNetworkACL,loadNetworkACLInventory,loadNetworkACLVpcs,
  networkACLFailure,networkACLInventoryOperations,validNetworkACLID,validNetworkACLName,validNetworkACLVpcID} from './ec2-network-acls.mjs';

const noSaving=()=>{};
const pageSize=20;
const idCell=value=><span className="resource-identifier">{value||'—'}</span>;

// Details are a projection of one validated ACL record. No VPC subnet list or
// AWS default behavior is used to fill in associations absent from readback.
export function NetworkACLDetails({acl,onOpenSubnet}) {
  const {t}=useLocale();
  const ruleTable=(egress,label)=>{
    const rules=acl.entries.filter(rule=>rule.egress===egress).sort((a,b)=>a.number-b.number);
    return <Table variant="embedded" items={rules} trackBy="number" ariaLabels={{tableLabel:label}}
      columnDefinitions={[
        {id:'number',header:t('Rule number','ルール番号'),cell:row=>row.number},
        {id:'protocol',header:t('Protocol','プロトコル'),cell:row=>row.protocol==='-1'?t('All (-1)','すべて (-1)'):row.protocol},
        {id:'cidr',header:egress?t('Destination','送信先'):t('Source','送信元'),cell:row=>idCell(row.cidr)},
        {id:'action',header:t('Allow / Deny','許可 / 拒否'),cell:row=>row.action==='deny'?t('Deny','拒否'):t('Allow','許可')},
      ]} empty={t('No rules returned for this direction.','この方向のルールは返されませんでした。')}/>;
  };
  const inbound=t('Inbound rules','インバウンドルール'),outbound=t('Outbound rules','アウトバウンドルール');
  const linked=t('Subnet associations','サブネットの関連付け');
  return <Container header={<Header variant="h2">{idCell(acl.id)}</Header>}>
    <SpaceBetween size="m">
      <KeyValuePairs columns={3} items={[
        {label:t('VPC ID','VPC ID'),value:idCell(acl.vpc)},
        {label:t('Default network ACL','デフォルトのネットワーク ACL'),value:acl.isDefault?t('Yes','はい'):t('No','いいえ')},
        {label:t('Subnet associations','サブネットの関連付け'),value:String(acl.associations.length)},
      ]}/>
      <Tabs tabs={[
        {id:'inbound',label:inbound,content:ruleTable(false,inbound)},
        {id:'outbound',label:outbound,content:ruleTable(true,outbound)},
        {id:'subnets',label:linked,content:<Table variant="embedded" trackBy="id" items={acl.associations}
          ariaLabels={{tableLabel:linked}}
          columnDefinitions={[
            {id:'subnet',header:t('Subnet ID','サブネット ID'),cell:row=>onOpenSubnet
              ? <Button variant="inline-link" onClick={()=>onOpenSubnet({vpcId:acl.vpc,subnetId:row.subnetId})}>{row.subnetId}</Button>
              : idCell(row.subnetId)},
            {id:'id',header:t('Association ID','関連付け ID'),cell:row=>idCell(row.id)},
          ]} empty={t('The API confirms no associated subnets.','API が関連付けられたサブネットなしと確認しました。')}/>},
      ]}/>
      <Box color="text-body-secondary">{t('Rules shown here are saved configuration. They do not filter traffic.','ここに表示されるルールは保存済みの設定です。パケットはフィルタリングされません。')}</Box>
    </SpaceBetween>
  </Container>;
}

// Standalone VPC console page. The parent owns navigation and the session;
// request is the same authenticated same-origin request as the other panels.
export default function NetworkACLInventory({request,region,capabilities,authenticationRequired,initialVpcId='',initialAclId='',
  onOpenSubnet,onSelectAcl,onSaving=noSaving,writeBlocked=false}) {
  const {t}=useLocale();
  const {read,create,delete:canDelete,vpcs:canListVPCs}=networkACLInventoryOperations(capabilities,authenticationRequired);
  const [selection,setSelection]=useState(()=>({region,vpc:validNetworkACLVpcID(initialVpcId)?initialVpcId:'',
    acl:validNetworkACLID(initialAclId)?initialAclId:''}));
  const filter=selection.region===region?selection.vpc:'';
  const scope=useMemo(()=>({request,region,filter,read}),[request,region,filter,read]);
  const vpcScope=useMemo(()=>({request,region,canListVPCs}),[request,region,canListVPCs]);
  const active=useRef(null);
  const [view,setView]=useState(null),[vpcView,setVpcView]=useState(null),[tick,setTick]=useState(0);
  const [modal,setModal]=useState(null),[notice,setNotice]=useState(null),[page,setPage]=useState(1);
  const current=view?.scope===scope?view:null,dialog=modal?.scope===scope?modal:null;
  const nativeVPCs=vpcView?.scope===vpcScope&&vpcView.status==='ready'?vpcView.rows:[];
  const canRefresh=!writeBlocked&&current?.status!=='writing';
  const ready=current?.status==='ready',writing=current?.status==='writing';
  const rows=current?.rows??[];
  const selected=rows.find(item=>item.id===selection.acl&&selection.region===region);
  const eligible=nativeVPCs.filter(vpc=>!filter||vpc.id===filter);
  const canStartCreate=create&&ready&&!writeBlocked&&eligible.length>0;
  const canStartDelete=canDelete&&ready&&!writeBlocked&&Boolean(selected&&!selected.isDefault&&selected.associations.length===0);

  function retire() {
    const op=active.current;
    if (!op) return;
    active.current=null;
    op.controller.abort();
    op.saving?.(false);
  }
  function begin(saving) {
    if (active.current?.saving) return null;
    retire();
    const op={controller:new AbortController(),saving:saving?onSaving:null};
    active.current=op;
    op.saving?.(true);
    return op;
  }
  function finish(op) {
    if (op!==active.current||op.controller.signal.aborted) return false;
    active.current=null;
    op.saving?.(false);
    return true;
  }
  useEffect(()=>{
    if (!read) return;
    const op=begin(false);
    if (!op) return;
    setView({scope,status:'loading'});setNotice(null);setModal(null);setPage(1);
    loadNetworkACLInventory(request,region,filter,{signal:op.controller.signal}).then(
      next=>{if (finish(op)) setView({scope,status:'ready',rows:next});},
      error=>{if (finish(op)) setView({scope,status:networkACLFailure(error)});});
    return retire;
  },[scope,tick]);
  useEffect(()=>{
    if (!canListVPCs) return;
    const abort=new AbortController();
    setVpcView({scope:vpcScope,status:'loading'});
    loadNetworkACLVpcs(request,region,{signal:abort.signal}).then(
      next=>{if (!abort.signal.aborted) setVpcView({scope:vpcScope,status:'ready',rows:next});},
      error=>{if (!abort.signal.aborted) setVpcView({scope:vpcScope,status:networkACLFailure(error)});});
    return ()=>abort.abort();
  },[vpcScope,tick]);

  const target=eligible.find(vpc=>vpc.id===dialog?.vpc);
  const valid=dialog?.kind==='create' ? canStartCreate&&Boolean(target)&&validNetworkACLName(dialog.name)
    : dialog?.kind==='delete'&&canStartDelete&&dialog.acl===selected&&dialog.confirm===dialog.acl.id;
  async function submit(event) {
    event.preventDefault();
    if (!valid) return;
    const op=begin(true);
    if (!op) return;
    setNotice(null);setView({...current,status:'writing'});
    try {
      const options={signal:op.controller.signal};
      const result=dialog.kind==='create'
        ? await createVpcNetworkACL(request,region,target,dialog.name,globalThis.crypto.randomUUID(),options)
        : await deleteVpcNetworkACL(request,region,{id:dialog.acl.vpc,managedBy:'ec2'},dialog.acl,options);
      // A mutation's scoped VPC readback alone is not a complete regional list.
      const next=await loadNetworkACLInventory(request,region,filter,options);
      const id=(result.created??result.removed).id;
      if (next.some(acl=>acl.id===id)!==(dialog.kind==='create')) throw Error('Unconfirmed inventory after write.');
      if (!finish(op)) return;
      setView({scope,status:'ready',rows:next});setModal(null);
      const targetId=result.created?.id??'';
      setSelection({region,vpc:filter,acl:targetId});
      if(onSelectAcl) onSelectAcl(targetId, result.created ? target.id : filter, true);
      setNotice({scope,id,kind:dialog.kind});
    } catch (error) {
      if (!finish(op)) return;
      setView({scope,status:networkACLFailure(error),refreshBeforeWrite:true});setModal(null);
    }
  }

  const title=t('Network ACLs','ネットワーク ACL');
  if (!read) return <ContentLayout header={<Header variant="h1">{title}</Header>}>
    <Alert type="info">{t('Network ACL inventory is unavailable in this session.','このセッションではネットワーク ACL の一覧を利用できません。')}</Alert>
  </ContentLayout>;
  const allVPCs={label:t('All VPCs','すべての VPC'),value:''};
  const names=new Map(nativeVPCs.map(vpc=>[vpc.id,vpc.name]));
  const vpcIds=[...new Set([...names.keys(),...rows.map(acl=>acl.vpc),...(filter?[filter]:[])])].sort();
  const vpcOption=id=>({value:id,label:names.get(id)?`${names.get(id)} (${id})`:id});
  const filters=[allVPCs,...vpcIds.map(vpcOption)];
  const targets=eligible.map(vpc=>vpcOption(vpc.id));
  const errors={
    denied:t('This identity cannot read or change these network ACLs. Check its EC2 permissions.','現在の ID ではネットワーク ACL を参照・変更できません。EC2 の権限を確認してください。'),
    'not-found':t('The network ACL or VPC is no longer available.','ネットワーク ACL または VPC が見つかりません。'),
    limit:t('The local network ACL limit was reached.','ローカルのネットワーク ACL の上限に達しました。'),
    unconfirmed:t('Could not confirm network ACL state. No result was reported.','ネットワーク ACL の状態を確認できませんでした。結果は表示していません。'),
  };
  const creating=t('Create network ACL','ネットワーク ACL を作成'),deleting=t('Delete network ACL','ネットワーク ACL を削除');
  const pages=Math.max(1,Math.ceil(rows.length/pageSize)),shown=Math.min(page,pages);
  const loading=!current||current.status==='loading';
  const pick=(id,vpc=filter)=>{
    if (writing||dialog||writeBlocked) return;
    if(onSelectAcl) onSelectAcl(id,vpc);
    else setSelection({region,vpc,acl:id});
  };
  const edit=(key,value)=>setModal(old=>old?.scope===scope?{...old,[key]:value}:old);
  return <ContentLayout header={<Header variant="h1" actions={<SpaceBetween direction="horizontal" size="xs">
    <Button iconName="refresh" disabled={!canRefresh} loading={loading} onClick={()=>setTick(n=>n+1)}>{t('Refresh','更新')}</Button>
    {create&&<Button variant="primary" disabled={!canStartCreate}
      onClick={()=>setModal({scope,kind:'create',vpc:filter||eligible[0]?.id||'',name:''})}>{creating}</Button>}
    {canDelete&&<Button disabled={!canStartDelete}
      onClick={()=>setModal({scope,kind:'delete',acl:selected,confirm:''})}>{deleting}</Button>}
  </SpaceBetween>}>{title}</Header>}>
    <SpaceBetween size="l">
      <Alert type="info">{t('Only saved, detached custom ACLs are available. Default ACLs, rule changes, subnet attachment, and packet filtering are not supported.','保存済みの、関連付けのないカスタム ACL のみ利用できます。デフォルト ACL、ルールの変更、サブネットへの関連付け、パケットフィルタリングには対応していません。')}</Alert>
      {errors[current?.status]&&<Alert type="error">{errors[current.status]} {current.refreshBeforeWrite&&
        t('Refresh before trying another write.','次の変更の前に一覧を更新してください。')}</Alert>}
      {notice?.scope===scope&&<Alert type="success">{notice.kind==='create'?t('Saved network ACL: ','ネットワーク ACL を保存しました: ')
        :t('Deleted network ACL: ','ネットワーク ACL を削除しました: ')}{idCell(notice.id)}</Alert>}
      <FormField label={t('Filter by VPC','VPC で絞り込み')}><Select options={filters} filteringType="auto"
        disabled={writing||Boolean(dialog)||writeBlocked} selectedOption={filters.find(item=>item.value===filter)}
        onChange={({detail})=>pick('',detail.selectedOption.value??'')}/></FormField>
      {canListVPCs&&vpcView?.scope===vpcScope&&errors[vpcView.status]&&<Box color="text-body-secondary">
        {t('Could not verify VPCs for creating ACLs. ACLs already returned can still be inspected.','作成先の VPC を確認できませんでした。取得済みの ACL は引き続き参照できます。')}
      </Box>}
      <Table trackBy="id" items={rows.slice((shown-1)*pageSize,shown*pageSize)} loading={loading||writing}
        loadingText={t('Reading network ACLs','ネットワーク ACL を読み込み中')}
        ariaLabels={{tableLabel:title}}
        columnDefinitions={[
          {id:'name',header:t('Name','名前'),cell:acl=>acl.name||'—'},
          {id:'id',header:t('Network ACL ID','ネットワーク ACL ID'),cell:acl=><Button variant="inline-link" disabled={writing}
            onClick={()=>pick(acl.id,acl.vpc)}>{acl.id}</Button>},
          {id:'vpc',header:t('VPC ID','VPC ID'),cell:acl=>idCell(acl.vpc)},
          {id:'subnets',header:t('Subnet associations','サブネットの関連付け'),cell:acl=>String(acl.associations.length)},
        ]} pagination={pages>1&&<Pagination currentPageIndex={shown} pagesCount={pages} onChange={({detail})=>setPage(detail.currentPageIndex)}
          ariaLabels={{previousPageLabel:t('Previous page','前のページ'),nextPageLabel:t('Next page','次のページ'),pageLabel:n=>String(n)}}/>}
        empty={ready?t('No detached network ACLs match this VPC filter.','VPC の絞り込みに一致する関連付けのないネットワーク ACL はありません。')
          :t('Network ACL data is not available.','ネットワーク ACL のデータは利用できません。')}/>
      {initialAclId&&ready&&!selected&&<Alert type="info">{t('The selected network ACL was not found in this inventory.',
        '選択したネットワーク ACL は、この一覧にはありません。')}</Alert>}
      {selected&&<NetworkACLDetails key={selected.id} acl={selected} onOpenSubnet={!writing?onOpenSubnet:undefined}/>}
    </SpaceBetween>
    {dialog&&<Modal visible header={dialog.kind==='create'?creating:deleting} closeAriaLabel={t('Close network ACL dialog','ネットワーク ACL のダイアログを閉じる')}
      onDismiss={()=>{if (!writing) setModal(null);}}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={writing} onClick={()=>setModal(null)}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" formAction="submit" disabled={!valid} loading={writing}>{dialog.kind==='create'?creating:deleting}</Button>
      </SpaceBetween>}><SpaceBetween size="m">
        {dialog.kind==='create'?<>
          <FormField label={t('VPC','VPC')}><Select options={targets} selectedOption={targets.find(item=>item.value===dialog.vpc)??null}
            filteringType="auto" disabled={writing} onChange={({detail})=>edit('vpc',detail.selectedOption.value??'')}/></FormField>
          <FormField label={t('Name (optional)','名前（任意）')}
            constraintText={t('Up to 256 UTF-8 bytes.','UTF-8 で 256 バイトまで。')}
            errorText={validNetworkACLName(dialog.name)?undefined:t('Enter valid text of at most 256 UTF-8 bytes.','UTF-8 で 256 バイト以内の有効な文字を入力してください。')}>
            <Input value={dialog.name} disabled={writing} onChange={({detail})=>edit('name',detail.value)}/>
          </FormField>
        </>:<>
          <Box>{t('Delete this detached configuration: ','関連付けのない設定を削除します: ')}{idCell(dialog.acl.id)}</Box>
          <FormField label={t('Enter the network ACL ID to confirm deletion','削除を確認するにはネットワーク ACL ID を入力')}>
            <Input value={dialog.confirm} disabled={writing} onChange={({detail})=>edit('confirm',detail.value)}/>
          </FormField>
        </>}
      </SpaceBetween></Form></form>
    </Modal>}
  </ContentLayout>;
}
