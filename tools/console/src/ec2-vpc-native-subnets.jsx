import React,{useEffect,useMemo,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {createVpcNativeSubnet,deleteVpcNativeSubnet,loadVpcSubnets,nativeSubnetCIDR,nativeSubnetFailure,subnetTarget} from './ec2-native-subnets.mjs';

export default function VPCNativeSubnets({request,region,vpc,onSaving}) {
  const {t} = useLocale();
  const {id,cidr,managedBy} = vpc;
  const scope = useMemo(()=>({request,region,vpc:{id,cidr,managedBy}}),[request,region,id,cidr,managedBy]);
  const target = subnetTarget(vpc), writable = target==='ec2';
  const active = useRef(null);
  const [snapshot,setSnapshot] = useState(null), [modal,setModal] = useState(null), [notice,setNotice] = useState(null);
  const current = snapshot?.scope===scope ? snapshot : null;
  const dialog = modal?.scope===scope ? modal : null;
  const pending = current?.status === 'writing';
  const zones = ['a','b','c'].map(s=>({value:region+s,label:region+s}));
  function cancel() {
    active.current?.controller.abort();
    if (active.current?.writing) onSaving(false);
    active.current = null;
  }
  function begin(writing) {
    if (active.current?.writing) return null;
    cancel();
    const operation = {controller:new AbortController(),writing};
    active.current = operation;
    return operation;
  }
  function complete(operation) {
    if (active.current!==operation) return false;
    active.current = null;
    if (operation.writing) onSaving(false);
    return !operation.controller.signal.aborted;
  }
  async function refresh() {
    if (!target) return;
    const operation = begin(false);
    if (!operation) return;
    setSnapshot({scope,status:'loading'});
    setNotice(null);
    try {
      const rows = await loadVpcSubnets(scope.request,scope.region,scope.vpc,{signal:operation.controller.signal});
      if (complete(operation)) setSnapshot({scope,status:'ready',rows});
    } catch (error) {
      if (complete(operation)) setSnapshot({scope,status:nativeSubnetFailure(error)});
    }
  }
  useEffect(()=>{void refresh(); return cancel;},[scope]);
  const validCIDR = dialog?.kind==='create' && !!nativeSubnetCIDR(vpc,dialog.cidr);
  function change(key,value) {setModal(old=>({...old,[key]:value}));}
  async function submit(event) {
    event.preventDefault();
    if (!writable || !dialog || current?.status!=='ready' || (dialog.kind==='create' ? !validCIDR : dialog.confirm!=='delete')) return;
    const operation = begin(true);
    if (!operation) return;
    setNotice(null);
    setSnapshot({...current,status:'writing'});
    onSaving(true);
    try {
      const config = {signal:operation.controller.signal};
      const result = dialog.kind==='create'
        ? await createVpcNativeSubnet(scope.request,scope.region,scope.vpc,dialog.cidr,dialog.az,config)
        : await deleteVpcNativeSubnet(scope.request,scope.region,scope.vpc,dialog.subnet,config);
      if (!complete(operation)) return;
      setSnapshot({scope,status:'ready',rows:result.rows});
      setModal(null);
      setNotice({scope,id:(result.created || result.removed).id,kind:dialog.kind});
    } catch (error) {
      if (!complete(operation)) return;
      // After ambiguous completion reconcile by a read, never resend a write.
      setSnapshot({scope,status:nativeSubnetFailure(error),mutationFailed:true});
      setModal(null);
    }
  }
  if (!target) return <Box>{t('Select a verified VPC to read subnets.','サブネットを表示するには確認済みの VPC を選択してください。')}</Box>;
  const ready = current?.status==='ready';
  const errors = {
    denied: t('The selected identity cannot complete the subnet request. Check its EC2 permissions.','現在の ID ではサブネットへの操作が許可されていません。EC2 権限を確認してください。'),
    overlap: t('The CIDR overlaps a subnet in this VPC.','CIDR がこの VPC の別のサブネットと重複しています。'),
    dependency: t('This subnet still has a network dependency.','このサブネットには依存リソースが残っています。'),
    unconfirmed: t('Could not confirm subnet state. No result was reported.','サブネットの状態を確認できませんでした。結果は表示していません。'),
  };
  return <SpaceBetween size="s">
    <Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button iconName="refresh" disabled={pending} loading={!current||current.status==='loading'} onClick={refresh}>{t('Refresh subnets','サブネットを更新')}</Button>
      {writable&&<Button disabled={!ready} onClick={()=>setModal({scope,kind:'create',cidr:'',az:region+'a'})}>{t('Add subnet','サブネットを追加')}</Button>}
    </SpaceBetween>}>{t('Subnets','サブネット')}</Header>
    <Box color="text-body-secondary">{writable
      ? t('Saved IPv4 configuration in this VPC. No routes or guest connectivity are created.','この VPC の IPv4 設定を保存します。ルートやゲストの通信は作成しません。')
      : t('CloudFormation owns this VPC. Saved subnets are shown, but this Console does not change the stack.','CloudFormation が管理する VPC です。保存済みのサブネットは表示しますが、スタックは変更できません。')}</Box>
    {notice?.scope===scope&&<Alert type="success">{notice.kind==='create'?t('Saved subnet: ','サブネットを保存しました: '):t('Removed subnet: ','サブネットを削除しました: ')}{notice.id}</Alert>}
    {errors[current?.status]&&<Alert type="error">{errors[current.status]} {current.mutationFailed&&t('Refresh subnets before retrying.','再試行する前にサブネットを更新してください。')}</Alert>}
    <Table variant="embedded" trackBy="id" items={current?.rows??[]} loading={!current||current.status==='loading'||pending}
      ariaLabels={{tableLabel:t('Saved subnets','保存済みサブネット')}}
      columnDefinitions={[
        {id:'id',header:t('Subnet ID','サブネット ID'),cell:item=><span className="resource-identifier">{item.id}</span>},
        {id:'cidr',header:t('IPv4 CIDR','IPv4 CIDR'),cell:item=>item.cidr},
        {id:'az',header:t('Availability Zone','アベイラビリティーゾーン'),cell:item=>item.az||'—'},
        ...(writable?[{id:'actions',header:t('Actions','操作'),cell:item=><Button disabled={!ready} onClick={()=>setModal({scope,kind:'delete',subnet:item,confirm:''})}>{t('Delete subnet','サブネットを削除')}</Button>}]:[]),
      ]} empty={ready?t('No subnets are recorded for this VPC.','この VPC にサブネットはありません。'):t('Subnets are unavailable.','サブネットは利用できません。')}/>
    {dialog&&<Modal visible closeAriaLabel={t('Close subnet dialog','サブネットのダイアログを閉じる')} onDismiss={()=>{if (!pending) setModal(null);}}
      header={dialog.kind==='create'?t('Add subnet','サブネットを追加'):t('Delete subnet','サブネットを削除')}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={pending} onClick={()=>setModal(null)}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" formAction="submit" loading={pending} disabled={dialog.kind==='create'?!validCIDR:dialog.confirm!=='delete'}>
          {dialog.kind==='create'?t('Create subnet','サブネットを作成'):t('Delete subnet','サブネットを削除')}
        </Button>
      </SpaceBetween>}><SpaceBetween size="m">
        {dialog.kind==='create'?<>
          <Box>{t('Selected VPC: ','選択済み VPC: ')}{vpc.id} ({vpc.cidr})</Box>
          <FormField label={t('Subnet IPv4 CIDR','サブネットの IPv4 CIDR')}
            constraintText={t('Use a /16–/28 range within this VPC.','この VPC 内の /16〜/28 の範囲を入力してください。')}
            errorText={dialog.cidr&&!validCIDR?t('Enter a range inside the selected VPC.','選択した VPC 内の範囲を入力してください。'):undefined}>
            <Input value={dialog.cidr} disabled={pending} placeholder="10.0.1.0/24" onChange={({detail})=>change('cidr',detail.value)}/>
          </FormField>
          <FormField label={t('Availability Zone','アベイラビリティーゾーン')}><Select options={zones} disabled={pending}
            selectedOption={zones.find(option=>option.value===dialog.az)} onChange={({detail})=>change('az',detail.selectedOption.value)}/></FormField>
        </>:<>
          <Box>{t('Delete this saved subnet from the selected VPC: ','選択した VPC からこのサブネットを削除します: ')}{dialog.subnet.id}</Box>
          <FormField label={t('Type delete to confirm','確認のため delete と入力')}><Input value={dialog.confirm} disabled={pending} onChange={({detail})=>change('confirm',detail.value)}/></FormField>
        </>}
      </SpaceBetween></Form></form>
    </Modal>}
  </SpaceBetween>;
}
