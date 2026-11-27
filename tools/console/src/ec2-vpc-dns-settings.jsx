import React,{useEffect,useMemo,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Form from '@cloudscape-design/components/form';
import Header from '@cloudscape-design/components/header';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import {useLocale} from './i18n.jsx';
import {loadVpcDNSAttributes,planVpcDNSChanges,saveVpcDNSAttributes,vpcDNSAttributes,vpcDNSFailure,
  vpcDNSSettingsAvailable,vpcDNSSettingsWritable,vpcDNSTarget} from './ec2-vpc-dns-attributes.mjs';

const [support,hostnames]=vpcDNSAttributes;

export default function VPCDNSSettings({request,region,vpc,capabilities,authenticationRequired,
  writeBlocked=false,acquireWrite}) {
  const {t}=useLocale();
  const owner=vpcDNSTarget(vpc), id=vpc?.id;
  const available=vpcDNSSettingsAvailable(capabilities,authenticationRequired);
  const editable=owner==='ec2' && vpcDNSSettingsWritable(capabilities,authenticationRequired) && typeof acquireWrite==='function';
  const scope=useMemo(()=>({request,region,vpc:{id,managedBy:owner},available,editable}),
    [request,region,id,owner,available,editable]);
  const [state,setState]=useState(null), [modal,setModal]=useState(null);
  const active=useRef(null);
  const current=state?.scope===scope ? state : null;
  const dialog=modal?.scope===scope ? modal : null;
  const writing=current?.status==='writing';
  const readback=current?.snapshot;
  const changes=dialog && readback ? planVpcDNSChanges(readback,dialog.draft) : null;

  function cancel() {
    const operation=active.current;
    active.current=null;
    operation?.controller.abort();
    operation?.release?.();
  }
  function begin(writing) {
    if (active.current?.writing || (writing && (writeBlocked||!scope.editable))) return null;
    const release=writing ? acquireWrite() : null;
    if (writing && typeof release!=='function') return null;
    cancel();
    const operation={controller:new AbortController(),writing,release};
    active.current=operation;
    return operation;
  }
  function finish(operation) {
    if (active.current!==operation || operation.controller.signal.aborted) return false;
    active.current=null;
    operation.release?.();
    return true;
  }
  async function refresh() {
    if (!scope.available || !owner) return;
    const operation=begin(false);
    if (!operation) return;
    setState({scope,status:'loading'});
    setModal(null);
    try {
      const snapshot=await loadVpcDNSAttributes(scope.request,scope.region,scope.vpc,{signal:operation.controller.signal});
      if (finish(operation)) setState({scope,status:'ready',snapshot});
    } catch (error) {
      if (finish(operation)) setState({scope,status:'error',readError:vpcDNSFailure(error)});
    }
  }
  useEffect(()=>{void refresh();return cancel;},[scope]);

  function toggle(attribute,value) {
    setModal(old=>old?.scope===scope ? {...old,draft:{...old.draft,[attribute]:value}} : old);
  }
  async function save(event) {
    event.preventDefault();
    if (!scope.available || !scope.editable || !dialog || current?.status!=='ready' || !changes?.length) return;
    const operation=begin(true);
    if (!operation) return;
    setState({scope,status:'writing',snapshot:readback});
    try {
      const result=await saveVpcDNSAttributes(scope.request,scope.region,scope.vpc,readback,dialog.draft,
        {signal:operation.controller.signal});
      if (!finish(operation)) return;
      setModal(null);
      setState({scope,status:result.snapshot?'ready':'error',...result});
    } catch {
      if (!finish(operation)) return;
      setModal(null);
      setState({scope,status:'error',failed:{attribute:changes[0].attribute,reason:'unconfirmed'},confirmed:[]});
    }
  }

  if (!available || !owner) return null;
  const title=t('DNS settings','DNS 設定');
  const edit=t('Edit VPC settings','VPC 設定を編集');
  const labels={[support]:t('DNS resolution','DNS 解決'),[hostnames]:t('DNS hostnames','DNS ホスト名')};
  const dependency=t('Enable DNS resolution before enabling DNS hostnames.','DNS ホスト名を有効にするには、先に DNS 解決を有効にしてください。');
  const toggleState=(attribute,value)=>!value?t('Disabled','無効'):attribute===hostnames&&!readback[support]
    ?t('Enabled (inactive while DNS resolution is disabled)','有効（DNS 解決が無効の間は使用されません）')
    :t('Enabled','有効');
  const failed=current?.failed;

  return <SpaceBetween size="s">
    <Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button iconName="refresh" onClick={refresh} disabled={writing||!!dialog}
        loading={!current||current.status==='loading'}>{t('Refresh DNS settings','DNS 設定を更新')}</Button>
      {editable&&<Button disabled={current?.status!=='ready'||writeBlocked||!!dialog}
        onClick={()=>setModal({scope,draft:{...readback}})}>{edit}</Button>}
    </SpaceBetween>}>{title}</Header>
    <Box color="text-body-secondary">{t('These are saved settings only. The simulator does not resolve DNS queries or assign DNS names here.',
      '保存済みの設定のみです。シミュレーターは DNS の名前解決やホスト名の割り当てを行いません。')}</Box>
    {owner==='cloudformation'&&<Alert type="info">{t('CloudFormation manages this VPC. Its DNS settings are read-only here.',
      'CloudFormation が管理する VPC です。この画面で DNS 設定は変更できません。')}</Alert>}
    {failed ? <Alert type="error" header={t('Could not complete the DNS settings update','DNS 設定の変更を完了できませんでした')}>
      <SpaceBetween size="xs">
        {failed.phase==='request'&&failed.reason==='denied' ? <Box>{t('This identity is not allowed to change:','現在の ID にはこの設定を変更する権限がありません:')} {labels[failed.attribute]} {t('Check ec2:ModifyVpcAttribute.','ec2:ModifyVpcAttribute 権限を確認してください。')}</Box>
          : failed.phase==='request'&&failed.reason==='dependency' ? <Box>{dependency}</Box>
          : failed.reason==='changed' ? <Box>{t('The latest readback no longer matches the requested value for:','最新の読み取り結果が指定した値と一致しません:')} {labels[failed.attribute]}</Box>
          : <Box>{t('Could not confirm the change to:','この設定の変更結果を確認できませんでした:')} {labels[failed.attribute]}</Box>}
        {current.confirmed.length>0&&<Box>{t('Confirmed before the failure:','失敗する前に保存を確認した設定:')} {current.confirmed.map(key=>labels[key]).join(', ')}</Box>}
        <Box>{failed.reason!=='changed'&&t('No remaining changes were sent.','残りの変更は送信していません。')} {readback
          ?t('Current settings were read again and are shown below.','現在の設定を再取得して表示しています。')
          :t('Refresh DNS settings before trying again.','再試行する前に DNS 設定を更新してください。')}</Box>
      </SpaceBetween>
    </Alert> : current?.status==='error' ? <Alert type="error" header={t('Could not read DNS settings','DNS 設定を取得できませんでした')}>
      {current.readError==='denied' ? t('Check that this identity has ec2:DescribeVpcAttribute for the selected VPC.',
        '選択した VPC に対する ec2:DescribeVpcAttribute 権限を確認してください。')
        : current.readError==='missing' ? t('The VPC was not found in this region. Refresh the VPC list.',
          'このリージョンに該当する VPC がありません。VPC 一覧を更新してください。')
        : t('Use Refresh DNS settings to try again.','「DNS 設定を更新」で再試行してください。')}
    </Alert> : current?.confirmed?.length>0&&<Alert type="success">
      {t('Saved and verified:','保存を確認した設定:')} {current.confirmed.map(key=>labels[key]).join(', ')}
    </Alert>}
    {!current||current.status==='loading' ? <StatusIndicator type="loading">{t('Loading DNS settings','DNS 設定を読み込み中')}</StatusIndicator>
      : readback&&<KeyValuePairs columns={2} items={vpcDNSAttributes.map(key=>({label:labels[key],value:toggleState(key,readback[key])}))}/>}
    {editable&&dialog&&<Modal visible header={edit} closeAriaLabel={t('Close VPC settings','VPC 設定を閉じる')}
      onDismiss={()=>{if(!active.current?.writing)setModal(null);}}>
      <form onSubmit={save}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={writing} onClick={()=>setModal(null)}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" formAction="submit" loading={writing} disabled={!changes?.length||writeBlocked}>
          {t('Save changes','変更を保存')}
        </Button>
      </SpaceBetween>}><SpaceBetween size="m">
        <Box>{t('Selected VPC:','選択した VPC:')} <span className="resource-identifier">{id}</span> ({region})</Box>
        {vpcDNSAttributes.map(key=><Checkbox key={key} checked={dialog.draft[key]} disabled={writing||writeBlocked}
          onChange={({detail})=>toggle(key,detail.checked)}>{key===support?t('Enable DNS resolution','DNS 解決を有効にする')
            :t('Enable DNS hostnames','DNS ホスト名を有効にする')}</Checkbox>)}
        <Box color="text-body-secondary">{dependency}</Box>
        {dialog.draft[hostnames]&&!dialog.draft[support]&&(changes===null
          ?<Alert type="warning">{t('To save this hostname change, also select Enable DNS resolution.',
            'ホスト名の変更を保存するには「DNS 解決を有効にする」も選択してください。')}</Alert>
          :<Alert type="info">{t('DNS hostnames remain enabled in saved settings but are inactive without DNS resolution.',
            'DNS ホスト名は保存済み設定では有効のままですが、DNS 解決が無効の間は使用されません。')}</Alert>)}
        <Box color="text-body-secondary">{t('Changes are saved and checked one at a time. An earlier change can remain if a later request fails.',
          '設定は一つずつ保存・確認します。後の処理が失敗しても、先に保存された変更は残ることがあります。')}</Box>
      </SpaceBetween></Form></form>
    </Modal>}
  </SpaceBetween>;
}
