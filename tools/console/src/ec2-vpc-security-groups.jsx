import React, {useEffect, useMemo, useRef, useState} from 'react';
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
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {loadVpcSecurityGroups, vpcSecurityGroupsFailure} from './ec2-security-groups.mjs';
import {createVpcNativeSecurityGroup, deleteVpcNativeSecurityGroup, loadVpcNativeSecurityGroups,
  nativeSGFailure, nativeSGName, nativeSGTarget, validNativeSGDescription} from './ec2-native-security-groups.mjs';

const loading = {status:'loading', groups:[]};
const wrap = text => <span className="resource-identifier">{text}</span>;
const noop = () => {};
function SecurityGroupTags({tags}) {
  const {t} = useLocale();
  return <Table variant="embedded" trackBy="key" items={tags}
    header={<Box variant="h4">{t('Security group tags', 'セキュリティグループのタグ')}</Box>}
    columnDefinitions={[
      {id:'key',header:t('Key','キー'),cell:tag=>wrap(tag.key)},
      {id:'value',header:t('Value','値'),cell:tag=>tag.value===''?t('(empty)','（空）'):wrap(tag.value)},
    ]}
    empty={<Box color="text-body-secondary">{t('No tags are recorded.', 'タグは記録されていません。')}</Box>}/>;
}

export function SecurityGroupDetails({group}) {
  const {t} = useLocale();
  const inbound = t('Recorded inbound rules (TCP/IPv4)', '記録された受信ルール (TCP/IPv4)');
  return <SpaceBetween size="m">
    <div><Box variant="h4">{t('Description', '説明')}</Box>{wrap(group.description)}</div>
    <div><Box variant="h4">{inbound}</Box>{group.ingress.length
      ? <ul aria-label={inbound}>{group.ingress.map(({from,to,cidr}, index) => <li key={index}>{wrap('TCP '+(from===to ? from : from+'–'+to)+' · '+cidr)}</li>)}</ul>
      : <Box color="text-body-secondary">{t('No inbound rules are recorded.', '受信ルールは記録されていません。')}</Box>}</div>
    <SecurityGroupTags tags={group.tags}/>
  </SpaceBetween>;
}

function LegacyVPCSecurityGroups({vpc, region, request}) {
  const {t} = useLocale();
  const [state, setState] = useState(loading), [selected, setSelected] = useState(''), [refresh, setRefresh] = useState(0);
  const id = vpc.id, managedBy = vpc.managedBy;
  const applicable = managedBy === 'cloudformation';
  useEffect(() => {
    if (!applicable) return;
    const pending = new AbortController();
    setState(loading);
    loadVpcSecurityGroups(request, region, {id, managedBy}, {signal:pending.signal}).then(groups => {
      if (!pending.signal.aborted) setState({status:'ready', groups});
    }, error => {
      if (!pending.signal.aborted) setState({status:vpcSecurityGroupsFailure(error), groups:[]});
    });
    return () => pending.abort();
  }, [request, region, id, managedBy, applicable, refresh]);

  const options = state.groups.map(group => ({value:group.id,label:group.id}));
  const group = state.groups.find(g => g.id === selected) ?? state.groups[0];
  const title = t('Security groups','セキュリティグループ');
  return <SpaceBetween size="m">
    <Header variant="h3" actions={applicable && <Button iconName="refresh" loading={state.status==='loading'} onClick={() => {
      setState(loading);setRefresh(v=>v+1);
    }}>{t('Refresh security groups','セキュリティグループを更新')}</Button>}>{title}</Header>
    {!applicable ? <Box color="text-body-secondary">{managedBy==='ec2'
      ? t('This pane displays CloudFormation-managed groups only. Detached EC2 security groups are available through the native API.', 'この画面は CloudFormation 管理のグループ専用です。未接続の EC2 セキュリティグループはネイティブ API で操作できます。')
      : t('Security group readback requires a CloudFormation-managed VPC.', 'セキュリティグループの読み取りには CloudFormation 管理の VPC が必要です。')}</Box>
      : state.status==='loading' ? <StatusIndicator type="loading">{t('Loading security groups','セキュリティグループを読み込み中')}</StatusIndicator>
      : state.status==='denied' ? <Alert type="error" header={t('Security group access denied','セキュリティグループの参照が拒否されました')}>{t('The current identity needs ec2:DescribeSecurityGroups. No group data was shown.', '現在の ID には ec2:DescribeSecurityGroups が必要です。グループ情報は表示していません。')}</Alert>
      : state.status==='error' ? <Alert type="error" header={t('Could not read security groups','セキュリティグループを読み取れませんでした')}>{t('Use Refresh security groups to try again.', '「セキュリティグループを更新」で再試行してください。')}</Alert>
      : group ? <SpaceBetween size="m">
        <FormField label={t('Security group','セキュリティグループ')} description={t('Saved rules only. This view does not verify network traffic.', '保存済みルールのみを表示しています。この画面では通信を検証しません。')}>
          <Select selectedOption={options.find(option=>option.value===group.id)} options={options} onChange={({detail})=>setSelected(detail.selectedOption.value)}/>
        </FormField>
        <SecurityGroupDetails group={group}/>
      </SpaceBetween> : <div role="status">{t('No security groups are recorded for this VPC.', 'この VPC に記録されたセキュリティグループはありません。')}</div>}
  </SpaceBetween>;
}

