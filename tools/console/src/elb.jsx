import React, {useEffect, useLayoutEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import {elbClient, TargetGroupUnlinked, validHealthPath} from './elb.mjs';
import {useLocale} from './i18n.jsx';

const inventory = {kind: 'list', name: ''};
const definitive = /^(?:AccessDenied|ValidationError|TargetGroupNotFound|InvalidConfigurationRequest):/;

export default function ElasticLoadBalancing({region, request, onSaving}) {
  const {t} = useLocale();
  const client = useMemo(() => elbClient(request,region), [request,region]);
  const task = useRef(null);
  const [busy, setBusy] = useState('');
  const [screen, setScreen] = useState(inventory);
  const [data, setData] = useState(null);
  const [query, setQuery] = useState('');
  const [path, setPath] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  function message(err) {
    return err instanceof TargetGroupUnlinked
      ? t('This target group is no longer linked to the selected load balancer.', 'このターゲットグループは選択したロードバランサーに関連付けられていません。')
      : err.message;
  }
  function begin(kind) {
    task.current?.abort();
    const active = new AbortController();
    task.current = active; setBusy(kind); setError(''); setNotice('');
    if (kind === 'write') onSaving(true);
    return active;
  }
  function finish(active) {
    if (task.current !== active) return;
    task.current = null; setBusy(''); onSaving(false);
  }
  async function load(destination, previous = null) {
    const active = begin('read');
    setScreen(destination); setData(null); setPath('');
    try {
      let next;
      if (destination.kind === 'list') {
        const page = await client.list(destination.name, previous?.next ?? '', active.signal);
        next = {rows: [...(previous?.rows ?? []), ...page.rows], next: page.next};
      } else if (destination.kind === 'lb') {
        next = await client.balancer(destination.arn, active.signal);
      } else {
        next = await client.group(destination.arn, destination.lb, active.signal);
      }
      if (task.current !== active) return;
      setData(next);
      if (destination.kind === 'group') {setPath(next.path); setUncertain(false);}
    } catch (err) {
      if (task.current !== active) return;
      setError(message(err));
      // A fresh observation of deletion or reassociation releases the view without confirming the write.
      if (destination.kind === 'group' && (err instanceof TargetGroupUnlinked || /^TargetGroupNotFound:/.test(err.message))) setUncertain(false);
    } finally {finish(active);}
  }
  useLayoutEffect(() => () => {task.current?.abort(); task.current = null; onSaving(false);}, [client,onSaving]);
  useEffect(() => {load(inventory);}, [client]);
  function cancel() {
    if (busy === 'read') {task.current?.abort(); task.current = null; setBusy('');}
  }
  async function save() {
    if (task.current || uncertain || screen.kind !== 'group' || !data || path === data.path || !validHealthPath(path)) return;
    const active = begin('write'), wanted = path, arn = data.arn;
    let sent = false, acknowledged = false;
    try {
      await client.linkedGroup(arn, screen.lb, active.signal);
      sent = true;
      await client.modify(arn, screen.lb, wanted, active.signal);
      acknowledged = true;
      const current = await client.group(arn, screen.lb, active.signal);
      if (task.current !== active) return;
      if (current.path !== wanted) throw Error(t('The saved path differs from the request.', '保存されたパスがリクエストと一致しません。'));
      setData(current); setPath(current.path); setNotice(t('Saved health check path verified.', '保存したヘルスチェックパスを確認しました。'));
    } catch (err) {
      if (task.current !== active) return;
      setData(null); setPath(''); setError(message(err));
      setUncertain(sent && (acknowledged || !definitive.test(err.message)));
    } finally {finish(active);}
  }

  const locked = !!busy || uncertain;
  const col = (id, header, cell) => ({id, header, cell, minWidth: 130});
  const rows = (heading, items, columns, key, empty) => <Table variant="embedded" wrapLines
    ariaLabels={{tableLabel: heading}} trackBy={key} items={items} columnDefinitions={columns}
    header={<Header variant="h2">{heading}</Header>} empty={<Box>{empty}</Box>}/>;
  const facts = entries => <KeyValuePairs columns={1} items={entries.map(([label,value]) => ({label,value}))}/>;
  const current = t('State','状態'), name = t('Name','名前');
  const lbs = t('Load balancers','ロードバランサー'), listeners = t('Listeners','リスナー');
  const groups = t('Target groups','ターゲットグループ'), targets = t('Registered targets','登録済みターゲット');
  const groupScreen = screen.kind === 'group';
  const refresh = <Button disabled={!!busy} onClick={() => load(screen)}>{uncertain
    ? t('Refresh affected target group','対象のターゲットグループを再読み込み') : t('Refresh','再読み込み')}</Button>;
  return <SpaceBetween size="m">
    <Header variant="h1" actions={<SpaceBetween direction="horizontal" size="xs">
      {screen.kind !== 'list' && <Button disabled={locked} onClick={() => {setQuery('');load(inventory);}}>{t('Back to load balancers','ロードバランサー一覧に戻る')}</Button>}
      {busy === 'read' && <Button onClick={cancel}>{t('Cancel read','読み込みをキャンセル')}</Button>}{refresh}
    </SpaceBetween>}>{t('Elastic Load Balancing','Elastic Load Balancing')}</Header>
    {error && <Alert type="error">{error}</Alert>}
    {uncertain && <Alert type="warning">{t('The update outcome is unknown. Refresh the affected target group to inspect its current settings.',
      '更新の結果を確認できません。対象のターゲットグループを再読み込みして現在の設定を確認してください。')}</Alert>}
    {notice && <Alert type="success">{notice}</Alert>}
    {busy && <StatusIndicator type="loading">{busy === 'write' ? t('Saving','保存中') : t('Loading','読み込み中')}</StatusIndicator>}
    {screen.kind === 'list' && <>
      <form onSubmit={event => {event.preventDefault();if (!locked) load({kind:'list',name:query.trim()});}}>
        <SpaceBetween size="xs"><FormField label={t('Exact load balancer name','ロードバランサー名（完全一致）')}>
          <Input value={query} disabled={locked} onChange={({detail}) => setQuery(detail.value)}/></FormField>
          <Button formAction="submit" disabled={locked}>{t('Apply filter','フィルターを適用')}</Button></SpaceBetween>
      </form>
      {data && rows(lbs,data.rows,[col('name',name,lb => <Button variant="inline-link" disabled={locked} onClick={() => load({kind:'lb',arn:lb.arn})}>{lb.name}</Button>),
        col('state',current,lb => lb.state),col('vpc','VPC',lb => lb.vpc || t('Not recorded','記録なし'))],'arn',t('No load balancers found.','ロードバランサーは見つかりませんでした。'))}
      {data?.next && <Button disabled={locked} onClick={() => load(screen,data)}>{t('Load more load balancers','ロードバランサーを追加で読み込む')}</Button>}
    </>}
    {screen.kind === 'lb' && data && <>
      <Container header={<Header variant="h2">{data.lb.name}</Header>}>{facts([['ARN',data.lb.arn],[current,data.lb.state],['VPC',data.lb.vpc || t('Not recorded','記録なし')],
        [t('Security groups','セキュリティグループ'),data.lb.groups.join(', ')]])}</Container>
      {rows(t('Subnet placement','サブネット配置'),data.lb.subnets,[col('id',t('Subnet ID','サブネット ID'),s=>s.id),col('zone',t('Availability Zone','アベイラビリティーゾーン'),s=>s.zone || t('Not recorded','記録なし'))],'id',t('No saved subnets.','保存済みサブネットはありません。'))}
      {rows(listeners,data.listeners,[col('port',t('Protocol and port','プロトコルとポート'),l=>l.protocol+' : '+l.port),col('action',t('Default action','デフォルトアクション'),l=>l.type),col('destination',t('Target or response','ターゲットまたは応答'),l=>l.destination)],'arn',t('No listeners.','リスナーはありません。'))}
      {rows(groups,data.groups,[col('name',name,g=><Button variant="inline-link" disabled={locked} onClick={()=>load({kind:'group',arn:g.arn,lb:screen.arn})}>{g.name}</Button>),col('arn','ARN',g=>g.arn)],'arn',t('No linked target groups.','関連するターゲットグループはありません。'))}
    </>}
    {groupScreen && data && <>
      <Button disabled={locked} onClick={()=>load({kind:'lb',arn:screen.lb})}>{t('Back to load balancer details','ロードバランサーの詳細に戻る')}</Button>
      <Container header={<Header variant="h2">{data.name}</Header>}>{facts([['ARN',data.arn],[t('Health checks','ヘルスチェック'),data.enabled?t('Enabled','有効'):t('Disabled','無効')]])}</Container>
      {rows(targets,data.health,[col('id','Lambda ARN',h=>h.id),col('state',current,h=>h.state),col('reason',t('Reason','理由'),h=>h.reason || '—')],'id',t('No registered targets.','登録済みターゲットはありません。'))}
      <Container header={<Header variant="h2">{t('Health check settings','ヘルスチェック設定')}</Header>}>
        <form onSubmit={event=>{event.preventDefault();save();}}><SpaceBetween size="s">
          <FormField label={t('Health check path','ヘルスチェックパス')} errorText={validHealthPath(path)?null:t('Enter a path beginning with /, up to 1,024 printable ASCII characters without spaces.','/ で始まる空白を含まない ASCII パスを 1,024 文字以内で入力してください。')}>
            <Input value={path} disabled={locked} onChange={({detail})=>setPath(detail.value)}/></FormField>
          <Button variant="primary" formAction="submit" disabled={locked || !validHealthPath(path) || path===data.path}>{t('Save health check path','ヘルスチェックパスを保存')}</Button>
        </SpaceBetween></form>
      </Container>
    </>}
  </SpaceBetween>;
}
