import React, {useEffect, useMemo, useRef, useState} from 'react';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Table from '@cloudscape-design/components/table';
import Button from '@cloudscape-design/components/button';
import Alert from '@cloudscape-design/components/alert';
import SpaceBetween from '@cloudscape-design/components/space-between';
import TextFilter from '@cloudscape-design/components/text-filter';
import Pagination from '@cloudscape-design/components/pagination';
import Container from '@cloudscape-design/components/container';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Box from '@cloudscape-design/components/box';
import Tabs from '@cloudscape-design/components/tabs';
import {useLocale} from './i18n.jsx';
import VPCCreate from './ec2-vpc-create.jsx';
import VPCNativeSubnets from './ec2-vpc-native-subnets.jsx';
import VPCDNSSettings from './ec2-vpc-dns-settings.jsx';
import VPCResourceMap from './ec2-vpc-resource-map.jsx';
import {createVpcResourceMapScope} from './ec2-vpc-topology-read.mjs';
import {createVpcWithSubnet,vpcCreateIssues} from './ec2-vpc-subnet.mjs';
import EC2InstanceMetadata from './ec2-instance-metadata.jsx';
import EC2TemplatePreview from './ec2-template-preview.jsx';
import EC2InstanceStackLaunch from './ec2-instance-stack-launch.jsx';
import VPCSecurityGroups from './ec2-vpc-security-groups.jsx';
import VPCNetworkACLs from './ec2-vpc-network-acls.jsx';
import VPCRouteTables from './ec2-vpc-route-tables.jsx';
import VPCNativeRouteTables from './ec2-vpc-native-route-tables.jsx';
import VPCInternetGateways from './ec2-vpc-internet-gateways.jsx';
import {nativeRouteTableTarget} from './ec2-native-route-tables.mjs';

import {parseEC2Document,parseCreateVpc,parseDeleteVpc,parseEC2Error,createVpcParameters} from './ec2-response.mjs';
import VpcTagsPanel from './ec2-vpc-tags-panel.jsx';
import {vpcTagEditingAvailable} from './ec2-vpc-tags.mjs';

const idCell=value=><span className="resource-identifier">{value||'—'}</span>;
const blankVpc={name:'',cidr:'10.0.0.0/16',mode:'vpc-only',stackName:'',subnetCIDR:'10.0.1.0/24'};

// A single selected VPC owns this tab state. Default to the existing details
// (including DNS). A hidden map is not a background inventory reader.
export function VPCDetailsTabs({vpc,region,request,capabilities,authenticationRequired,saving,selectionLocked,children}) {
  const {t}=useLocale();
  const [activeTab,setActiveTab]=useState('details');
  const mapAvailable=useMemo(()=>!!createVpcResourceMapScope(request,region,vpc,capabilities,authenticationRequired),
    [request,region,vpc,capabilities,authenticationRequired]);
  if (!mapAvailable) return children;
  const otherTabDisabled=id=>saving&&activeTab!==id;
  return <Tabs activeTabId={activeTab} ariaLabel={t('VPC details views','VPC 詳細の表示')}
    onChange={({detail})=>{
      // The ref-backed guard also catches transitions before React has
      // painted the disabled state, including a DNS readback in progress.
      if (!selectionLocked() && ['details','resource-map'].includes(detail.activeTabId)) setActiveTab(detail.activeTabId);
    }}
    tabs={[
      {id:'details',label:t('Details','詳細'),disabled:otherTabDisabled('details'),content:children},
      {id:'resource-map',label:t('Resource map','リソースマップ'),disabled:otherTabDisabled('resource-map'),content:
        <VPCResourceMap request={request} region={region} vpc={vpc} capabilities={capabilities}
          authenticationRequired={authenticationRequired} active={activeTab==='resource-map'&&!saving}
          navigationBlocked={saving}/>},
    ]}/>;
}