function VPCNativeSecurityGroups({vpc, region, request, onSaving = noop}) {
  const {t} = useLocale(), {id, managedBy} = vpc;
  const scope = useMemo(()=>({region,request,vpc:{id,managedBy}}),[region,request,id,managedBy]);
  const active = useRef(null);
  const [snapshot,setSnapshot] = useState(null), [modal,setModal] = useState(null);
  const [selection,setSelection] = useState(null), [notice,setNotice] = useState(null);
  const current = snapshot?.scope===scope ? snapshot : null;
  const dialog = modal?.scope===scope ? modal : null;
  const ready = current?.status==='ready', writing = current?.status==='writing';
  const rows = current?.rows??[];
  const group = rows.find(g=>selection?.scope===scope&&g.id===selection.id) ?? rows[0];
  const options = rows.map(g=>({value:g.id,label:g.name,description:g.id}));

  function cancel() {
    const operation = active.current;
    active.current = null;
    operation?.controller.abort();
    if (operation?.writing) operation.onSaving(false);
  }
  function begin(writing) {
    if (active.current?.writing) return null;
    cancel();
    const operation = {controller:new AbortController(),writing,onSaving};
    active.current = operation;
    if (writing) onSaving(true);
    return operation;
  }
  function complete(operation) {
    if (active.current!==operation) return false;
    active.current = null;
    if (operation.writing) operation.onSaving(false);
    return !operation.controller.signal.aborted;
  }
  async function refresh() {
    const operation = begin(false);
    if (!operation) return;
    setSnapshot({scope,status:'loading'});
    setNotice(null);
    try {
      const rows = await loadVpcNativeSecurityGroups(scope.request,scope.region,scope.vpc,{signal:operation.controller.signal});
      if (complete(operation)) setSnapshot({scope,status:'ready',rows});
    } catch (error) {
      if (complete(operation)) setSnapshot({scope,status:nativeSGFailure(error)});
    }
  }
  useEffect(()=>{void refresh(); return cancel;},[scope]);

  const nameValid = !!nativeSGName(dialog?.name), descriptionValid = validNativeSGDescription(dialog?.description);
  const canSubmit = !!dialog && (dialog.kind==='create' ? nameValid && descriptionValid : dialog.confirm==='delete');
  function change(key,value) {setModal(old=>old?.scope===scope?{...old,[key]:value}:old);}
  async function submit(event) {
    event.preventDefault();
    if (!ready || !canSubmit) return;
    if (dialog.kind==='delete' && !rows.some(g=>g.id===dialog.group.id && g.vpc===scope.vpc.id)) return;
    const operation = begin(true);
    if (!operation) return;
    setSnapshot({scope,status:'writing'});
    setNotice(null);
    try {
      const opts = {signal:operation.controller.signal};
      const result = dialog.kind==='create'
        ? await createVpcNativeSecurityGroup(scope.request,scope.region,scope.vpc,dialog,opts)
        : await deleteVpcNativeSecurityGroup(scope.request,scope.region,scope.vpc,dialog.group,opts);
      if (!complete(operation)) return;
      setSnapshot({scope,status:'ready',rows:result.rows});
      if (result.created) setSelection({scope,id:result.created.id});
      else setSelection(null);
      setModal(null);
      setNotice({scope,kind:dialog.kind,id:(result.created||result.removed).id});
    } catch (error) {
      if (!complete(operation)) return;
      // A failed readback may follow a committed write. Require a new read
      // from the current scope; never automatically resend the mutation.
      setSnapshot({scope,status:nativeSGFailure(error),reconcile:true});
      setModal(null);
    }
  }

  const errors = {
    denied:t('The selected identity cannot complete this security group request. Check its EC2 permissions.','現在の ID ではこのセキュリティグループを操作できません。EC2 権限を確認してください。'),
    duplicate:t('A security group with that name already exists in this VPC.','この VPC には同じ名前のセキュリティグループがあります。'),
    dependency:t('This security group still has a dependency.','このセキュリティグループには依存リソースが残っています。'),
    unconfirmed:t('Could not confirm security group state. No change was reported.','セキュリティグループの状態を確認できず、変更結果は表示していません。'),
  };
  const createLabel = t('Create security group','セキュリティグループを作成');
  const deleteLabel = t('Delete security group','セキュリティグループを削除');
  const refreshLabel = t('Refresh security groups','セキュリティグループを更新');
  return <SpaceBetween size="m">
    <Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button iconName="refresh" ariaLabel={refreshLabel} disabled={writing} loading={!current||current.status==='loading'} onClick={refresh}/>
      <Button disabled={!ready} onClick={()=>setModal({scope,kind:'create',name:'',description:''})}>{createLabel}</Button>
    </SpaceBetween>}>{t('Security groups','セキュリティグループ')}</Header>
    <Alert type="info">{t('Detached configuration only. These groups have no editable rules and do not attach to instances or affect traffic.',
      '未接続の設定のみを保存します。ルールの編集やインスタンスへの接続はできず、通信にも影響しません。')}</Alert>
    {notice?.scope===scope&&<Alert type="success">{notice.kind==='create'
      ?t('Saved security group: ','セキュリティグループを保存しました: ')
      :t('Removed security group: ','セキュリティグループを削除しました: ')}{wrap(notice.id)}</Alert>}
    {errors[current?.status]&&<Alert type="error">{errors[current.status]} {current.reconcile
      &&t('Refresh security groups before retrying.','再試行する前にセキュリティグループを更新してください。')}</Alert>}
    {(!current||current.status==='loading'||writing)&&<StatusIndicator type="loading">
      {writing?t('Checking the saved change','保存結果を確認中'):t('Loading security groups','セキュリティグループを読み込み中')}
    </StatusIndicator>}
    {ready&&(group?<SpaceBetween size="m">
      <FormField label={t('Saved security group','保存済みセキュリティグループ')}>
        <Select selectedOption={options.find(option=>option.value===group.id)} options={options}
          onChange={({detail})=>setSelection({scope,id:detail.selectedOption.value})}/>
      </FormField>
      <div><Box variant="h4">{t('Group ID','グループ ID')}</Box>{wrap(group.id)}</div>
      <div><Box variant="h4">{t('Description','説明')}</Box>{wrap(group.description)}</div>
      <SecurityGroupTags tags={group.tags}/>
      <Button onClick={()=>setModal({scope,kind:'delete',group,confirm:''})}>{deleteLabel}</Button>
    </SpaceBetween>:<div role="status">{t('No detached security groups are recorded for this VPC.','この VPC に未接続のセキュリティグループはありません。')}</div>)}
    {dialog&&<Modal visible closeAriaLabel={t('Close security group dialog','セキュリティグループのダイアログを閉じる')}
      header={dialog.kind==='create'?createLabel:deleteLabel} onDismiss={()=>{if (!writing) setModal(null);}}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={writing} onClick={()=>setModal(null)}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" formAction="submit" disabled={!canSubmit||!ready} loading={writing}>{dialog.kind==='create'?createLabel:deleteLabel}</Button>
      </SpaceBetween>}><SpaceBetween size="m">
        <Box>{t('Selected VPC: ','選択済み VPC: ')}{wrap(id)}</Box>
        {dialog.kind==='create'?<>
          <FormField label={t('Group name','グループ名')}
            constraintText={t('Up to 255 characters. Names are unique in each VPC and cannot be default or start with sg-.','255 文字まで。名前は VPC 内で重複できません。default という名前と、sg- で始まる名前は使えません。')}
            errorText={dialog.name&&!nameValid?t('Enter a valid group name.','有効なグループ名を入力してください。'):undefined}>
            <Input value={dialog.name} disabled={writing} onChange={({detail})=>change('name',detail.value)}/>
          </FormField>
          <FormField label={t('Group description','グループの説明')}
            constraintText={t('Use 1–255 EC2 name and description characters.','EC2 でサポートされる文字を 1〜255 文字入力してください。')}
            errorText={dialog.description&&!descriptionValid?t('Enter a valid description.','有効な説明を入力してください。'):undefined}>
            <Input value={dialog.description} disabled={writing} onChange={({detail})=>change('description',detail.value)}/>
          </FormField>
        </>:<>
          <Box>{t('Delete saved group: ','削除するグループ: ')}{wrap(dialog.group.name)} ({wrap(dialog.group.id)})</Box>
          <FormField label={t('Type delete to confirm','確認のため delete と入力')}>
            <Input value={dialog.confirm} disabled={writing} onChange={({detail})=>change('confirm',detail.value)}/>
          </FormField>
        </>}
      </SpaceBetween></Form></form>
    </Modal>}
  </SpaceBetween>;
}

export default function VPCSecurityGroups(props) {
  return props.nativeLifecycle===true && nativeSGTarget(props.vpc)
    ? <VPCNativeSecurityGroups {...props}/>
    : <LegacyVPCSecurityGroups {...props}/>;
}
