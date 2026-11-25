import React, {useEffect, useRef, useState} from 'react';
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
import RadioGroup from '@cloudscape-design/components/radio-group';
import Select from '@cloudscape-design/components/select';
import Modal from '@cloudscape-design/components/modal';
import Box from '@cloudscape-design/components/box';
import Link from '@cloudscape-design/components/link';
import {useLocale} from './i18n.jsx';

import {parseEC2Document,describeAction,parseCreateVpc,parseDeleteVpc,parseCreateSubnet,parseDeleteSubnet,parseRunInstances,parseTerminateInstances,parseEC2Error,createVpcParameters,createSubnetParameters,runInstancesParameters,VPC_NAME_LIMIT,INSTANCE_TYPES} from './ec2-response.mjs';

const idCell=value=><span className="resource-identifier">{value||'—'}</span>;
const blank={name:'',cidr:'',vpc:'',zone:'',image:'',type:INSTANCE_TYPES[0],subnet:'',token:''};
// A fresh token per form state: resubmitting an unchanged form after a lost
// response returns the record it already created instead of a second one.
const launchToken=()=>globalThis.crypto?.randomUUID?.()??'';

// kind is 'ec2' (instances), 'vpc' or 'subnets'. Create and delete controls
// appear only when the server advertises the native operations, and only
// EC2-owned resources can be deleted here. A launch record has no guest: never
// infer a running instance from it or from stack metadata.
export default function EC2Readonly({kind,region,request,sessionInstance,onSession,vpcLifecycle=false,subnetLifecycle=false,instanceLifecycle=false,onSaving=()=>{}}) {
  const {t}=useLocale();
  const instance=kind==='ec2',subnets=kind==='subnets',manage=instance?instanceLifecycle:subnets?subnetLifecycle:vpcLifecycle;
  const [items,setItems]=useState([]),[loading,setLoading]=useState(false),[error,setError]=useState('');
  const [selected,setSelected]=useState([]),[filter,setFilter]=useState(''),[page,setPage]=useState(1);
  const [view,setView]=useState('list'),[form,setFormState]=useState(blank),[busy,setBusy]=useState(false),[formError,setFormError]=useState(''),[notice,setNotice]=useState('');
  const [deleting,setDeleting]=useState(null),[confirmation,setConfirmation]=useState(''),[deleteError,setDeleteError]=useState('');
  const [parents,setParents]=useState({vpcs:[],subnets:[],loading:false,error:''});
  const epoch=useRef(0),parentEpoch=useRef(0),alive=useRef(true);
  useEffect(()=>{alive.current=true;return()=>{alive.current=false;epoch.current++;parentEpoch.current++;onSaving(false);};},[]);
  useEffect(()=>{onSaving(busy);},[busy]);
  const setForm=changes=>setFormState(old=>({...old,...changes,token:launchToken()}));
  const headers={'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region};
  async function describe(target){
    const response=await request('/',{method:'POST',headers,body:new URLSearchParams({Action:describeAction(target),Version:'2016-11-15'})});
    const xml=new DOMParser().parseFromString(await response.text(),'application/xml');
    if(!response.ok)throw Error(t('Could not retrieve resources.','リソースを取得できませんでした。'));
    return parseEC2Document(xml,target);
  }
  async function load(select){
    const current=++epoch.current;setLoading(true);setError('');setItems([]);setSelected([]);setPage(1);
    try {
      const rows=await describe(kind);
      if(alive.current&&current===epoch.current){setItems(rows);setSelected(rows.filter(row=>row.id===select));}
    }catch(e){if(alive.current&&current===epoch.current)setError(e.message);}
    finally{if(alive.current&&current===epoch.current)setLoading(false);}
  }
  useEffect(()=>{load();},[kind,region]);
  // The create forms choose among EC2-owned parents only; the server refuses
  // CloudFormation-owned ones, so they are not offered.
  async function openCreate(){
    setFormState({...blank,token:launchToken()});setFormError('');setNotice('');setView('create');
    if(!instance&&!subnets)return;
    const current=++parentEpoch.current;setParents({vpcs:[],subnets:[],loading:true,error:''});
    try {
      const vpcs=(await describe('vpc')).filter(row=>row.managedBy==='ec2');
      const owned=instance?(await describe('subnets')).filter(row=>row.managedBy==='ec2'):[];
      if(alive.current&&current===parentEpoch.current)setParents({vpcs,subnets:owned,loading:false,error:''});
    }catch(e){if(alive.current&&current===parentEpoch.current)setParents({vpcs:[],subnets:[],loading:false,error:e.message});}
  }
  // A refused mutation reports the server's EC2 error code; it is never shown as success.
  // A request with no answer may or may not have been applied: say so rather
  // than showing the browser's own transport message.
  async function mutate(body){
    let response;
    try{response=await request('/',{method:'POST',headers,body});}
    catch(e){if(e instanceof TypeError)throw Error(t('No response from the server. The request may or may not have been applied; check the list before trying again.','サーバーから応答がありませんでした。リクエストが反映されたかどうかは不明です。再試行する前に一覧を確認してください。'));throw e;}
    const xml=new DOMParser().parseFromString(await response.text(),'application/xml');
    if(!response.ok){const failure=parseEC2Error(xml);throw Error(failure?failure.code+': '+failure.message:t('The request failed.','リクエストに失敗しました。'));}
    return xml;
  }
  function invalidForm(){
    if(form.name.length>VPC_NAME_LIMIT)return t('The Name tag can have up to 256 characters.','名前タグは 256 文字以内で入力してください。');
    if(instance){
      if(!form.image.trim())return t('Enter an AMI ID, such as ami-0123456789abcdef0.','AMI ID を入力してください (例: ami-0123456789abcdef0)。');
      if(!form.subnet)return t('Choose a subnet.','サブネットを選択してください。');
      return '';
    }
    if(subnets&&!form.vpc)return t('Choose a VPC.','VPC を選択してください。');
    if(!form.cidr.trim())return subnets?t('Enter an IPv4 CIDR block inside the VPC, such as 10.0.1.0/24.','VPC の範囲内の IPv4 CIDR ブロックを入力してください (例: 10.0.1.0/24)。'):t('Enter an IPv4 CIDR block, such as 10.0.0.0/16.','IPv4 CIDR ブロックを入力してください (例: 10.0.0.0/16)。');
    return '';
  }
  async function create(event){
    event.preventDefault();if(busy)return;
    const problem=invalidForm();
    if(problem){setFormError(problem);return;}
    setBusy(true);setFormError('');
    try {
      const made=instance?parseRunInstances(await mutate(runInstancesParameters(form))):subnets?parseCreateSubnet(await mutate(createSubnetParameters(form))):parseCreateVpc(await mutate(createVpcParameters(form)));
      if(!alive.current)return;
      setView('list');setFormState(blank);setFilter('');
      setNotice((instance?t('Saved the launch record ','起動記録を保存しました: '):subnets?t('You successfully created ','サブネットを作成しました: '):t('You successfully created ','VPC を作成しました: '))+made.id);
      await load(made.id);
    }catch(e){if(alive.current)setFormError(e.message);}
    finally{if(alive.current)setBusy(false);}
  }
  async function remove(event){
    event.preventDefault();if(busy||!deleting)return;
    if(!instance&&confirmation!=='delete'){setDeleteError(t('Type delete to confirm.','確認のため delete と入力してください。'));return;}
    setBusy(true);setDeleteError('');
    try {
      if(instance)parseTerminateInstances(await mutate(new URLSearchParams({Action:'TerminateInstances',Version:'2016-11-15','InstanceId.1':deleting.id})),deleting.id);
      else if(subnets)parseDeleteSubnet(await mutate(new URLSearchParams({Action:'DeleteSubnet',Version:'2016-11-15',SubnetId:deleting.id})));
      else parseDeleteVpc(await mutate(new URLSearchParams({Action:'DeleteVpc',Version:'2016-11-15',VpcId:deleting.id})));
      if(!alive.current)return;
      setNotice((instance?t('Terminated ','インスタンスを終了しました: '):subnets?t('Deleted ','サブネットを削除しました: '):t('Deleted ','VPC を削除しました: '))+deleting.id);setDeleting(null);
      await load();
    }catch(e){if(alive.current)setDeleteError(e.message);}
    finally{if(alive.current)setBusy(false);}
  }
  const managedLabel=value=>value==='ec2'?'EC2':value==='cloudformation'?'CloudFormation':t('Unconfirmed','未確認');
  const stateLabel=value=>value==='not_launched'?t('No guest (launch record only)','ゲストなし (起動記録のみ)'):!value||value==='unknown'?t('Unconfirmed','未確認'):value;
  // The table shows the first `columns` fields; the details panel shows all.
  const fields=instance?[['id','Instance ID','インスタンスID'],['name','Name','名前'],['state','Recorded runtime state','記録されたランタイム状態'],['type','Instance type','インスタンスタイプ'],['managedBy','Managed by','管理元'],['vpc','VPC ID','VPC ID'],['subnet','Subnet ID','サブネットID'],['image','Image ID','イメージID']]
    :subnets?[['id','Subnet ID','サブネットID'],['name','Name','名前'],['vpc','VPC ID','VPC ID'],['cidr','IPv4 CIDR','IPv4 CIDR'],['managedBy','Managed by','管理元'],['zone','Availability Zone','アベイラビリティーゾーン']]
    :[['id','VPC ID','VPC ID'],['name','Name','名前'],['cidr','IPv4 CIDR','IPv4 CIDR'],['managedBy','Managed by','管理元']];
  const columns=instance||subnets?6:4;
  const display=(key,item)=>key==='state'?stateLabel(item[key]):key==='managedBy'?managedLabel(item[key]):item[key];
  const filtered=items.filter(item=>Object.values(item).some(v=>String(v).toLocaleLowerCase().includes(filter.toLocaleLowerCase())));
  const detail=selected[0];
  const unavailable=t('Not available in this simulator.','このシミュレーターでは利用できません。');
  const createText=instance?t('Launch instances','インスタンスを起動'):subnets?t('Create subnet','サブネットを作成'):t('Create VPC','VPC を作成');
  const deleteText=instance?t('Terminate (delete) instance','インスタンスを終了 (削除)'):subnets?t('Delete subnet','サブネットを削除'):t('Delete VPC','VPC を削除');
  const stackOnly=instance?t('Delete CloudFormation-managed instances by deleting their stack.','CloudFormation で管理されるインスタンスは、スタックを削除して削除してください。'):subnets?t('Delete CloudFormation-managed subnets by deleting their stack.','CloudFormation で管理されるサブネットは、スタックを削除して削除してください。'):t('Delete CloudFormation-managed VPCs by deleting their stack.','CloudFormation で管理される VPC は、スタックを削除して削除してください。');
  const option=(row,extra)=>({value:row.id,label:row.id,description:[row.name,extra].filter(Boolean).join(' · ')});
  const vpcOptions=parents.vpcs.map(row=>option(row,row.cidr));
  const chosenVpc=parents.vpcs.find(row=>row.id===form.vpc);
  const subnetOptions=parents.subnets.filter(row=>row.vpc===form.vpc).map(row=>option(row,[row.cidr,row.zone].filter(Boolean).join(' · ')));
  const picked=(options,value)=>options.find(o=>o.value===value)??null;
  const parentStatus=parents.loading?'loading':parents.error?'error':'finished';
  // Nothing to choose yet: say where to create it instead of an empty list.
  const parentsReady=!parents.loading&&!parents.error;
  const missingParent=!parentsReady?null:parents.vpcs.length===0?<Alert type="warning" header={t('Create a VPC first','先に VPC を作成してください')}>{t('This region has no VPC created in this console. ','このリージョンには、このコンソールで作成した VPC がありません。')}<Link href="#/vpc">{t('Open VPC','VPC を開く')}</Link></Alert>
    :instance&&parents.subnets.length===0?<Alert type="warning" header={t('Create a subnet first','先にサブネットを作成してください')}>{t('This region has no subnet created in this console. ','このリージョンには、このコンソールで作成したサブネットがありません。')}<Link href="#/subnets">{t('Open Subnets','サブネットを開く')}</Link></Alert>:null;
  // A failed submit may still have been applied (a lost response), so leaving
  // the form after an error reloads the list instead of showing a stale one.
  const cancel=<Button formAction="none" disabled={busy} onClick={()=>{const failed=!!formError;setView('list');setFormError('');if(failed)load();}}>{t('Cancel','キャンセル')}</Button>;
  const nameField=<FormField label={instance?t('Name - optional','名前 - オプション'):subnets?t('Subnet name - optional','サブネット名 - オプション'):t('Name tag - optional','名前タグ - オプション')} description={t("Creates a tag with a key of 'Name' and a value that you specify.","キーが「Name」で、指定した値を持つタグを作成します。")} constraintText={t('Up to 256 characters.','256 文字以内。')}><Input value={form.name} disabled={busy} placeholder={instance?'my-web-server':subnets?'my-subnet-01':'my-vpc-01'} onChange={({detail})=>setForm({name:detail.value})}/></FormField>;
  const vpcField=<FormField label="VPC ID" description={subnets?t('Create a subnet in this VPC.','この VPC にサブネットを作成します。'):t('Only VPCs created in this console or with CreateVpc are listed.','このコンソールまたは CreateVpc で作成した VPC のみ表示されます。')} errorText={parents.error||undefined}>
    <Select selectedOption={picked(vpcOptions,form.vpc)} options={vpcOptions} disabled={busy} statusType={parentStatus} loadingText={t('Loading VPCs','VPC を読み込み中')} placeholder={t('Select a VPC','VPC を選択')} empty={t('No EC2-managed VPC in this region. Create one under VPC first.','このリージョンには EC2 管理の VPC がありません。先に VPC で作成してください。')} onChange={({detail})=>setForm({vpc:detail.selectedOption.value,subnet:''})}/></FormField>;
  if(manage&&view==='create'&&instance)return <ContentLayout header={<Header variant="h1" description={t('Save a launch record for one instance. No virtual machine is started in this local simulator.','1 台のインスタンスの起動記録を保存します。このローカルシミュレーターでは仮想マシンは起動しません。')}>{t('Launch an instance','インスタンスを起動')}</Header>}>
    <form onSubmit={create}><Form errorText={formError} actions={<SpaceBetween direction="horizontal" size="xs">{cancel}<Button variant="primary" formAction="submit" loading={busy}>{t('Launch instance','インスタンスを起動')}</Button></SpaceBetween>}>
      <SpaceBetween size="l">
        <Alert type="info" header={t('A launch record, not a running server','稼働するサーバーではなく起動記録です')}>{t('The instance is saved with its image ID, type, subnet and Name tag and appears in the list. No guest, IP address, disk or metadata service is created, and it never becomes running. Instances that run a real guest come from a CloudFormation stack on a simulator started with a Battle runtime.','インスタンスはイメージ ID、タイプ、サブネット、名前タグとともに保存され、一覧に表示されます。ゲスト、IP アドレス、ディスク、メタデータサービスは作成されず、実行中にはなりません。実際にゲストが動くインスタンスは、Battle ランタイム付きで起動したシミュレーターの CloudFormation スタックから作成します。')}</Alert>
        <Container header={<Header variant="h2">{t('Name and tags','名前とタグ')}</Header>}>{nameField}</Container>
        <Container header={<Header variant="h2">{t('Application and OS Images (Amazon Machine Image)','アプリケーションおよび OS イメージ (Amazon マシンイメージ)')}</Header>}>
          <FormField label={t('AMI ID','AMI ID')} description={t('Only the format is checked. There is no image catalog, and no image is looked up or downloaded.','形式のみ検証します。イメージのカタログはなく、イメージの検索やダウンロードは行いません。')} constraintText={t('ami- followed by 8 or 17 hexadecimal characters.','ami- に続く 8 桁または 17 桁の 16 進数。')}><Input value={form.image} disabled={busy} placeholder="ami-0123456789abcdef0" onChange={({detail})=>setForm({image:detail.value})}/></FormField></Container>
        <Container header={<Header variant="h2">{t('Instance type','インスタンスタイプ')}</Header>}>
          <FormField label={t('Instance type','インスタンスタイプ')} constraintText={t('Only t3.micro and t3.small are recorded.','記録できるのは t3.micro と t3.small のみです。')}><Select selectedOption={{value:form.type,label:form.type}} disabled={busy} options={INSTANCE_TYPES.map(value=>({value,label:value}))} onChange={({detail})=>setForm({type:detail.selectedOption.value})}/></FormField></Container>
        <Container header={<Header variant="h2">{t('Key pair (login)','キーペア (ログイン)')}</Header>}>
          <FormField label={t('Key pair name','キーペア名')}><Select selectedOption={{value:'none',label:t('Proceed without a key pair','キーペアなしで続行')}} onChange={()=>{}} options={[{value:'none',label:t('Proceed without a key pair','キーペアなしで続行')},{value:'create',label:t('Create new key pair','新しいキーペアの作成'),disabled:true,description:unavailable}]}/></FormField></Container>
        <Container header={<Header variant="h2">{t('Network settings','ネットワーク設定')}</Header>}><SpaceBetween size="l">
          {missingParent}
          {vpcField}
          <FormField label={t('Subnet','サブネット')} description={t('Only subnets created in this console or with CreateSubnet are listed.','このコンソールまたは CreateSubnet で作成したサブネットのみ表示されます。')}>
            <Select selectedOption={picked(subnetOptions,form.subnet)} options={subnetOptions} disabled={busy||!form.vpc} statusType={parentStatus} loadingText={t('Loading subnets','サブネットを読み込み中')} placeholder={t('Select a subnet','サブネットを選択')} empty={t('No EC2-managed subnet in this VPC. Create one under VPC, Subnets first.','この VPC には EC2 管理のサブネットがありません。先に VPC のサブネットで作成してください。')} onChange={({detail})=>setForm({subnet:detail.selectedOption.value})}/></FormField>
          <FormField label={t('Firewall (security groups)','ファイアウォール (セキュリティグループ)')}><RadioGroup value="none" onChange={()=>{}} items={[{value:'none',label:t('No security group','セキュリティグループなし'),description:t('A launch record has no network path to protect.','起動記録には保護するネットワーク経路がありません。')},{value:'create',label:t('Create security group','セキュリティグループを作成'),disabled:true,description:unavailable},{value:'existing',label:t('Select existing security group','既存のセキュリティグループを選択する'),disabled:true,description:unavailable}]}/></FormField>
        </SpaceBetween></Container>
        <Container header={<Header variant="h2">{t('Summary','概要')}</Header>}><KeyValuePairs columns={2} items={[{label:t('Number of instances','インスタンス数'),value:'1'},{label:t('Other settings','その他の設定'),value:t('Storage, user data and IAM instance profiles are not available in this simulator.','ストレージ、ユーザーデータ、IAM インスタンスプロファイルはこのシミュレーターでは利用できません。')}]}/></Container>
      </SpaceBetween>
    </Form></form></ContentLayout>;
  if(manage&&view==='create'&&subnets)return <ContentLayout header={<Header variant="h1" description={t('Create a subnet inside a VPC that you created. It is saved in this local simulator only.','作成済みの VPC の中にサブネットを作成します。このローカルシミュレーターにのみ保存されます。')}>{t('Create subnet','サブネットを作成')}</Header>}>
    <form onSubmit={create}><Form errorText={formError} actions={<SpaceBetween direction="horizontal" size="xs">{cancel}<Button variant="primary" formAction="submit" loading={busy}>{t('Create subnet','サブネットを作成')}</Button></SpaceBetween>}>
      <SpaceBetween size="l"><Container header={<Header variant="h2">VPC</Header>}><SpaceBetween size="l">{missingParent}{vpcField}</SpaceBetween></Container>
      <Container header={<Header variant="h2">{t('Subnet settings','サブネットの設定')}</Header>}><SpaceBetween size="l">
        {nameField}
        <FormField label={t('Availability Zone','アベイラビリティーゾーン')}><Select selectedOption={form.zone?{value:form.zone,label:form.zone}:{value:'',label:t('No preference','指定なし')}} disabled={busy} options={[{value:'',label:t('No preference','指定なし')},...['a','b','c'].map(letter=>({value:region+letter,label:region+letter}))]} onChange={({detail})=>setForm({zone:detail.selectedOption.value})}/></FormField>
        <FormField label={t('IPv4 VPC CIDR block','IPv4 VPC CIDR ブロック')}>{idCell(chosenVpc?.cidr)}</FormField>
        <FormField label={t('IPv4 subnet CIDR block','IPv4 サブネット CIDR ブロック')} constraintText={t('Inside the VPC CIDR, between /16 and /28, without overlapping another subnet of the VPC.','VPC CIDR の範囲内で /16 から /28 の間。同じ VPC の他のサブネットと重複できません。')}><Input value={form.cidr} disabled={busy} placeholder="10.0.1.0/24" onChange={({detail})=>setForm({cidr:detail.value})}/></FormField>
      </SpaceBetween></Container>
      <Alert type="info">{t('Only the subnet itself is created. Route tables, internet gateways, IPv6, public IP assignment and additional tags are not available in this console.','作成されるのはサブネット本体のみです。ルートテーブル、インターネットゲートウェイ、IPv6、パブリック IP の割り当て、追加のタグはこのコンソールでは利用できません。')}</Alert></SpaceBetween>
    </Form></form></ContentLayout>;
  if(manage&&view==='create')return <ContentLayout header={<Header variant="h1" description={t('Create an isolated virtual network with an IPv4 CIDR block. It is saved in this local simulator only.','IPv4 CIDR ブロックを持つ、分離された仮想ネットワークを作成します。このローカルシミュレーターにのみ保存されます。')}>{t('Create VPC','VPC を作成')}</Header>}>
    <form onSubmit={create}><Form errorText={formError} actions={<SpaceBetween direction="horizontal" size="xs">{cancel}<Button variant="primary" formAction="submit" loading={busy}>{t('Create VPC','VPC を作成')}</Button></SpaceBetween>}>
      <SpaceBetween size="l"><Container header={<Header variant="h2">{t('VPC settings','VPC の設定')}</Header>}><SpaceBetween size="l">
        <FormField label={t('Resources to create','作成するリソース')}><RadioGroup value="vpc-only" onChange={()=>{}} items={[{value:'vpc-only',label:t('VPC only','VPC のみ')},{value:'vpc-and-more',label:t('VPC and more','VPC など'),disabled:true,description:unavailable}]}/></FormField>
        {nameField}
        <FormField label={t('IPv4 CIDR block','IPv4 CIDR ブロック')}><RadioGroup value="manual" onChange={()=>{}} items={[{value:'manual',label:t('IPv4 CIDR manual input','IPv4 CIDR の手動入力')},{value:'ipam',label:t('IPAM-allocated IPv4 CIDR block','IPAM で割り当てられた IPv4 CIDR ブロック'),disabled:true,description:unavailable}]}/></FormField>
        <FormField label={t('IPv4 CIDR','IPv4 CIDR')} constraintText={t('CIDR block size must be between /16 and /28.','CIDR ブロックのサイズは /16 から /28 の間である必要があります。')}><Input value={form.cidr} disabled={busy} placeholder="10.0.0.0/16" onChange={({detail})=>setForm({cidr:detail.value})}/></FormField>
        <FormField label={t('IPv6 CIDR block','IPv6 CIDR ブロック')}><RadioGroup value="none" onChange={()=>{}} items={[{value:'none',label:t('No IPv6 CIDR block','IPv6 CIDR ブロックなし')},{value:'amazon',label:t('Amazon-provided IPv6 CIDR block','Amazon 提供の IPv6 CIDR ブロック'),disabled:true,description:unavailable}]}/></FormField>
        <FormField label={t('Tenancy','テナンシー')}><Select selectedOption={{value:'default',label:t('Default','デフォルト')}} onChange={()=>{}} options={[{value:'default',label:t('Default','デフォルト')},{value:'dedicated',label:t('Dedicated','専有'),disabled:true,description:unavailable}]}/></FormField>
      </SpaceBetween></Container>
      <Alert type="info">{t('Only the VPC itself is created. Add subnets under Subnets. Route tables, internet gateways, IPv6, IPAM, DNS settings and additional tags are not available in this console.','作成されるのは VPC 本体のみです。サブネットは「サブネット」から追加します。ルートテーブル、インターネットゲートウェイ、IPv6、IPAM、DNS 設定、追加のタグはこのコンソールでは利用できません。')}</Alert></SpaceBetween>
    </Form></form></ContentLayout>;
  const deletable=detail?.managedBy==='ec2';
  const banner=instance?(manage?t('Instances that you launch here are saved as launch records: no guest runs, and they can be terminated here. Instances from a CloudFormation stack are read-only here; delete their stack to remove them. Start, stop and network edits are not supported. Reported state is not a health check of the guest.','この画面で起動したインスタンスは起動記録として保存されます。ゲストは動作せず、この画面から終了できます。CloudFormation スタックのインスタンスはこの画面では読み取り専用で、スタックを削除して削除します。開始・停止・ネットワーク編集はできません。表示された状態はゲストの稼働確認ではありません。')
      :t('Read-only resources managed through CloudFormation. Create, start, stop and network edits are not supported here. Reported state is not a health check of the guest.','CloudFormation で管理されるリソースの読み取り専用画面です。この画面から作成・起動・停止・ネットワーク編集はできません。表示された状態はゲストの稼働確認ではありません。'))
    :subnets?(manage?t('Subnets that you create here are saved in this local simulator and can be deleted here. Delete CloudFormation-managed subnets by deleting their stack. Route tables, gateways and other network edits are not supported here.','この画面で作成したサブネットはローカルのシミュレーターに保存され、この画面から削除できます。CloudFormation で管理されるサブネットは、スタックを削除して削除してください。ルートテーブル、ゲートウェイなどのネットワーク編集はこの画面ではできません。')
      :t('Read-only resources managed through CloudFormation. Network edits are not supported here.','CloudFormation で管理されるリソースの読み取り専用画面です。この画面からネットワーク編集はできません。'))
    :(manage?t('VPCs that you create here are saved in this local simulator and can be deleted here. Delete CloudFormation-managed VPCs by deleting their stack. Route tables, gateways and other network edits are not supported here.','この画面で作成した VPC はローカルのシミュレーターに保存され、この画面から削除できます。CloudFormation で管理される VPC は、スタックを削除して削除してください。ルートテーブル、ゲートウェイなどのネットワーク編集はこの画面ではできません。')
      :t('Read-only resources managed through CloudFormation. Create, start, stop and network edits are not supported here. Reported state is not a health check of the guest.','CloudFormation で管理されるリソースの読み取り専用画面です。この画面から作成・起動・停止・ネットワーク編集はできません。表示された状態はゲストの稼働確認ではありません。'));
  return <ContentLayout header={<Header variant="h1">{instance?t('EC2 instances','EC2 インスタンス'):subnets?t('Subnets','サブネット'):'VPC'}</Header>}><SpaceBetween size="l">
    <Alert type="info">{banner}</Alert>
    {notice&&<Alert type="success" dismissible dismissAriaLabel={t('Dismiss','閉じる')} onDismiss={()=>setNotice('')}>{notice}</Alert>}
    {error&&<Alert type="error" header={t('Could not load resources','リソースを読み込めませんでした')}>{error}</Alert>}
    <Table trackBy="id" selectionType="single" selectedItems={selected} onSelectionChange={({detail})=>setSelected(detail.selectedItems)}
      ariaLabels={{selectionGroupLabel:t('Select a resource','リソースを選択'),itemSelectionLabel:(_,item)=>t('Select ','選択: ')+item.id}}
      loading={loading} loadingText={t('Loading resources','リソースを読み込み中')}
      items={filtered.slice((page-1)*20,page*20)} columnDefinitions={fields.slice(0,columns).map(([id,en,ja])=>({id,header:t(en,ja),cell:item=>idCell(display(id,item))}))}
      header={<Header counter={'('+items.length+')'} actions={<SpaceBetween direction="horizontal" size="xs">{sessionInstance&&<Button variant="primary" disabled={selected[0]?.id!==sessionInstance} onClick={onSession}>{t('Connect with Session Manager','Session Manager で接続')}</Button>}<Button iconName="refresh" loading={loading} disabled={busy} onClick={()=>load()}>{t('Refresh','更新')}</Button>
        {manage&&<Button disabled={!deletable||busy||loading} disabledReason={detail&&!deletable?stackOnly:undefined} onClick={()=>{setConfirmation('');setDeleteError('');setDeleting(detail);}}>{deleteText}</Button>}
        {manage&&<Button variant="primary" disabled={busy} onClick={openCreate}>{createText}</Button>}</SpaceBetween>}>{instance?t('Instances','インスタンス'):subnets?t('Subnets','サブネット'):t('VPCs','VPC 一覧')}</Header>}
      filter={<TextFilter filteringText={filter} onChange={({detail})=>{setFilter(detail.filteringText);setPage(1);setSelected([]);}} filteringAriaLabel={t('Filter resources','リソースを絞り込む')} filteringPlaceholder={t('Find resources','リソースを検索')}/>}
      pagination={<Pagination currentPageIndex={page} pagesCount={Math.max(1,Math.ceil(filtered.length/20))} onChange={({detail})=>setPage(detail.currentPageIndex)} ariaLabels={{nextPageLabel:t('Next page','次のページ'),previousPageLabel:t('Previous page','前のページ'),pageLabel:n=>String(n)}}/>}
      empty={<SpaceBetween size="s"><span>{error?t('Resources are unavailable. Retry with Refresh.','リソースを取得できません。「更新」で再試行してください。'):items.length?t('No resources match your filter.','検索条件に一致するリソースがありません。'):t('No resources found in this region.','このリージョンには該当するリソースがありません。')}</span>{items.length>0&&<Button onClick={()=>{setFilter('');setPage(1);}}>{t('Clear filter','絞り込みを解除')}</Button>}</SpaceBetween>}/>
    {detail&&<Container header={<Header variant="h2" actions={<Button onClick={()=>setSelected([])}>{t('Close details','詳細を閉じる')}</Button>}>{t('Resource details','リソースの詳細')}</Header>}><KeyValuePairs columns={2} items={fields.map(([key,en,ja])=>({label:t(en,ja),value:idCell(display(key,detail))}))}/></Container>}
    {manage&&<Modal visible={!!deleting} onDismiss={()=>{if(!busy)setDeleting(null);}} header={instance?t('Terminate (delete) instance?','インスタンスを終了 (削除) しますか?'):deleteText} closeAriaLabel={t('Close dialog','ダイアログを閉じる')}>
      <form onSubmit={remove}><Form errorText={deleteError} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={()=>setDeleting(null)}>{t('Cancel','キャンセル')}</Button><Button variant="primary" formAction="submit" loading={busy} disabled={!instance&&confirmation!=='delete'}>{instance?t('Terminate (delete)','終了 (削除)'):t('Delete','削除')}</Button></SpaceBetween>}>
        <SpaceBetween size="m"><Box>{instance?t('The launch record is removed from this simulator. This cannot be undone.','起動記録をこのシミュレーターから削除します。この操作は元に戻せません。'):subnets?t('Delete this subnet? This cannot be undone.','このサブネットを削除しますか? この操作は元に戻せません。'):t('Delete this VPC? This cannot be undone.','この VPC を削除しますか? この操作は元に戻せません。')}</Box><Box>{idCell(deleting?.id)}{deleting?.name?' ('+deleting.name+')':''}</Box>
          {!instance&&<FormField label={t('To confirm deletion, type delete in the field.','削除を確認するには、フィールドに delete と入力してください。')}><Input value={confirmation} placeholder="delete" disabled={busy} onChange={({detail})=>setConfirmation(detail.value)}/></FormField>}</SpaceBetween>
      </Form></form></Modal>}
  </SpaceBetween></ContentLayout>;
}
