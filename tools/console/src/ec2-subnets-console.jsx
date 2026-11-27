import React,{useEffect,useRef,useState} from 'react';
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
import Link from '@cloudscape-design/components/link';
import Pagination from '@cloudscape-design/components/pagination';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import TextFilter from '@cloudscape-design/components/text-filter';
import {useLocale} from './i18n.jsx';
import {createVpcNativeSubnet,loadRegionalSubnetInventory,nativeSubnetCIDR,nativeSubnetFailure} from './ec2-native-subnets.mjs';
import {loadEC2RouteTablePages} from './ec2-route-table-pages.mjs';
import SubnetNetworkACLPanel from './ec2-subnet-network-acl-panel.jsx';
import VpcTagsPanel from './ec2-vpc-tags-panel.jsx';
import {subnetConsoleHref,subnetRouteTable} from './ec2-subnet-console-route.mjs';

const shown=value=>value||'—';
const identifier=value=><span className="resource-identifier">{value}</span>;
const pageSize=20;
function vpcOption(vpc){return {value:vpc.id,label:vpc.name? vpc.name+' · '+vpc.id:vpc.id};}

function SubnetDetails({subnet,request,region,showRoutes,back,hrefForResource,subnetTags,subnetACLAssociation,acquireWrite,onConfirmed}) {
  const {t}=useLocale();
  const [changeBusy,setChangeBusy]=useState(false);
  const [routes,setRoutes]=useState({status:'loading'});
  const {id,vpc,owner:ownership}=subnet;
  useEffect(()=>{
    if(!showRoutes)return;
    const controller=new AbortController();
    loadEC2RouteTablePages(request,region,{signal:controller.signal})
      .then(({tables})=>{if(!controller.signal.aborted)setRoutes({status:'ready',association:subnetRouteTable(tables,{id,vpc,owner:ownership})});})
      .catch(()=>{if(!controller.signal.aborted)setRoutes({status:'error'});});
    return ()=>controller.abort();
  },[request,region,id,vpc,ownership,showRoutes]);
  const association=routes.association;
  function lease() {
    const release=acquireWrite();
    if(!release)return null;
    setChangeBusy(true);
    return ()=>{release();setChangeBusy(false);};
  }
  const owner=subnet.owner==='ec2'?'EC2':'CloudFormation';
  const ref=(kind,id)=>{const href=hrefForResource?.({kind,id,vpcId:subnet.vpc});
    return href?<Link href={href} onFollow={event=>{if(changeBusy)event.preventDefault();}}>{identifier(id)}</Link>:identifier(id);};
  return <SpaceBetween size="l">
    <Link href={back} onFollow={event=>{if(changeBusy)event.preventDefault();}}>{t('Back to subnets','サブネット一覧に戻る')}</Link>
    <Container header={<Header variant="h2">{t('Subnet details','サブネットの詳細')}</Header>}>
      <KeyValuePairs columns={2} minColumnWidth={180} items={[
        {label:t('Subnet ID','サブネット ID'),value:identifier(subnet.id)},
        {label:t('Name','名前'),value:shown(subnet.name)},
        {label:t('State','状態'),value:<StatusIndicator type="success">{t('Available','利用可能')}</StatusIndicator>},
        {label:'VPC ID',value:ref('vpc',subnet.vpc)},
        {label:'IPv4 CIDR',value:subnet.cidr},
        {label:t('Availability Zone','アベイラビリティーゾーン'),value:shown(subnet.az)},
        {label:t('Managed by','管理元'),value:owner},
        ...(typeof subnet.publicIPv4==='boolean'?[{label:t('Auto-assign public IPv4','パブリック IPv4 の自動割り当て'),
          value:subnet.publicIPv4?t('Enabled','有効'):t('Disabled','無効')}]:[]),
      ]}/>
    </Container>
    {showRoutes&&<Container header={<Header variant="h2">{t('Route table','ルートテーブル')}</Header>}>
      {routes.status==='loading'?<StatusIndicator type="loading">{t('Reading the subnet’s route table','サブネットのルートテーブルを読み込み中')}</StatusIndicator>
        :routes.status==='error'?<Alert type="error">{t('Could not confirm the route table. Refresh the page to try again.','ルートテーブルを確認できませんでした。ページを更新して再試行してください。')}</Alert>
        :association?<KeyValuePairs columns={2} minColumnWidth={180} items={[
          {label:t('Route table ID','ルートテーブル ID'),value:ref('route-table',association.id)},
          {label:t('Association','関連付け'),value:association.kind==='explicit'?t('Explicit subnet association','サブネットに明示的に関連付け済み'):t('Main route table (implicit)','メインルートテーブル（暗黙の関連付け）')},
        ]}/>:<Box>{t('No route-table association was confirmed for this subnet.','このサブネットのルートテーブルの関連付けは確認できませんでした。')}</Box>}
    </Container>}
    {subnetTags&&<Container><VpcTagsPanel subnet={subnet} request={request} region={region}
      acquireWrite={lease} writeBlocked={changeBusy} onConfirmed={onConfirmed}/></Container>}
    {subnetACLAssociation&&<SubnetNetworkACLPanel request={request} region={region} vpcId={vpc} subnetId={id} owner={ownership}
      interactive acquireWrite={lease} writeBlocked={changeBusy}/>}
    {subnetACLAssociation?<Box color="text-body-secondary">{t('Reserved addresses and guest connectivity are not provided by this view.',
      'この画面では予約アドレスとゲストの通信状態は表示しません。')}</Box>
      :<Box color="text-body-secondary">{t('Subnet ACL associations, reserved addresses and guest connectivity are not provided by this view.',
      'この画面ではサブネットの ACL 関連付け、予約アドレス、ゲストの通信状態は表示しません。')}</Box>}
  </SpaceBetween>;
}