// The instance list is saved state. Opt-in local launches use CloudFormation,
// never native RunInstances. VPC deletion applies only to EC2-owned VPCs.
export default function EC2Readonly({kind,region,request,sessionInstance,onSession,instanceStackLaunchVisible=false,instanceStackLaunchAllowed=false,vpcLifecycle=false,vpcSubnet=false,vpcNativeSubnets=false,vpcNativeSubnetPublicIPv4=false,vpcSecurityGroups=false,vpcNativeSecurityGroups=false,vpcNativeSecurityGroupRules=false,vpcNetworkACLs=false,vpcRouteTables=false,vpcNativeRouteTables=false,vpcNativeRouteAssociations=false,vpcNativeRouteReplacement=false,vpcNativeIPv4RouteLimit=0,vpcInternetGateways=false,vpcDetailsCapabilities,vpcDetailsAuthenticationRequired=false,onSaving=()=>{},onVpcCreateView=()=>{},initialVpcCreate=false,initialVpcId=''}) {
  const {t}=useLocale();
  const instance=kind==='ec2',manage=!instance&&vpcLifecycle;
  const [items,setItems]=useState([]),[loading,setLoading]=useState(false),[error,setError]=useState('');
  const [preview,setPreview]=useState(false),[launchOpen,setLaunchOpen]=useState(false);
  const [selected,setSelected]=useState([]),[filter,setFilter]=useState(''),[page,setPage]=useState(1);
  const [view,setView]=useState(initialVpcCreate?'create':'list'),[form,setForm]=useState(blankVpc),[busy,setBusy]=useState(false),[formError,setFormError]=useState(''),[notice,setNotice]=useState('');
  const [deleting,setDeleting]=useState(null),[confirmation,setConfirmation]=useState(''),[deleteError,setDeleteError]=useState('');
  const [writeLeases,setWriteLeases]=useState(()=>new Set());
  const pendingWrites=useRef(writeLeases);
  // Stable per-pane leases aggregate on the host. A readback completion or
  // unmount releases only its own lease, even if another pane is still saving.
  const lease=useMemo(()=>Object.fromEntries(['subnet','sg','acl','rtb','igw','dns','tags'].map(pane=>[pane,open=>{
    const current=pendingWrites.current;
    if(current.has(pane)===open)return;
    const next=new Set(current);
    if(open)next.add(pane);else next.delete(pane);
    pendingWrites.current=next;
    setWriteLeases(next);
  }])),[]);
  const saving=busy||writeLeases.size>0;
  const selectionLocked=()=>busy||pendingWrites.current.size>0;
  const siblingBusy=pane=>busy||[...writeLeases].some(owner=>owner!==pane);
  function acquireNetworkWrite(pane){
    if (selectionLocked()) return null;
    lease[pane](true);
    let held=true;
    return ()=>{if(held){held=false;lease[pane](false);}};
  }
  const epoch=useRef(0),alive=useRef(true),submitting=useRef(false),topology=useRef(null);
  const heading=useRef(null), focusNext=useRef(initialVpcCreate);
  const paired=form.mode==='vpc-subnet';
  function changeView(next){focusNext.current=true;setView(next);onVpcCreateView(next==='create');}
  useEffect(()=>{if(focusNext.current){heading.current?.focus();focusNext.current=false;}},[view]);
  useEffect(()=>{alive.current=true;return()=>{alive.current=false;epoch.current++;topology.current?.abort();onSaving(false);};},[]);
  useEffect(()=>{onSaving(saving);},[saving,onSaving]);
  const headers={'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region};
  async function load(select){
    const current=++epoch.current;setLoading(true);setError('');setItems([]);setSelected([]);setPage(1);
    try {
      const action=instance?'DescribeInstances':'DescribeVpcs';
      const response=await request('/',{method:'POST',headers,body:new URLSearchParams({Action:action,Version:'2016-11-15'})});
      const xml=new DOMParser().parseFromString(await response.text(),'application/xml');
      if(!response.ok)throw Error(t('Could not retrieve resources.','リソースを取得できませんでした。'));
      const rows=parseEC2Document(xml,kind);
      if(alive.current&&current===epoch.current){setItems(rows);setSelected(rows.filter(row=>row.id===select));}
    }catch(e){if(alive.current&&current===epoch.current)setError(e.message);}
    finally{if(alive.current&&current===epoch.current)setLoading(false);}
  }
  useEffect(()=>{load(initialVpcId);},[kind,region,initialVpcId]);
  // A refused mutation reports the server's EC2 error code; it is never shown as success.
  async function mutate(body){
    const response=await request('/',{method:'POST',headers,body});
    const xml=new DOMParser().parseFromString(await response.text(),'application/xml');
    if(!response.ok){const failure=parseEC2Error(xml);throw Error(failure?failure.code+': '+failure.message:t('The request failed.','リクエストに失敗しました。'));}
    return xml;
  }
  async function create(event){
    event.preventDefault();if(selectionLocked()||submitting.current)return;
    if(paired&&!vpcSubnet){setFormError(t('Only the signed-in simulator administrator can create a stack here.','ここでスタックを作成できるのは、サインイン済みのシミュレーター管理者だけです。'));return;}
    if(Object.keys(vpcCreateIssues(form,paired)).length){setFormError(t('Check the VPC settings before creating.','作成前に VPC の設定を確認してください。'));return;}
    submitting.current=true;setBusy(true);setFormError('');
    try {
      if(paired){
        const handle=new AbortController();topology.current=handle;
        const verified=await createVpcWithSubnet(request,region,form,{allowed:vpcSubnet,signal:handle.signal});
        if(!alive.current||handle.signal.aborted)return;
        changeView('list');setForm(blankVpc);setFilter('');
        setNotice(t('CloudFormation stack and saved network verified: ','CloudFormation スタックと保存済みネットワークを確認: ')+form.stackName+' · '+verified.vpcId+' · '+verified.subnetId);
        await load(verified.vpcId);
        return;
      }
      const vpc=parseCreateVpc(await mutate(createVpcParameters(form)));
      if(!alive.current)return;
      changeView('list');setForm(blankVpc);setFilter('');setNotice(t('You successfully created ','VPC を作成しました: ')+vpc.id);
      await load(vpc.id);
    }catch(e){
      if(alive.current)setFormError(paired?t('Check the CloudFormation stack before retrying. ','再試行前に CloudFormation のスタックを確認してください。 ')+e.message:e.message);
    }finally{topology.current=null;submitting.current=false;if(alive.current)setBusy(false);}
  }
  async function remove(event){
    event.preventDefault();if(selectionLocked()||!deleting)return;
    if(confirmation!=='delete'){setDeleteError(t('Type delete to confirm.','確認のため delete と入力してください。'));return;}
    setBusy(true);setDeleteError('');
    try {
      parseDeleteVpc(await mutate(new URLSearchParams({Action:'DeleteVpc',Version:'2016-11-15',VpcId:deleting.id})));
      if(!alive.current)return;
      setNotice(t('Deleted ','VPC を削除しました: ')+deleting.id);setDeleting(null);
      await load();
    }catch(e){if(alive.current)setDeleteError(e.message);}
    finally{if(alive.current)setBusy(false);}
  }
  const managedLabel=value=>value==='ec2'?'EC2':value==='cloudformation'?'CloudFormation':t('Unconfirmed','未確認');
  const fields=instance?[['id','Instance ID','インスタンスID'],['name','Name','名前'],['state','Recorded runtime state','記録されたランタイム状態'],['type','Instance type','インスタンスタイプ'],['vpc','VPC ID','VPC ID'],['subnet','Subnet ID','サブネットID'],['image','Image ID','イメージID']]:[['id','VPC ID','VPC ID'],['name','Name','名前'],['cidr','IPv4 CIDR','IPv4 CIDR'],['managedBy','Managed by','管理元']];
  const display=(key,item)=>key==='state'&&(!item[key]||item[key]==='unknown')?t('Unconfirmed','未確認'):key==='managedBy'?managedLabel(item[key]):item[key];
  const query=filter.toLocaleLowerCase();
  const filtered=items.filter(item=>fields.some(([field])=>item[field].toLocaleLowerCase().includes(query)));
  const detail=selected[0];
  const detailFields=detail&&<KeyValuePairs columns={2} items={fields.map(([key,en,ja])=>({label:t(en,ja),value:idCell(display(key,detail))}))}/>;
  if(manage&&view==='create')return <VPCCreate values={form} pairedAvailable={vpcSubnet} heading={heading}
    busy={busy} requestError={formError} submit={create} change={(key,value)=>{setForm(old=>({...old,[key]:value}));setFormError('');}}
    cancel={()=>{changeView('list');setFormError('');}}/>;
  const deletable=detail?.managedBy==='ec2';
  const nativeRoutePane=vpcNativeRouteTables&&nativeRouteTableTarget(detail);
  return <ContentLayout header={<div ref={heading} tabIndex={-1}><Header variant="h1">{instance?t('EC2 instances','EC2 インスタンス'):'VPC'}</Header></div>}><SpaceBetween size="l">
    {manage?<Alert type="info">{t('Delete CloudFormation-managed networks through their stack. In an EC2-managed VPC, select a resource to use the available subnet, route table or gateway controls. These settings do not forward traffic.','CloudFormation 管理のネットワークはスタックから削除してください。EC2 管理の VPC では、対象を選択してサブネット、ルートテーブル、ゲートウェイの利用可能な設定を変更できます。設定によって実際の通信が転送されるわけではありません。')}</Alert>
      :<Alert type="info">{instance?t('Instances are managed through CloudFormation. This list shows saved state, not guest health. There are no direct start, stop or network-edit controls.','インスタンスは CloudFormation で管理されます。ここに表示されるのは保存済みの状態で、ゲストの稼働確認ではありません。直接の起動・停止、ネットワーク編集には対応していません。')
        :t('Read-only resources managed through CloudFormation. Create, start, stop and network edits are not supported here. Reported state is not a health check of the guest.','CloudFormation で管理されるリソースの読み取り専用画面です。この画面から作成・起動・停止・ネットワーク編集はできません。表示された状態はゲストの稼働確認ではありません。')}</Alert>}
    {instance&&instanceStackLaunchVisible&&!instanceStackLaunchAllowed&&<Alert type="info">{t(
      'Instance creation requires the opt-in local VM runtime (--battle-runtime-config). With the default startup you can only preview a template.',
      'インスタンスの作成には、明示的に設定したローカル VM 実行環境（--battle-runtime-config）が必要です。既定の起動ではテンプレートのプレビューのみ利用できます。')}</Alert>}
    {notice&&<Alert type="success" dismissible dismissAriaLabel={t('Dismiss','閉じる')} onDismiss={()=>setNotice('')}>{notice}</Alert>}
    {error&&<Alert type="error" header={t('Could not load resources','リソースを読み込めませんでした')}>{error}</Alert>}
    <Table trackBy="id" selectionType="single" selectedItems={selected} onSelectionChange={({detail})=>{if(!selectionLocked())setSelected(detail.selectedItems);}}
      ariaLabels={{selectionGroupLabel:t('Select a resource','リソースを選択'),itemSelectionLabel:(_,item)=>t('Select ','選択: ')+item.id}}
      loading={loading} loadingText={t('Loading resources','リソースを読み込み中')}
      items={filtered.slice((page-1)*20,page*20)} columnDefinitions={fields.slice(0,instance?5:4).map(([id,en,ja])=>({id,header:t(en,ja),cell:item=>idCell(display(id,item))}))}
      header={<Header counter={'('+items.length+')'} actions={<SpaceBetween direction="horizontal" size="xs">{sessionInstance&&<Button variant="primary" disabled={selected[0]?.id!==sessionInstance} onClick={onSession}>{t('Connect with Session Manager','Session Manager で接続')}</Button>}<Button iconName="refresh" loading={loading} disabled={saving} onClick={()=>load(detail?.id||initialVpcId)}>{t('Refresh','更新')}</Button>
        {instance&&!sessionInstance&&instanceStackLaunchVisible&&<Button variant="primary" disabled={saving||!instanceStackLaunchAllowed}
          disabledReason={!instanceStackLaunchAllowed?t('Configure the local VM runtime first.','ローカル VM 実行環境を先に設定してください。'):undefined}
          onClick={()=>setLaunchOpen(true)}>{t('Create instance stack','インスタンスのスタックを作成')}</Button>}
        {instance&&!sessionInstance&&<Button disabled={saving} onClick={()=>setPreview(true)}>{t('Preview CloudFormation template','CloudFormation テンプレートをプレビュー')}</Button>}
        {manage&&<Button disabled={!deletable||saving||loading} disabledReason={detail&&!deletable?t('Delete CloudFormation-managed VPCs by deleting their stack.','CloudFormation で管理される VPC は、スタックを削除して削除してください。'):undefined} onClick={()=>{setConfirmation('');setDeleteError('');setDeleting(detail);}}>{t('Delete VPC','VPC を削除')}</Button>}
        {manage&&<Button variant="primary" disabled={saving} onClick={()=>{setForm(blankVpc);setFormError('');setNotice('');changeView('create');}}>{t('Create VPC','VPC を作成')}</Button>}</SpaceBetween>}>{instance?t('Instances','インスタンス'):t('VPCs','VPC 一覧')}</Header>}
      filter={<TextFilter filteringText={filter} disabled={saving} onChange={({detail})=>{if(!selectionLocked()){setFilter(detail.filteringText);setPage(1);setSelected([]);}}} filteringAriaLabel={t('Filter resources','リソースを絞り込む')} filteringPlaceholder={t('Find resources','リソースを検索')}/>}
      pagination={<Pagination currentPageIndex={page} pagesCount={Math.max(1,Math.ceil(filtered.length/20))} onChange={({detail})=>{if(!selectionLocked())setPage(detail.currentPageIndex);}} ariaLabels={{nextPageLabel:t('Next page','次のページ'),previousPageLabel:t('Previous page','前のページ'),pageLabel:n=>String(n)}}/>}
      empty={<SpaceBetween size="s"><span>{error?t('Resources are unavailable. Retry with Refresh.','リソースを取得できません。「更新」で再試行してください。'):items.length?t('No resources match your filter.','検索条件に一致するリソースがありません。'):t('No resources found in this region.','このリージョンには該当するリソースがありません。')}</span>{items.length>0&&<Button disabled={saving} onClick={()=>{setFilter('');setPage(1);}}>{t('Clear filter','絞り込みを解除')}</Button>}</SpaceBetween>}/>
    {!instance&&initialVpcId&&!loading&&!error&&!items.some(item=>item.id===initialVpcId)&&<Alert type="info">
      {t('This VPC was not found in the selected region.','選択したリージョンにはこの VPC がありません。')}</Alert>}
    {detail&&<Container header={<Header variant="h2" actions={<Button disabled={saving} onClick={()=>setSelected([])}>{t('Close details','詳細を閉じる')}</Button>}>{t('Resource details','リソースの詳細')}</Header>}>
      <VPCDetailsTabs key={'details|'+region+'|'+detail.id} vpc={instance?null:detail} region={region} request={request}
        capabilities={vpcDetailsCapabilities} authenticationRequired={vpcDetailsAuthenticationRequired}
        saving={saving} selectionLocked={selectionLocked}>
        <SpaceBetween size="l">{detailFields}{instance&&<EC2InstanceMetadata groupIds={detail.groupIds} tags={detail.tags}/>}
          {!instance&&vpcDetailsCapabilities&&<div inert={siblingBusy('dns')}>
            <VPCDNSSettings key={'dns|'+region+'|'+detail.id} vpc={detail} region={region} request={request}
              capabilities={vpcDetailsCapabilities} authenticationRequired={vpcDetailsAuthenticationRequired}
              writeBlocked={siblingBusy('dns')} acquireWrite={()=>acquireNetworkWrite('dns')}/>
          </div>}
          {!instance&&vpcTagEditingAvailable(vpcDetailsCapabilities,vpcDetailsAuthenticationRequired)&&<div inert={siblingBusy('tags')}>
            <VpcTagsPanel vpc={detail} region={region} request={request} writeBlocked={siblingBusy('tags')}
              acquireWrite={()=>acquireNetworkWrite('tags')} onConfirmed={snapshot=>{
                const name=snapshot.tags.find(tag=>tag.key==='Name')?.value??'';
                const replace=row=>row.id===snapshot.id&&row.managedBy==='ec2'?{...row,name}:row;
                setItems(rows=>rows.map(replace));setSelected(rows=>rows.map(replace));
              }}/>
          </div>}
          {!instance&&vpcNativeSubnets&&<div inert={siblingBusy('subnet')}><VPCNativeSubnets key={'subnet|'+region+'|'+detail.id} vpc={detail} region={region}
            request={request} onSaving={lease.subnet} publicIPv4={vpcNativeSubnetPublicIPv4}/></div>}
          {!instance&&vpcSecurityGroups&&<div inert={siblingBusy('sg')}><VPCSecurityGroups key={region+'|'+detail.id} vpc={detail} region={region} request={request}
            nativeLifecycle={vpcNativeSecurityGroups} nativeRules={vpcNativeSecurityGroupRules} writeBlocked={siblingBusy('sg')} acquireWrite={()=>acquireNetworkWrite('sg')}/></div>}
          {!instance&&vpcNetworkACLs&&<VPCNetworkACLs key={'nacl|'+region+'|'+detail.id} vpc={detail} region={region} request={request}
            writeBlocked={siblingBusy('acl')} onSaving={lease.acl}/>}
          {!instance&&nativeRoutePane&&<VPCNativeRouteTables key={'rtb-native|'+region+'|'+detail.id} vpc={detail} region={region} request={request}
            writeBlocked={siblingBusy('rtb')} acquireWrite={()=>acquireNetworkWrite('rtb')} associations={vpcNativeRouteAssociations} replacement={vpcNativeRouteReplacement} routeLimit={vpcNativeIPv4RouteLimit}/>}
          {!instance&&vpcRouteTables&&!nativeRoutePane&&<VPCRouteTables key={'rtb|'+region+'|'+detail.id} vpc={detail} region={region} request={request}/>}
        </SpaceBetween>
      </VPCDetailsTabs>
    </Container>}
    {!instance&&vpcInternetGateways&&<VPCInternetGateways request={request} region={region}
      writeBlocked={siblingBusy('igw')} acquireWrite={()=>acquireNetworkWrite('igw')}/>}
    {instance&&!sessionInstance&&instanceStackLaunchAllowed&&launchOpen&&<EC2InstanceStackLaunch
      region={region} request={request} onSaving={setBusy} onDismiss={()=>setLaunchOpen(false)} onConfirmed={id=>{setFilter('');void load(id);}}/>}
    {instance&&!sessionInstance&&preview&&<EC2TemplatePreview region={region} request={request} onDismiss={()=>setPreview(false)}/>}
    {manage&&<Modal visible={!!deleting} onDismiss={()=>{if(!busy)setDeleting(null);}} header={t('Delete VPC','VPC を削除')} closeAriaLabel={t('Close dialog','ダイアログを閉じる')}>
      <form onSubmit={remove}><Form errorText={deleteError} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={()=>setDeleting(null)}>{t('Cancel','キャンセル')}</Button><Button variant="primary" formAction="submit" loading={busy} disabled={confirmation!=='delete'||writeLeases.size>0}>{t('Delete','削除')}</Button></SpaceBetween>}>
        <SpaceBetween size="m"><Box>{t('Delete this VPC? This cannot be undone.','この VPC を削除しますか? この操作は元に戻せません。')}</Box><Box>{idCell(deleting?.id)}{deleting?.name?' ('+deleting.name+')':''}</Box>
          <FormField label={t('To confirm deletion, type delete in the field.','削除を確認するには、フィールドに delete と入力してください。')}><Input value={confirmation} placeholder="delete" disabled={busy} onChange={({detail})=>setConfirmation(detail.value)}/></FormField></SpaceBetween>
      </Form></form></Modal>}
  </SpaceBetween></ContentLayout>;
}
