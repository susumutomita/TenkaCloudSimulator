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
import {createVpcNativeRouteTable,deleteVpcNativeRouteTable,loadVpcNativeRouteTables,
  nativeRouteTableFailure,nativeRouteTableTarget,validNativeRouteTableName} from './ec2-native-route-tables.mjs';

const pageSize=20;
const identifier=value=><span className="resource-identifier">{value}</span>;
const newToken=()=>Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)),b=>b.toString(16).padStart(2,'0')).join('');

export default function VPCNativeRouteTables({request,region,vpc,acquireWrite,writeBlocked=false}) {
  const {t}=useLocale();
  const {id,managedBy}=vpc??{};
  const scope=useMemo(()=>({request,region,vpc:{id,managedBy}}),[request,region,id,managedBy]);
  const validScope=nativeRouteTableTarget(scope.vpc);
  const active=useRef(null);
  const [result,setResult]=useState(null),[modal,setModal]=useState(null),[notice,setNotice]=useState(null),[page,setPage]=useState(1);
  const current=result?.scope===scope ? result : null;
  const dialog=modal?.scope===scope ? modal : null;
  const ready=current?.status==='ready',pending=current?.status==='writing';
  const canWrite=typeof acquireWrite==='function'&&!writeBlocked;

  function retire() {
    const previous=active.current;
    if (!previous) return;
    active.current=null;
    previous.controller.abort();
    previous.release?.();
  }
  function begin(writing) {
    if (active.current?.release) return null;
    const release=writing ? acquireWrite?.() : null;
    if (writing && typeof release!=='function') return null;
    retire();
    return active.current={controller:new AbortController(),release};
  }
  function finish(operation) {
    if (active.current!==operation || operation.controller.signal.aborted) return false;
    active.current=null;
    operation.release?.();
    return true;
  }
  async function refresh() {
    if (!validScope) return;
    const operation=begin(false);
    if (!operation) return;
    setResult({scope,status:'loading'});setModal(null);setNotice(null);setPage(1);
    try {
      const rows=await loadVpcNativeRouteTables(scope.request,scope.region,scope.vpc,{signal:operation.controller.signal});
      if (finish(operation)) setResult({scope,status:'ready',rows});
    } catch(error) {
      if (finish(operation)) setResult({scope,status:nativeRouteTableFailure(error)});
    }
  }
  useEffect(()=>{void refresh();return retire;},[scope]);

  const valid=dialog?.kind==='create' ? validNativeRouteTableName(dialog.name) :
    dialog?.kind==='delete' && current?.rows?.includes(dialog.row) && dialog.confirm===dialog.row.id;
  async function submit(event) {
    event.preventDefault();
    if (!validScope || !ready || !canWrite || !valid) return;
    const operation=begin(true);
    if (!operation) return;
    setResult({...current,status:'writing'});setNotice(null);
    try {
      const options={signal:operation.controller.signal};
      const {created,removed,rows}=dialog.kind==='create'
        ? await createVpcNativeRouteTable(scope.request,scope.region,scope.vpc,dialog.name,newToken(),options)
        : await deleteVpcNativeRouteTable(scope.request,scope.region,scope.vpc,dialog.row,options);
      if (!finish(operation)) return;
      setResult({scope,status:'ready',rows});setModal(null);
      setNotice({scope,kind:dialog.kind,id:(created??removed).id});
    } catch(error) {
      if (!finish(operation)) return;
      // The write may have happened without a reliable reply or readback.
      // Hide stale rows and require fresh state rather than replaying it.
      setResult({scope,status:nativeRouteTableFailure(error),refreshBeforeWrite:true});setModal(null);
    }
  }
  if (!validScope) return null;

  const errors={
    denied:t('The selected identity cannot complete this route table request. Check its EC2 permissions.','現在の ID ではルートテーブルを操作できません。EC2 の権限を確認してください。'),
    'not-found':t('The selected VPC or route table is no longer available.','選択した VPC またはルートテーブルは利用できなくなりました。'),
    dependency:t('A saved dependency prevents deletion. The route table was not removed.','保存済みの依存関係があるため、ルートテーブルを削除できませんでした。'),
    limit:t('The local route table resource or request limit was reached.','ローカルのルートテーブルのリソース数またはリクエスト数が上限に達しました。'),
    unconfirmed:t('Could not confirm the saved route table state. No result was reported.','ルートテーブルの保存状態を確認できませんでした。結果は表示していません。'),
  };
  const rows=current?.rows??[],pages=Math.max(1,Math.ceil(rows.length/pageSize)),shown=Math.min(page,pages);
  const loading=!current || current.status==='loading';
  const refreshLabel=t('Refresh route tables','ルートテーブルを更新');
  const createLabel=t('Create route table','ルートテーブルを作成');
  const deleteLabel=t('Delete route table','ルートテーブルを削除');
  function change(key,value){setModal(old=>old?.scope===scope ? {...old,[key]:value} : old);}
  return <SpaceBetween size="s">
    <Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button iconName="refresh" disabled={pending} loading={loading} onClick={refresh}>{refreshLabel}</Button>
      <Button disabled={!ready||!canWrite} onClick={()=>setModal({scope,kind:'create',name:''})}>{createLabel}</Button>
    </SpaceBetween>}>{t('Route tables','ルートテーブル')}</Header>
    <Box color="text-body-secondary">{t('Detached configuration only. These tables have no routes or subnet associations and do not forward traffic.','関連付けのない設定のみです。ルートやサブネットの関連付けは含まれず、通信は転送しません。')}</Box>
    {notice?.scope===scope&&<Alert type="success">{notice.kind==='create'
      ? t('Saved route table: ','ルートテーブルを保存しました: ')
      : t('Deleted route table: ','ルートテーブルを削除しました: ')}{identifier(notice.id)}</Alert>}
    {errors[current?.status]&&<Alert type="error">{errors[current.status]} {current.refreshBeforeWrite&&
      t('Refresh route tables before trying again.','再試行する前にルートテーブルを更新してください。')}</Alert>}
    <Table variant="embedded" trackBy="id" items={rows.slice((shown-1)*pageSize,shown*pageSize)} loading={loading||pending}
      loadingText={t('Reading route tables','ルートテーブルを読み込み中')}
      ariaLabels={{tableLabel:t('Saved detached route tables','保存済みの関連付けのないルートテーブル')}}
      columnDefinitions={[
        {id:'id',header:t('Route table ID','ルートテーブル ID'),cell:item=>identifier(item.id)},
        {id:'name',header:t('Name','名前'),cell:item=>item.name||'—'},
        {id:'actions',header:t('Actions','操作'),cell:item=><Button disabled={!ready||!canWrite}
          ariaLabel={deleteLabel+': '+item.id} onClick={()=>setModal({scope,kind:'delete',row:item,confirm:''})}>{deleteLabel}</Button>},
      ]}
      pagination={pages>1&&<Pagination currentPageIndex={shown} pagesCount={pages} onChange={({detail})=>setPage(detail.currentPageIndex)}
        ariaLabels={{previousPageLabel:t('Previous page','前のページ'),nextPageLabel:t('Next page','次のページ'),pageLabel:n=>String(n)}}/>}
      empty={ready?t('No detached route tables are saved for this VPC.','この VPC に関連付けのないルートテーブルはありません。')
        :t('Route tables are unavailable.','ルートテーブルを取得できません。')}/>
    {dialog&&<Modal visible header={dialog.kind==='create'?createLabel:deleteLabel}
      closeAriaLabel={t('Close route table dialog','ルートテーブルのダイアログを閉じる')}
      onDismiss={()=>{if (!active.current?.release) setModal(null);}}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={pending} onClick={()=>setModal(null)}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" formAction="submit" loading={pending} disabled={!valid||!canWrite||!ready}>
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
          <Box>{t('Delete this detached configuration: ','関連付けのない設定を削除します: ')}{identifier(dialog.row.id)}</Box>
          <FormField label={t('Enter the route table ID to confirm deletion','削除を確認するにはルートテーブル ID を入力')}>
            <Input value={dialog.confirm} disabled={pending} onChange={({detail})=>change('confirm',detail.value)}/>
          </FormField>
        </>}
      </SpaceBetween></Form></form>
    </Modal>}
  </SpaceBetween>;
}
