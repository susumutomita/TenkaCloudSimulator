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
import {createInternetGateway,deleteInternetGateway,describeInternetGateways,internetGatewayError,validInternetGatewayName} from './ec2-internet-gateways.mjs';

const pageSize=20;
const identifier=value=><span className="resource-identifier">{value||'—'}</span>;

export default function VPCInternetGateways({request,region,acquireWrite,writeBlocked}) {
  const {t}=useLocale();
  const scope=useMemo(()=>({request,region}),[request,region]);
  const operation=useRef(null);
  const [saved,setSaved]=useState(null),[dialog,setDialog]=useState(null),[selected,setSelected]=useState(null),[page,setPage]=useState(1);
  const view=saved?.scope===scope?saved:null,modal=dialog?.scope===scope?dialog:null;
  const loading=!view||view.status==='loading',writing=view?.status==='writing',ready=view?.status==='ready';
  const rows=ready?view.rows:[];
  const selection=rows.includes(selected)?selected:null;
  const removable=selection?.managedBy==='ec2'&&!selection.vpc;
  const pages=Math.max(1,Math.ceil(rows.length/pageSize)),currentPage=Math.min(page,pages);

  function retire() {
    const old=operation.current;
    operation.current=null;
    if (old) {old.controller.abort();old.release?.();}
  }
  function begin(mutating) {
    if (operation.current?.release) return null;
    const release=mutating?acquireWrite?.():null;
    if (mutating&&typeof release!=='function') return null;
    retire();
    return operation.current={controller:new AbortController(),release};
  }
  function finish(current) {
    if (operation.current!==current||current.controller.signal.aborted) return false;
    operation.current=null;
    current.release?.();
    return true;
  }
  async function refresh() {
    const current=begin(false);
    if (!current) return;
    setSaved({scope,status:'loading'});setDialog(null);setSelected(null);setPage(1);
    try {
      const rows=await describeInternetGateways(scope.request,scope.region,current.controller.signal);
      if (finish(current)) setSaved({scope,status:'ready',rows});
    } catch (error) {
      if (finish(current)) setSaved({scope,status:internetGatewayError(error)});
    }
  }
  useEffect(()=>{void refresh();return retire;},[scope]);

  const valid=modal?.kind==='create'?validInternetGatewayName(modal.value):removable&&modal?.row===selection&&modal.value===selection.id;
  async function submit(event) {
    event.preventDefault();
    if (!ready||!valid||writeBlocked) return;
    const current=begin(true);
    if (!current) return;
    const signal=current.controller.signal;
    setSaved({scope,status:'writing'});
    try {
      const result=modal.kind==='create'?await createInternetGateway(scope.request,scope.region,modal.value,signal):
        await deleteInternetGateway(scope.request,scope.region,modal.row,signal);
      if (!finish(current)) return;
      setSaved({scope,status:'ready',rows:result.rows,notice:{id:result.id,kind:modal.kind}});
      setSelected(null);setPage(1);setDialog(null);
    } catch (error) {
      if (!finish(current)) return;
      setSaved({scope,status:internetGatewayError(error)});setSelected(null);setDialog(null);
    }
  }
  const create=t('Create internet gateway','インターネットゲートウェイを作成');
  const remove=t('Delete internet gateway','インターネットゲートウェイを削除');
  const notice=view?.notice;
  return <SpaceBetween size="s">
    <Table trackBy="id" selectionType="single" selectedItems={selection?[selection]:[]}
      ariaLabels={{tableLabel:t('Regional internet gateways','リージョンのインターネットゲートウェイ'),
        selectionGroupLabel:t('Select an internet gateway','インターネットゲートウェイを選択'),
        itemSelectionLabel:(_,item)=>t('Select gateway ','ゲートウェイを選択: ')+item.id}}
      onSelectionChange={({detail})=>{if(ready&&!writeBlocked)setSelected(detail.selectedItems[0]??null);}}
      loading={loading||writing} loadingText={writing?t('Verifying saved state','保存状態を確認中'):t('Loading internet gateways','インターネットゲートウェイを読み込み中')}
      items={rows.slice((currentPage-1)*pageSize,currentPage*pageSize)}
      header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">
        <Button disabled={writing} onClick={refresh}>{t('Refresh internet gateways','インターネットゲートウェイを更新')}</Button>
        <Button disabled={!ready||writeBlocked} onClick={()=>setDialog({scope,kind:'create',value:''})}>{create}</Button>
        <Button disabled={!ready||!removable||writeBlocked} onClick={()=>setDialog({scope,kind:'delete',value:'',row:selection})}>{remove}</Button>
      </SpaceBetween>}>{t('Internet gateways','インターネットゲートウェイ')}</Header>}
      columnDefinitions={[
        {id:'id',header:t('Internet gateway ID','インターネットゲートウェイ ID'),cell:row=>identifier(row.id)},
        {id:'name',header:t('Name','名前'),cell:row=>row.name||'—'},
        {id:'vpc',header:t('Attached VPC','接続先 VPC'),cell:row=>row.vpc?identifier(row.vpc):t('Detached','未接続')},
        {id:'owner',header:t('Managed by','管理元'),cell:row=>row.managedBy==='ec2'?'EC2':'CloudFormation'},
      ]}
      pagination={<Pagination currentPageIndex={currentPage} pagesCount={pages} onChange={({detail})=>{setPage(detail.currentPageIndex);setSelected(null);}}
        ariaLabels={{nextPageLabel:t('Next page','次のページ'),previousPageLabel:t('Previous page','前のページ'),pageLabel:n=>String(n)}}/>}
      empty={<Box>{ready?t('No internet gateways in this region.','このリージョンにインターネットゲートウェイはありません。'):
        t('Internet gateways are unavailable.','インターネットゲートウェイを取得できません。')}</Box>}/>
    <Alert type="info">{t('Create and delete detached EC2-managed gateways only. CloudFormation gateways and their attachments are read-only here. This does not enable routing or internet access.',
      '未接続の EC2 管理ゲートウェイのみ作成・削除できます。CloudFormation 管理のゲートウェイと接続先は表示のみです。ルーティングやインターネット接続は有効になりません。')}</Alert>
    {view?.status==='denied'&&<Alert type="error">{t('This identity cannot complete the internet gateway request. Check the EC2 permissions, then refresh.',
      'この ID ではインターネットゲートウェイの操作ができません。EC2 の権限を確認して更新してください。')}</Alert>}
    {view?.status==='unconfirmed'&&<Alert type="error">{t('The saved gateway state could not be verified. Refresh before trying again.',
      'ゲートウェイの保存状態を確認できませんでした。再試行する前に更新してください。')}</Alert>}
    {notice&&<Alert type="success">{(notice.kind==='create'?t('Saved internet gateway: ','インターネットゲートウェイを保存しました: '):
      t('Deleted internet gateway: ','インターネットゲートウェイを削除しました: '))+notice.id}</Alert>}
    {modal&&<Modal visible onDismiss={()=>{if(!writing)setDialog(null);}} header={modal.kind==='create'?create:remove}
      closeAriaLabel={t('Close internet gateway dialog','インターネットゲートウェイのダイアログを閉じる')}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={writing} onClick={()=>setDialog(null)}>{t('Cancel','キャンセル')}</Button>
        <Button formAction="submit" variant="primary" loading={writing} disabled={!valid||writeBlocked||!ready}>
          {modal.kind==='create'?t('Create','作成'):t('Delete','削除')}</Button></SpaceBetween>}>
        <SpaceBetween size="s">
          {modal.row&&<Box>{identifier(modal.row.id)}</Box>}
          <FormField label={modal.kind==='create'?t('Name (optional)','名前 (任意)'):
            t('Enter the gateway ID to confirm deletion','削除を確認するためゲートウェイ ID を入力')}>
            <Input value={modal.value} placeholder={modal.kind==='create'?'':'igw-…'} disabled={writing}
              onChange={({detail})=>setDialog(old=>old?.scope===scope?{...old,value:detail.value}:old)}/>
          </FormField>
        </SpaceBetween>
      </Form></form>
    </Modal>}
  </SpaceBetween>;
}
