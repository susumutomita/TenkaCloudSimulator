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
import {changeInternetGatewayAttachment,createInternetGateway,deleteInternetGateway,describeInternetGateways,
  internetGatewayError,validInternetGatewayName,validInternetGatewayVPC} from './ec2-internet-gateways.mjs';

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
  const native=selection?.managedBy==='ec2', detached=native&&!selection.vpc, attached=native&&Boolean(selection.vpc);
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

  const valid=modal?.kind==='create'?validInternetGatewayName(modal.value):
    native&&modal?.row===selection&&(modal.kind==='delete'?detached&&modal.value===selection.id:
      modal.kind==='attach'?detached&&validInternetGatewayVPC(modal.value):
        modal.kind==='detach'&&attached&&modal.value===selection.vpc);
  async function submit(event) {
    event.preventDefault();
    if (!ready||!valid||writeBlocked) return;
    const current=begin(true);
    if (!current) return;
    const signal=current.controller.signal;
    setSaved({scope,status:'writing'});
    try {
      const {request,region}=scope;
      const result=modal.kind==='create'?await createInternetGateway(request,region,modal.value,signal):
        modal.kind==='delete'?await deleteInternetGateway(request,region,modal.row,signal):
          await changeInternetGatewayAttachment(request,region,modal.row,modal.value,modal.kind==='attach',signal);
      if (!finish(current)) return;
      setSaved({scope,status:'ready',rows:result.rows,notice:{id:result.id,vpc:result.vpc,kind:modal.kind}});
      setSelected(null);setPage(1);setDialog(null);
    } catch (error) {
      if (!finish(current)) return;
      setSaved({scope,status:internetGatewayError(error)});setSelected(null);setDialog(null);
    }
  }
  const create=t('Create internet gateway','インターネットゲートウェイを作成');
  const remove=t('Delete internet gateway','インターネットゲートウェイを削除');
  const attach=t('Attach to VPC','VPC に接続');
  const detach=t('Detach from VPC','VPC から切断');
  const notice=view?.notice;
  const title={create,delete:remove,attach,detach};
  const failure={
    occupied:t('This gateway or VPC is already attached.','指定したゲートウェイまたは VPC はすでに接続されています。'),
    detached:t('This gateway is not attached to the selected VPC.','このゲートウェイは指定した VPC に接続されていません。'),
    unsupported:t('Only EC2-managed gateways and VPCs can be changed here.','ここで変更できるのは EC2 管理のゲートウェイと VPC のみです。'),
    dependent:t('This resource still has a configuration dependency.','このリソースには依存する設定が残っています。'),
    missing:t('The gateway or VPC was not found in this region.','このリージョンに指定されたゲートウェイまたは VPC が見つかりません。'),
  }[view?.status];
  function edit(kind) {setDialog({scope,kind,value:'',row:kind==='create'?null:selection});}
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
        <Button disabled={!ready||writeBlocked} onClick={()=>edit('create')}>{create}</Button>
        <Button disabled={!ready||!detached||writeBlocked} onClick={()=>edit('attach')}>{attach}</Button>
        <Button disabled={!ready||!attached||writeBlocked} onClick={()=>edit('detach')}>{detach}</Button>
        <Button disabled={!ready||!detached||writeBlocked} onClick={()=>edit('delete')}>{remove}</Button>
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
    <Alert type="info">{t('Manage local EC2-owned gateway attachments only. Delete requires a detached gateway. CloudFormation configuration is read-only. Attaching does not enable routing or internet access.',
      'ローカルの EC2 管理の接続設定のみ変更できます。削除は未接続の状態で行ってください。CloudFormation 管理の設定は表示のみです。接続してもルーティングやインターネット通信は有効になりません。')}</Alert>
    {view?.status==='denied'&&<Alert type="error">{t('This identity cannot complete the internet gateway request. Check the EC2 permissions, then refresh.',
      'この ID ではインターネットゲートウェイの操作ができません。EC2 の権限を確認して更新してください。')}</Alert>}
    {view?.status==='unconfirmed'&&<Alert type="error">{t('The saved gateway state could not be verified. Refresh before trying again.',
      'ゲートウェイの保存状態を確認できませんでした。再試行する前に更新してください。')}</Alert>}
    {failure&&<Alert type="error">{failure} {t('Refresh before trying again.','再試行する前に更新してください。')}</Alert>}
    {notice&&<Alert type="success">{({
      create:t('Saved internet gateway: ','インターネットゲートウェイを保存しました: '),
      delete:t('Deleted internet gateway: ','インターネットゲートウェイを削除しました: '),
      attach:t('Saved VPC attachment: ','VPC との接続設定を保存しました: '),
      detach:t('Removed VPC attachment: ','VPC との接続設定を削除しました: '),
    })[notice.kind]+notice.id+(notice.vpc?' / '+notice.vpc:'')}</Alert>}
    {modal&&<Modal visible onDismiss={()=>{if(!writing)setDialog(null);}} header={title[modal.kind]}
      closeAriaLabel={t('Close internet gateway dialog','インターネットゲートウェイのダイアログを閉じる')}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={writing} onClick={()=>setDialog(null)}>{t('Cancel','キャンセル')}</Button>
        <Button formAction="submit" variant="primary" loading={writing} disabled={!valid||writeBlocked||!ready}>
          {({create:t('Create','作成'),delete:t('Delete','削除'),attach:t('Attach','接続'),detach:t('Detach','切断')})[modal.kind]}</Button></SpaceBetween>}>
        <SpaceBetween size="s">
          {modal.row&&<Box>{identifier(modal.row.id)}{modal.row.vpc&&<> / {identifier(modal.row.vpc)}</>}</Box>}
          <FormField label={({
            create:t('Name (optional)','名前 (任意)'),
            delete:t('Enter the gateway ID to confirm deletion','削除を確認するためゲートウェイ ID を入力'),
            attach:t('EC2-managed VPC ID','EC2 管理の VPC ID'),
            detach:t('Enter the attached VPC ID to confirm','切断を確認するため接続先の VPC ID を入力'),
          })[modal.kind]} description={modal.kind==='attach'&&t('Copy the ID from Your VPCs above.','上の「VPC」一覧から ID をコピーしてください。')}>
            <Input value={modal.value} placeholder={modal.kind==='create'?'':modal.kind==='delete'?'igw-…':'vpc-…'} disabled={writing}
              onChange={({detail})=>setDialog(old=>old?.scope===scope?{...old,value:detail.value}:old)}/>
          </FormField>
        </SpaceBetween>
      </Form></form>
    </Modal>}
  </SpaceBetween>;
}
