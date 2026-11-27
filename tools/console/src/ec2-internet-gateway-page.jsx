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
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import TextFilter from '@cloudscape-design/components/text-filter';
import {useLocale} from './i18n.jsx';
import {changeInternetGatewayAttachment,createInternetGateway,deleteInternetGateway,describeInternetGateways,
  internetGatewayError,validInternetGatewayName} from './ec2-internet-gateways.mjs';
import {filterInternetGateways,internetGatewayActions,loadInternetGatewayVPCs} from './ec2-internet-gateway-console.mjs';

const pageSize=20, emptyRows=Object.freeze([]);
const idText=value=><span className="resource-identifier">{value}</span>;

function attachment(row,t) {
  return <StatusIndicator type={row.vpc?'success':'stopped'}>{row.vpc?t('Attached','接続済み'):t('Detached','未接続')}</StatusIndicator>;
}
const owner=row=>row.managedBy==='ec2'?'EC2':'CloudFormation';

function vpcDisplay(id,vpcs,onOpenVpc,locked) {
  if (!id) return '—';
  const vpc=vpcs.find(vpc=>vpc.id===id),name=vpc?.name;
  const label=<>{name&&<>{name} · </>}{idText(id)}</>;
  return onOpenVpc&&vpc?<Button variant="link" disabled={locked} onClick={()=>onOpenVpc(id)}>{label}</Button>:label;
}

export function InternetGatewayInventory({rows,vpcs,filter,setFilter,page,setPage,open,busy,unavailable,actions}) {
  const {t}=useLocale();
  const filtered=filterInternetGateways(rows,vpcs,filter,{attached:t('Attached','接続済み'),detached:t('Detached','未接続')});
  const pages=Math.max(1,Math.ceil(filtered.length/pageSize)),currentPage=Math.min(page,pages);
  return <Table trackBy="id" ariaLabels={{tableLabel:t('Regional internet gateways','リージョンのインターネットゲートウェイ')}}
    items={filtered.slice((currentPage-1)*pageSize,currentPage*pageSize)} loading={busy}
    loadingText={t('Reading saved internet gateway configuration','保存済みゲートウェイ設定を読み込み中')}
    header={<Header variant="h2" counter={'('+filtered.length+(filtered.length!==rows.length?'/'+rows.length:'')+')'} actions={actions}>
      {t('Internet gateways','インターネットゲートウェイ')}</Header>}
    columnDefinitions={[
      {id:'name',header:t('Name','名前'),cell:row=>row.name||'—'},
      {id:'id',header:t('Internet gateway ID','インターネットゲートウェイ ID'),cell:row=><Button variant="link" disabled={busy} onClick={()=>open(row.id)}>{idText(row.id)}</Button>},
      {id:'state',header:t('Attachment state','接続状態'),cell:row=>attachment(row,t)},
      {id:'vpc',header:'VPC ID',cell:row=>vpcDisplay(row.vpc,vpcs,null,false)},
      {id:'owner',header:t('Managed by','管理元'),cell:owner},
    ]}
    filter={<TextFilter filteringText={filter} disabled={busy} onChange={({detail})=>{setFilter(detail.filteringText);setPage(1);}}
      filteringAriaLabel={t('Search internet gateways','インターネットゲートウェイを検索')}
      filteringPlaceholder={t('Search by name, gateway ID, VPC, or state','名前、ゲートウェイ ID、VPC、状態で検索')}/>}
    pagination={<Pagination currentPageIndex={currentPage} pagesCount={pages} disabled={busy}
      onChange={({detail})=>setPage(detail.currentPageIndex)}
      ariaLabels={{nextPageLabel:t('Next page','次のページ'),previousPageLabel:t('Previous page','前のページ'),pageLabel:n=>String(n)}}/>}
    empty={<Box>{unavailable?t('Internet gateways are unavailable. Use Refresh to try again.','インターネットゲートウェイを取得できません。更新してください。'):
      rows.length?t('No internet gateways match your search.','検索条件に一致するゲートウェイはありません。'):
        t('No internet gateways in this region.','このリージョンにゲートウェイはありません。')}</Box>}/>;
}

export function InternetGatewayDetails({row,vpcs,onOpenVpc,locked,actions}) {
  const {t}=useLocale();
  return <Container header={<Header variant="h2" actions={actions}>{t('Gateway details','ゲートウェイの詳細')}</Header>}>
    <KeyValuePairs columns={2} minColumnWidth={220} items={[
      {label:t('Name','名前'),value:row.name||'—'},
      {label:t('Internet gateway ID','インターネットゲートウェイ ID'),value:idText(row.id)},
      {label:t('Attachment state','接続状態'),value:attachment(row,t)},
      {label:t('Attached VPC','接続先 VPC'),value:vpcDisplay(row.vpc,vpcs,onOpenVpc,locked)},
      {label:t('Managed by','管理元'),value:owner(row)},
    ]}/>
  </Container>;
}

