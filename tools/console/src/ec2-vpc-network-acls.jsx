import React,{useEffect,useMemo,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {createVpcNetworkACL,deleteVpcNetworkACL,loadVpcNetworkACLs,networkACLFailure,
  networkACLTarget,validNetworkACLName} from './ec2-network-acls.mjs';

const PAGE_SIZE=20;
const noSaving=()=>{};
const identifier=value=><span className="resource-identifier">{value}</span>;

export default function VPCNetworkACLs({request,region,vpc,onSaving=noSaving,writeBlocked=false}) {
  const {t}=useLocale();
  const {id,managedBy}=vpc??{};
  const scope=useMemo(()=>({request,region,vpc:{id,managedBy}}),[request,region,id,managedBy]);
  const target=networkACLTarget(scope.vpc);
  const active=useRef(null);
  const [snapshot,setSnapshot]=useState(null),[modal,setModal]=useState(null),[notice,setNotice]=useState(null);
  const [page,setPage]=useState(1);
  const current=snapshot?.scope===scope ? snapshot : null;
  const dialog=modal?.scope===scope ? modal : null;
  const ready=current?.status==='ready',pending=current?.status==='writing';

  function retire() {
    const previous=active.current;
    if (!previous) return;
    active.current=null;
    previous.controller.abort();
    if (previous.release) previous.release(false);
  }
  function begin(writing) {
    if (active.current?.release) return null;
    retire();
    const operation={controller:new AbortController(),release:writing?onSaving:null};
    active.current=operation;
    if (operation.release) operation.release(true);
    return operation;
  }
  function finish(operation) {
    if (active.current!==operation || operation.controller.signal.aborted) return false;
    active.current=null;
    if (operation.release) operation.release(false);
    return true;
  }
  async function refresh() {
    if (target!=='ec2') return;
    const operation=begin(false);
    if (!operation) return;
    setSnapshot({scope,status:'loading'});
    setNotice(null);
    setModal(null);
    setPage(1);
    try {
      const rows=await loadVpcNetworkACLs(request,region,scope.vpc,{signal:operation.controller.signal});
      if (finish(operation)) setSnapshot({scope,status:'ready',rows});
    } catch (error) {
      if (finish(operation)) setSnapshot({scope,status:networkACLFailure(error)});
    }
  }
  useEffect(()=>{void refresh();return retire;},[scope]);
  const valid=dialog?.kind==='create' ? validNetworkACLName(dialog.name) :
    dialog?.kind==='delete' && current?.rows?.includes(dialog.acl) && dialog.confirm===dialog.acl.id;
  async function submit(event) {
    event.preventDefault();
    if (target!=='ec2' || !ready || writeBlocked || !valid) return;
    const operation=begin(true);
    if (!operation) return;
    setNotice(null);
    setSnapshot({...current,status:'writing'});
    try {
      const options={signal:operation.controller.signal};
      const result=dialog.kind==='create'
        ? await createVpcNetworkACL(request,region,scope.vpc,dialog.name,globalThis.crypto.randomUUID(),options)
        : await deleteVpcNetworkACL(request,region,scope.vpc,dialog.acl,options);
      if (!finish(operation)) return;
      setSnapshot({scope,status:'ready',rows:result.rows});
      setModal(null);
      setNotice({scope,kind:dialog.kind,id:(result.created??result.removed).id});
    } catch (error) {
      if (!finish(operation)) return;
      // An accepted write may have lost its reply or readback. Require a new
      // read before offering another write; never silently replay the request.
      setSnapshot({scope,status:networkACLFailure(error),refreshBeforeWrite:true});
      setModal(null);
    }
  }
  if (target!=='ec2') return <SpaceBetween size="xs">
    <Header variant="h3">{t('Network ACLs','ネットワーク ACL')}</Header>
    <Box color="text-body-secondary">{target==='cloudformation'
      ? t('CloudFormation manages this VPC. This panel does not read or change its network ACLs.','この VPC は CloudFormation が管理しています。ここではネットワーク ACL の参照や変更は行いません。')
      : t('Select a verified EC2-managed VPC to manage detached network ACLs.','関連付けのないネットワーク ACL を管理するには、確認済みの EC2 管理 VPC を選択してください。')}</Box>
  </SpaceBetween>;

  const errors={
    denied:t('The selected identity cannot complete this network ACL request. Check its EC2 permissions.','現在の ID ではネットワーク ACL を操作できません。EC2 の権限を確認してください。'),
    'not-found':t('The selected VPC or network ACL is no longer available.','選択した VPC またはネットワーク ACL は利用できなくなりました。'),
    limit:t('The local network ACL resource or request limit was reached.','ローカルのネットワーク ACL のリソース数またはリクエスト数が上限に達しました。'),
    unconfirmed:t('Could not confirm network ACL state. No result was reported.','ネットワーク ACL の状態を確認できませんでした。結果は表示していません。'),
  };
  const rows=current?.rows??[],pages=Math.max(1,Math.ceil(rows.length/PAGE_SIZE)),shown=Math.min(page,pages);
  const statusLoading=!current||current.status==='loading';
  const refreshLabel=t('Refresh network ACLs','ネットワーク ACL を更新');
  const createLabel=t('Create network ACL','ネットワーク ACL を作成');
  const deleteLabel=t('Delete network ACL','ネットワーク ACL を削除');
  function change(key,value) {setModal(old=>old?.scope===scope ? {...old,[key]:value} : old);}
  return <SpaceBetween size="s">
    <Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button iconName="refresh" disabled={pending} loading={statusLoading} onClick={refresh}>{refreshLabel}</Button>
      <Button disabled={!ready||writeBlocked} onClick={()=>setModal({scope,kind:'create',name:''})}>{createLabel}</Button>
    </SpaceBetween>}>{t('Network ACLs','ネットワーク ACL')}</Header>
    <Box color="text-body-secondary">{t('Detached configuration only. These ACLs are not associated with subnets. Rule editing and packet filtering are not available.','関連付けのない設定のみです。これらの ACL はサブネットに関連付けられません。ルールの編集やパケットのフィルタリングはできません。')}</Box>
    {notice?.scope===scope&&<Alert type="success">{notice.kind==='create'
      ? t('Saved network ACL: ','ネットワーク ACL を保存しました: ')
      : t('Deleted network ACL: ','ネットワーク ACL を削除しました: ')}{identifier(notice.id)}</Alert>}
    {errors[current?.status]&&<Alert type="error">{errors[current.status]} {current.refreshBeforeWrite&&t('Refresh network ACLs before trying again.','再試行する前にネットワーク ACL を更新してください。')}</Alert>}
    <Table variant="embedded" trackBy="id" items={rows.slice((shown-1)*PAGE_SIZE,shown*PAGE_SIZE)} loading={statusLoading||pending}
      loadingText={t('Reading network ACLs','ネットワーク ACL を読み込み中')}
      ariaLabels={{tableLabel:t('Saved detached network ACLs','保存済みの関連付けのないネットワーク ACL')}}
      columnDefinitions={[
        {id:'id',header:t('Network ACL ID','ネットワーク ACL ID'),cell:item=>identifier(item.id)},
        {id:'name',header:t('Name','名前'),cell:item=>item.name||'—'},
        {id:'actions',header:t('Actions','操作'),cell:item=><Button disabled={!ready||writeBlocked}
          onClick={()=>setModal({scope,kind:'delete',acl:item,confirm:''})}>{deleteLabel}</Button>},
      ]}
      pagination={pages>1&&<Pagination currentPageIndex={shown} pagesCount={pages} onChange={({detail})=>setPage(detail.currentPageIndex)}
        ariaLabels={{previousPageLabel:t('Previous page','前のページ'),nextPageLabel:t('Next page','次のページ'),pageLabel:n=>String(n)}}/>}
      empty={ready?t('No detached network ACLs are saved for this VPC.','この VPC に関連付けのないネットワーク ACL はありません。')
        :t('Network ACLs are unavailable.','ネットワーク ACL を取得できません。')}/>
    {dialog&&<Modal visible header={dialog.kind==='create'?createLabel:deleteLabel}
      closeAriaLabel={t('Close network ACL dialog','ネットワーク ACL のダイアログを閉じる')}
      onDismiss={()=>{if (!pending) setModal(null);}}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={pending} onClick={()=>setModal(null)}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" formAction="submit" loading={pending} disabled={!valid||writeBlocked}>
          {dialog.kind==='create'?createLabel:deleteLabel}
        </Button>
      </SpaceBetween>}><SpaceBetween size="m">
        {dialog.kind==='create'?<>
          <Box>{t('Selected VPC: ','選択済み VPC: ')}{identifier(id)}</Box>
          <FormField label={t('Name (optional)','名前（任意）')}
            constraintText={t('The Name tag can use up to 256 UTF-8 bytes.','Name タグは UTF-8 で 256 バイトまで入力できます。')}
            errorText={!valid?t('Use valid text of at most 256 UTF-8 bytes.','UTF-8 で 256 バイト以内の有効な文字を入力してください。'):undefined}>
            <Input value={dialog.name} disabled={pending} onChange={({detail})=>change('name',detail.value)}/>
          </FormField>
        </>:<>
          <Box>{t('Delete this detached configuration: ','関連付けのない設定を削除します: ')}{identifier(dialog.acl.id)}</Box>
          <FormField label={t('Enter the network ACL ID to confirm deletion','削除を確認するにはネットワーク ACL ID を入力')}>
            <Input value={dialog.confirm} disabled={pending} onChange={({detail})=>change('confirm',detail.value)}/>
          </FormField>
        </>}
      </SpaceBetween></Form></form>
    </Modal>}
  </SpaceBetween>;
}