export default function EC2SubnetsConsole({request,region,route,publicIPv4,showRoutes,onSaving,canCreate=true,hrefForResource,subnetTags=false,subnetACLAssociation=false,acquireWrite}) {
  const {t}=useLocale();
  const [snapshot,setSnapshot]=useState({status:'loading'}),[revision,setRevision]=useState(0);
  const [filter,setFilter]=useState(route.vpc),[text,setText]=useState(''),[page,setPage]=useState(1);
  const [selectedVpc,chooseVpc]=useState(route.vpc),[az,setAz]=useState(''),[cidr,setCidr]=useState('');
  const [busy,setBusy]=useState(false),[formError,setFormError]=useState('');
  const heading=useRef(null),operation=useRef(null);
  const scopeHash=useRef(location.hash);
  const creating=route.target==='create',detailID=creating?'':route.target;
  const listHref=subnetConsoleHref(region,'',route.vpc);
  const {vpcs=[],subnets=[]}=snapshot;
  const ready=snapshot.status==='ready', native=vpcs.filter(vpc=>vpc.managedBy==='ec2');
  const selected=vpcs.find(vpc=>vpc.id===selectedVpc);
  const createTarget=selected?.managedBy==='ec2'?selected:null;
  const canonical=createTarget&&nativeSubnetCIDR(createTarget,cidr);
  const neverReplay=formError==='unconfirmed';
  const zones=[{value:'',label:t('No preference','指定なし'),description:t('Use '+region+'a locally',region+'a を使用します')},
    ...['a','b','c'].map(suffix=>({value:region+suffix,label:region+suffix}))];
  const readyToSubmit=canCreate&&ready&&!busy&&!neverReplay&&!!canonical;
  const fieldText=cidr&&createTarget&&!canonical?t('Enter an IPv4 CIDR within this VPC.','この VPC の範囲内の IPv4 CIDR を入力してください。'):undefined;

  useEffect(()=>{heading.current?.focus();},[]);
  useEffect(()=>{
    const controller=new AbortController();
    setSnapshot({status:'loading'});
    loadRegionalSubnetInventory(request,region,{signal:controller.signal,publicIPv4})
      .then(current=>{if(!controller.signal.aborted)setSnapshot({status:'ready',...current});})
      .catch(error=>{if(!controller.signal.aborted)setSnapshot({status:nativeSubnetFailure(error)});});
    return ()=>controller.abort();
  },[request,region,publicIPv4,revision]);
  useEffect(()=>()=>{if(operation.current){operation.current.abort();operation.current=null;onSaving(false);}},[onSaving]);

  async function submit(event) {
    event.preventDefault();
    if(!readyToSubmit||operation.current)return;
    const controller=new AbortController();
    operation.current=controller;setBusy(true);setFormError('');onSaving(true);
    const current=()=>operation.current===controller&&!controller.signal.aborted&&location.hash===scopeHash.current;
    try {
      const {created}=await createVpcNativeSubnet(request,region,createTarget,canonical,az||region+'a',{signal:controller.signal,publicIPv4});
      if(current()) location.replace(subnetConsoleHref(region,created.id,createTarget.id));
    } catch(error) {
      if(current()) setFormError(nativeSubnetFailure(error));
    } finally {
      if(operation.current===controller){operation.current=null;setBusy(false);onSaving(false);}
    }
  }
  function edit(change,value){if(!busy&&!neverReplay){change(value);setFormError('');}}
  const errors={
    denied:t('The current identity is not allowed to perform this subnet operation.','現在の ID ではサブネットを操作する権限がありません。'),
    'response-too-large':t('The regional subnet reply exceeds the Console’s 16 MiB limit. Retrieve the regional inventory with the native SDK; the Console’s VPC filter does not reduce this response.',
      'リージョンのサブネット応答がコンソールの 16 MiB 上限を超えています。ネイティブ SDK でリージョンの一覧を取得してください。コンソールの VPC 絞り込みでは応答は小さくなりません。'),
    overlap:t('This IPv4 range overlaps an existing subnet in the selected VPC.','この IPv4 CIDR は、選択した VPC の既存サブネットと重複します。'),
    unconfirmed:t('Could not confirm what was saved. Return to Subnets and inspect the inventory before trying again.',
      '保存結果を確認できませんでした。再試行する前にサブネット一覧で状態を確認してください。'),
  };
  const statusError=snapshot.status!=='loading'&&!ready
    ? errors[snapshot.status]&&snapshot.status!=='unconfirmed'?errors[snapshot.status]:t('Could not confirm the regional subnet inventory.','このリージョンのサブネット一覧を確認できませんでした。'):null;
  const subtitle=creating?t('Add one IPv4 subnet to a local EC2-managed VPC.', 'EC2 が管理するローカル VPC に IPv4 サブネットを 1 つ追加します。')
    :!detailID?t('Saved IPv4 subnet configuration in this region, not a connectivity check.','このリージョンの保存済み IPv4 サブネット設定です。通信状態を確認するものではありません。'):undefined;
  const contentHeading=creating?t('Create subnet','サブネットを作成'):detailID||t('Subnets','サブネット');
  const createHref=subnetConsoleHref(region,'create',filter);
  const detail=ready&&subnets.find(row=>row.id===detailID&&(!route.vpc||row.vpc===route.vpc));
  const vpcOptions=[{value:'',label:t('All VPCs','すべての VPC')},...vpcs.map(vpcOption)];
  if (ready&&filter&&!vpcs.some(vpc=>vpc.id===filter)) vpcOptions.push({value:filter,label:filter,
    description:t('Not found in this region','このリージョンにはありません')});
  const query=text.trim().toLowerCase(), filtered=subnets.filter(subnet=>(!filter||subnet.vpc===filter)&&
    [subnet.id,subnet.name,subnet.vpc,subnet.cidr,subnet.az,subnet.state,t('Available','利用可能')]
      .some(value=>value?.toLowerCase().includes(query)));
  const refresh=<Button disabled={busy} loading={snapshot.status==='loading'} iconName="refresh"
    onClick={()=>{setPage(1);setRevision(r=>r+1);}}>{t('Refresh subnets','サブネットを更新')}</Button>;
  return <ContentLayout header={<div ref={heading} tabIndex={-1}><Header variant="h1" description={subtitle}>{contentHeading}</Header></div>}>
    {creating&&!canCreate?<Alert type="info" action={<Button href={listHref}>{t('Back to subnets','サブネット一覧に戻る')}</Button>}>
      {t('Subnet creation is unavailable in this session.','このセッションではサブネットを作成できません。')}</Alert>:creating?<form onSubmit={submit} data-testid="subnet-create-form"><Form actions={<SpaceBetween direction="horizontal" size="xs">
      <Button href={listHref} disabled={busy} formAction="none">{t('Cancel','キャンセル')}</Button>
      <Button variant="primary" formAction="submit" disabled={!readyToSubmit} loading={busy}>{t('Create subnet','サブネットを作成')}</Button>
    </SpaceBetween>}>
      <SpaceBetween size="l">
        {statusError&&<Alert type="error" action={refresh}>{statusError}</Alert>}
        {formError&&<Alert type="error">{errors[formError]||errors.unconfirmed}</Alert>}
        <Container header={<Header variant="h2">{t('VPC','VPC')}</Header>}>
          <SpaceBetween size="s">
            <FormField label="VPC ID" description={t('Select the native VPC for this subnet. CloudFormation VPCs are read-only here.',
              'このサブネットを追加する EC2 管理の VPC を選択します。CloudFormation 管理の VPC は読み取り専用です。')}>
              <Select selectedOption={selected?vpcOption(selected):null} filteringType="auto" placeholder={t('Select a VPC','VPC を選択')}
                statusType={snapshot.status==='loading'?'loading':'finished'} disabled={!ready||busy||neverReplay}
                options={vpcs.map(vpc=>({...vpcOption(vpc),disabled:vpc.managedBy!=='ec2',description:vpc.cidr}))}
                onChange={({detail})=>edit(chooseVpc,detail.selectedOption.value)}/>
            </FormField>
            {selected&&<KeyValuePairs columns={1} items={[{label:t('VPC IPv4 CIDR','VPC の IPv4 CIDR'),value:selected.cidr}]}/>}
            {ready&&!native.length&&<Alert type="info">{t('No EC2-managed VPC is available. Create a VPC first.','EC2 管理の VPC がありません。先に VPC を作成してください。')}</Alert>}
          </SpaceBetween>
        </Container>
        <Container header={<Header variant="h2">{t('Subnet settings','サブネットの設定')}</Header>}>
          <SpaceBetween size="l">
            {subnetTags?<Box color="text-body-secondary">
              {t('After creation, open the subnet to set its Name tag.','作成後にサブネットを開き、Name タグを設定できます。')}
            </Box>:<FormField label={t('Subnet name - optional','サブネット名 - オプション')}
              constraintText={t('The native simulator cannot save subnet names or tags.','ネイティブのシミュレーターではサブネット名やタグを保存できません。')}>
              <Input value="" disabled/>
            </FormField>}
            <FormField label={t('Availability Zone','アベイラビリティーゾーン')}>
              <Select selectedOption={zones.find(option=>option.value===az)} options={zones} disabled={!createTarget||busy||neverReplay}
                onChange={({detail})=>edit(setAz,detail.selectedOption.value)}/>
            </FormField>
            <FormField label={t('IPv4 subnet CIDR block','IPv4 サブネット CIDR ブロック')} errorText={fieldText}
              constraintText={t('Use a /16–/28 range within the selected VPC.', '選択した VPC 内の /16〜/28 の範囲を指定します。')}>
              <Input value={cidr} placeholder="10.0.1.0/24" disabled={!createTarget||busy||neverReplay} onChange={({detail})=>edit(setCidr,detail.value)}/>
            </FormField>
          </SpaceBetween>
        </Container>
      </SpaceBetween>
    </Form></form>:detailID?<SpaceBetween size="l">
      {statusError?<Alert type="error" action={refresh}>{statusError}</Alert>
        :snapshot.status==='loading'?<StatusIndicator type="loading">{t('Loading subnet','サブネットを読み込み中')}</StatusIndicator>
        :!detail?<Alert type="warning" action={<Button href={listHref}>{t('Back to subnets','サブネット一覧に戻る')}</Button>}>
          {t('This subnet was not found in the selected region.','選択したリージョンにはこのサブネットがありません。')}</Alert>
        :<SubnetDetails key={detail.id} subnet={detail} request={request} region={region} showRoutes={showRoutes} back={listHref} hrefForResource={hrefForResource}
          subnetTags={subnetTags} subnetACLAssociation={subnetACLAssociation} acquireWrite={acquireWrite}
          onConfirmed={saved=>setSnapshot(prev=>prev.status!=='ready'?prev:{...prev,subnets:prev.subnets.map(row=>
            row.id===detail.id&&row.vpc===detail.vpc&&row.name!==saved.name?{...row,name:saved.name}:row)})}/>}
    </SpaceBetween>:<SpaceBetween size="l">
      {statusError&&<Alert type="error">{statusError}</Alert>}
      <Table trackBy="id" items={filtered.slice((page-1)*pageSize,page*pageSize)} loading={snapshot.status==='loading'}
        loadingText={t('Loading subnets','サブネットを読み込み中')} ariaLabels={{tableLabel:t('Subnets','サブネット')}}
        header={<Header counter={'('+filtered.length+')'} actions={<SpaceBetween direction="horizontal" size="xs">
          {refresh}
          {canCreate&&<Button variant="primary" href={createHref} disabled={!ready||!native.length}
            disabledReason={!native.length&&ready?t('Create an EC2-managed VPC first.','先に EC2 管理の VPC を作成してください。'):undefined}>
            {t('Create subnet','サブネットを作成')}</Button>}
        </SpaceBetween>}>{t('Subnets','サブネット')}</Header>}
        filter={<SpaceBetween direction="vertical" size="xs">
          <FormField label={t('Filter by VPC','VPC で絞り込む')}>
            <Select selectedOption={vpcOptions.find(option=>option.value===filter)||vpcOptions[0]} options={vpcOptions}
              disabled={!ready} onChange={({detail})=>{setFilter(detail.selectedOption.value);setPage(1);}}/>
          </FormField>
          <TextFilter filteringText={text} disabled={!ready} filteringAriaLabel={t('Search subnets','サブネットを検索')}
            filteringPlaceholder={t('Search by name, subnet ID or VPC','名前、サブネット ID、VPC で検索')}
            onChange={({detail})=>{setText(detail.filteringText);setPage(1);}}/>
        </SpaceBetween>}
        pagination={<Pagination currentPageIndex={page} pagesCount={Math.max(1,Math.ceil(filtered.length/pageSize))}
          onChange={({detail})=>setPage(detail.currentPageIndex)}/>}
        columnDefinitions={[
          {id:'name',header:t('Name','名前'),cell:row=>shown(row.name)},
          {id:'id',header:t('Subnet ID','サブネット ID'),cell:row=><Link href={subnetConsoleHref(region,row.id,filter)}>{row.id}</Link>},
          {id:'state',header:t('State','状態'),cell:()=><StatusIndicator type="success">{t('Available','利用可能')}</StatusIndicator>},
          {id:'vpc',header:'VPC ID',cell:row=>identifier(row.vpc)},
          {id:'cidr',header:'IPv4 CIDR',cell:row=>row.cidr},
          {id:'zone',header:t('Availability Zone','アベイラビリティーゾーン'),cell:row=>shown(row.az)},
        ]}
        empty={ready?(filter||text?t('No saved subnets match your filters.','絞り込み条件に一致するサブネットはありません。'):
          t('No subnets are saved in this region.','このリージョンに保存済みのサブネットはありません。')):
          t('Subnets are unavailable until the inventory can be loaded.','一覧を確認できるまでサブネットは表示できません。')}/>
    </SpaceBetween>}
  </ContentLayout>;
}