function failureMessage(code,t) {
  return ({
    denied:t('This identity does not have permission for the gateway request.','この ID にはゲートウェイを操作する権限がありません。'),
    occupied:t('The gateway or selected VPC already has an attachment.','ゲートウェイまたは選択した VPC はすでに接続されています。'),
    detached:t('The gateway is no longer attached to this VPC.','ゲートウェイはこの VPC に接続されていません。'),
    unsupported:t('This action requires an EC2-managed gateway and, for attachment changes, an EC2-managed VPC.','この操作には EC2 管理のゲートウェイが必要です。接続の変更には EC2 管理の VPC も必要です。'),
    dependent:t('The resource still has a dependency, such as a route using this gateway.','このゲートウェイを使うルートなど、依存する設定が残っています。'),
    missing:t('The gateway or selected VPC no longer exists in this region.','このリージョンにゲートウェイまたは選択した VPC がありません。'),
  })[code]??t('The saved gateway state could not be verified.','ゲートウェイの保存状態を確認できませんでした。');
}

export default function InternetGatewaysPage({request,region,gatewayId,onNavigateGateway,onOpenVpc,acquireWrite,operations,writeBlocked=false}) {
  const {t}=useLocale();
  const vpcRead=operations?.vpcs===true, read=operations?.read===true;
  const scope=useMemo(()=>({request,region,vpcRead,read}),[request,region,vpcRead,read]);
  const operation=useRef(null), heading=useRef(null), focusNext=useRef(false);
  const [saved,setSaved]=useState(null),[draft,setDraft]=useState(null);
  const [localId,setLocalId]=useState(null),[filter,setFilter]=useState(''),[page,setPage]=useState(1);
  const view=saved?.scope===scope?saved:null, modal=draft?.scope===scope?draft:null;
  const rows=view?.rows??emptyRows,vpcs=view?.vpcs??emptyRows;
  const visibleId=gatewayId??(localId?.scope===scope?localId.id:'');
  const row=rows.find(item=>item.id===visibleId);
  const loading=!view||view.status==='loading',writing=view?.status==='writing',ready=view?.status==='ready';
  const writable=ready&&!writeBlocked&&typeof acquireWrite==='function';
  const actions=internetGatewayActions(row,rows,vpcs);
  const names={
    create:t('Create internet gateway','インターネットゲートウェイを作成'),
    attach:t('Attach to VPC','VPC に接続'),
    detach:t('Detach from VPC','VPC から切断'),
    delete:t('Delete internet gateway','インターネットゲートウェイを削除'),
  };

  function retire() {
    const previous=operation.current;
    operation.current=null;
    if (previous) {previous.controller.abort();previous.release?.();}
  }
  function finish(current) {
    if (operation.current!==current||current.controller.signal.aborted) return false;
    operation.current=null;
    current.release?.();
    return true;
  }
  async function refresh() {
    if (!scope.read||operation.current?.release) return;
    retire();
    const current=operation.current={controller:new AbortController()};
    const signal=current.controller.signal;
    setSaved({scope,status:'loading',rows,vpcs});setDraft(null);
    const [gateways,networks]=await Promise.allSettled([
      describeInternetGateways(request,region,signal),scope.vpcRead?loadInternetGatewayVPCs(request,region,{signal}):emptyRows,
    ]);
    if (!finish(current)) return;
    const vpcReadError=networks.status==='rejected';
    const networksRead=vpcReadError?emptyRows:networks.value;
    if (gateways.status==='fulfilled') {
      setSaved({scope,status:'ready',rows:gateways.value,vpcs:networksRead,vpcReadError});
    } else {
      setSaved({scope,status:'error',rows,vpcs:networksRead,vpcReadError,error:internetGatewayError(gateways.reason)});
    }
  }
  useEffect(()=>{setFilter('');setPage(1);void refresh();return retire;},[scope]);
  useEffect(()=>{if(focusNext.current){heading.current?.focus();focusNext.current=false;}},[visibleId]);

  function navigate(id) {
    if (operation.current?.release) return;
    focusNext.current=true;
    setLocalId({scope,id});setDraft(null);onNavigateGateway?.(id);
  }
  function openAction(kind) {
    if (!writable||operations[kind]!==true) return;
    setDraft({scope,kind,value:'',row:kind==='create'?null:row});
  }
  const validDraft=modal?.kind==='create'?validInternetGatewayName(modal.value):
    !!row&&modal?.row===row&&(modal.kind==='attach'?actions.attachTargets.some(vpc=>vpc.id===modal.value):
      modal.kind==='detach'?actions.detach:modal.kind==='delete'&&actions.delete&&modal.value===row.id);
  async function submit(event) {
    event.preventDefault();
    if (!writable||!validDraft||operation.current||operations[modal.kind]!==true) return;
    const release=acquireWrite();
    if (typeof release!=='function') return;
    const current=operation.current={controller:new AbortController(),release};
    const signal=current.controller.signal;
    const {kind,value,row:target}=modal;
    let result, networkWitnesses=vpcs;
    setSaved({scope,status:'writing',rows,vpcs,vpcReadError:view.vpcReadError});
    try {
      if (kind==='create') result=await createInternetGateway(request,region,value,signal);
      else if (kind==='delete') result=await deleteInternetGateway(request,region,target,signal);
      else {
        const vpcID=kind==='attach'?value:target.vpc;
        // Recheck ownership under this same request and region before sending
        // the native mutation. The server still arbitrates races and IAM.
        networkWitnesses=await loadInternetGatewayVPCs(request,region,{signal});
        const live=networkWitnesses.find(vpc=>vpc.id===vpcID);
        if (!live) throw new Error('InvalidVpcID.NotFound: VPC no longer exists.');
        if (live.managedBy!=='ec2') throw new Error('UnsupportedOperation: VPC is not managed by EC2.');
        result=await changeInternetGatewayAttachment(request,region,target,vpcID,kind==='attach',signal);
      }
      if (!finish(current)) return;
      setDraft(null);
      setSaved({scope,status:'ready',rows:result.rows,vpcs:networkWitnesses,
        vpcReadError:networkWitnesses===vpcs&&view.vpcReadError,notice:{kind,id:result.id,vpc:result.vpc}});
      if (kind==='delete') navigate('');
    } catch (error) {
      if (!finish(current)) return;
      setDraft(null);
      setSaved({scope,status:'stale',rows,vpcs:networkWitnesses,vpcReadError:view.vpcReadError,error:internetGatewayError(error)});
    }
  }
  const title=t('Internet gateways','インターネットゲートウェイ');
  const optionFor=vpc=>({value:vpc.id,label:vpc.name?vpc.name+' · '+vpc.id:vpc.id,description:vpc.cidr});
  const options=modal?.kind==='attach'?actions.attachTargets.map(optionFor):emptyRows;
  const notice=view?.notice;
  const messages={
    create:t('Created internet gateway: ','インターネットゲートウェイを作成しました: '),
    attach:t('Saved the VPC attachment: ','VPC との接続設定を保存しました: '),
    detach:t('Removed the VPC attachment: ','VPC との接続設定を削除しました: '),
    delete:t('Deleted internet gateway: ','インターネットゲートウェイを削除しました: '),
  };
  const refreshButton=<Button iconName="refresh" loading={loading} disabled={writing} onClick={refresh}>{t('Refresh','更新')}</Button>;
  const updateValue=value=>setDraft(old=>old?.scope===scope?{...old,value}:old);

  return <ContentLayout header={<div ref={heading} tabIndex={-1}><Header variant="h1" description={region}
    actions={visibleId?<SpaceBetween direction="horizontal" size="xs">
      <Button disabled={writing} onClick={()=>navigate('')}>{t('Back to internet gateways','インターネットゲートウェイ一覧に戻る')}</Button>{refreshButton}
    </SpaceBetween>:undefined}>{visibleId&&row?idText(row.id):title}</Header></div>}>
    <SpaceBetween size="l">
      <Alert type="info">{t('This simulator manages saved attachment settings only. Attaching a gateway does not enable routing or internet access.',
        'このシミュレーターで管理できるのは保存済みの接続設定だけです。ゲートウェイを接続してもルーティングやインターネット通信は有効になりません。')}</Alert>
      {view?.error&&<Alert type="error" header={t('Could not complete the internet gateway request','インターネットゲートウェイの操作を完了できませんでした')}>
        {failureMessage(view.error,t)} {t('Refresh to verify the current state before trying again.','再試行する前に更新して現在の状態を確認してください。')}</Alert>}
      {notice&&<Alert type="success" dismissible dismissAriaLabel={t('Dismiss','閉じる')} onDismiss={()=>setSaved(old=>old?.scope===scope?{...old,notice:null}:old)}>
        {messages[notice.kind]}{idText(notice.id)}{notice.vpc&&<> / {idText(notice.vpc)}</>}</Alert>}
      {view?.vpcReadError&&<Alert type="warning">{t('The VPC list could not be verified. Attach and detach are unavailable until it can be refreshed.',
        'VPC 一覧を確認できませんでした。一覧を更新できるまで接続と切断はできません。')}</Alert>}
      {!visibleId?<InternetGatewayInventory rows={rows} vpcs={vpcs} filter={filter} setFilter={setFilter} page={page} setPage={setPage}
        open={navigate} busy={loading||writing} unavailable={view?.status==='error'} actions={<SpaceBetween direction="horizontal" size="xs">
          {refreshButton}{operations?.create&&<Button variant="primary" disabled={!writable} onClick={()=>openAction('create')}>{names.create}</Button>}
        </SpaceBetween>}/>
        :row?<SpaceBetween size="m">
          {row.managedBy!=='ec2'&&<Alert type="info">{t('CloudFormation manages this gateway. Change or remove it through its stack.',
            'このゲートウェイは CloudFormation が管理しています。変更や削除はスタックから行ってください。')}</Alert>}
          {ready&&operations?.attach&&row.managedBy==='ec2'&&!row.vpc&&!actions.attachTargets.length&&!view.vpcReadError&&
            <Alert type="info">{t('No detached EC2-managed VPC is available for attachment.','接続できる未接続の EC2 管理 VPC がありません。')}</Alert>}
          <InternetGatewayDetails row={row} vpcs={vpcs} onOpenVpc={onOpenVpc} locked={writing} actions={row.managedBy==='ec2'?
            <SpaceBetween direction="horizontal" size="xs">
              {!row.vpc?<>{operations?.attach&&<Button variant="primary" disabled={!writable||!actions.attachTargets.length} onClick={()=>openAction('attach')}>{names.attach}</Button>}
                {operations?.delete&&<Button disabled={!writable||!actions.delete} onClick={()=>openAction('delete')}>{names.delete}</Button>}</>:
                operations?.detach&&<Button disabled={!writable||!actions.detach} onClick={()=>openAction('detach')}>{names.detach}</Button>}
            </SpaceBetween>:undefined}/>
        </SpaceBetween>:<Box role="status">{loading?t('Loading internet gateway details','インターネットゲートウェイの詳細を読み込み中'):
          ready?t('This internet gateway was not found in this region.','このリージョンにインターネットゲートウェイはありません。'):
            t('Gateway details are unavailable. Use Refresh to try again.','ゲートウェイの詳細を取得できません。更新してください。')}</Box>}
      {modal&&<Modal visible header={names[modal.kind]} onDismiss={()=>{if(!writing)setDraft(null);}}
        closeAriaLabel={t('Close gateway dialog','ゲートウェイのダイアログを閉じる')}>
        <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
          <Button formAction="none" disabled={writing} onClick={()=>setDraft(null)}>{t('Cancel','キャンセル')}</Button>
          <Button formAction="submit" variant="primary" loading={writing} disabled={!writable||!validDraft}>{
            ({create:t('Create','作成'),attach:t('Attach','接続'),detach:t('Detach','切断'),delete:t('Delete','削除')})[modal.kind]}</Button>
        </SpaceBetween>}>
          <SpaceBetween size="m">
            {modal.row&&<Box>{idText(modal.row.id)}{modal.row.vpc&&<> / {idText(modal.row.vpc)}</>}</Box>}
            {modal.kind==='attach'?<FormField label={t('EC2-managed VPC','EC2 管理の VPC')}>
              <Select selectedOption={options.find(option=>option.value===modal.value)??null} options={options} filteringType="auto" disabled={writing}
                placeholder={t('Choose a VPC','VPC を選択')} onChange={({detail})=>updateValue(detail.selectedOption.value)}/>
            </FormField>:modal.kind==='detach'?<Box>{t('Remove the saved attachment? Routes that use this gateway must be removed first.',
              '保存済みの接続を削除しますか？ 先にこのゲートウェイを使うルートを削除してください。')}</Box>:
              <FormField label={modal.kind==='create'?t('Name (optional)','名前（任意）'):
                t('Enter the gateway ID to confirm deletion','削除を確認するためゲートウェイ ID を入力')}>
                <Input value={modal.value} disabled={writing} onChange={({detail})=>updateValue(detail.value)}/>
              </FormField>}
          </SpaceBetween>
        </Form></form>
      </Modal>}
    </SpaceBetween>
  </ContentLayout>;
}
