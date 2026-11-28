import React, {useEffect, useLayoutEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import {branchCreated, matchesSavedRule, notificationClient, ruleDraft, ruleInput} from './codestar-notifications.mjs';
import {useLocale} from './i18n.jsx';

export default function NotificationRules({region, request, onSaving}) {
  const {t} = useLocale();
  const api = useMemo(() => notificationClient(request, region), [request, region]);
  const task = useRef(null);
  const [busy, setBusy] = useState('');
  const [rows, setRows] = useState([]);
  const [token, setToken] = useState('');
  const [arn, setArn] = useState('');
  const [detail, setDetail] = useState(null);
  const [mode, setMode] = useState('');
  const [draft, setDraft] = useState(ruleDraft);
  const [confirmation, setConfirmation] = useState('');
  const [pending, setPending] = useState(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const title = t('Notification rules', '通知ルール');
  const label = {
    name: t('Rule name', 'ルール名'), source: t('Repository ARN', 'リポジトリ ARN'),
    topics: t('SNS topic ARNs', 'SNS トピック ARN'), enabled: t('Enabled', '有効'),
    created: t('Branch created', 'ブランチの作成'), updated: t('Branch updated', 'ブランチの更新'),
  };

  function begin(kind) {
    task.current?.abort();
    const active = new AbortController();
    task.current = active;
    setBusy(kind); setError(''); setNotice('');
    if (kind === 'write') onSaving(true);
    return active;
  }
  function end(active) {
    if (task.current !== active) return;
    task.current = null; setBusy(''); onSaving(false);
  }
  function cancelRead() {
    if (busy !== 'read') return;
    task.current?.abort(); task.current = null; setBusy('');
  }
  useLayoutEffect(() => () => {task.current?.abort(); task.current = null; onSaving(false);}, [api, onSaving]);

  async function browse(selected = '', continuation = '') {
    const active = begin('read');
    setMode('');
    setArn(selected); setDetail(null);
    if (!selected && !continuation) {setRows([]); setToken('');}
    try {
      if (selected) {
        const item = await api.detail(selected, active.signal);
        if (task.current !== active) return;
        setDetail(item);
        setPending(prior => prior?.arn === selected ? null : prior);
      } else {
        const page = await api.list(continuation, active.signal);
        if (task.current !== active) return;
        setRows(prior => continuation ? [...prior, ...page.NotificationRules] : page.NotificationRules);
        setToken(page.NextToken ?? '');
      }
    } catch (err) {
      if (task.current !== active) return;
      if (selected && /^404:/.test(err.message) && pending?.arn === selected) {
        setPending(null); setNotice(t('The selected rule is absent.', '選択したルールは存在しません。'));
        setRows(prior => prior.filter(row => row.Arn !== selected)); setArn('');
      } else {
        if (!selected && /^(401|403):/.test(err.message)) {setRows([]); setToken('');}
        setError(err.message);
      }
    } finally {end(active);}
  }
  useEffect(() => {browse();}, [api]);

  async function mutate(operation, body) {
    if (task.current) return;
    const active = begin('write');
    let known = body.Arn ?? '';
    let acknowledged = false;
    try {
      known = await api.write(operation, body, active.signal);
      acknowledged = true;
      if (task.current !== active) return;
      if (operation === 'DeleteNotificationRule') {
        try {await api.detail(known, active.signal); throw Error(t('The rule still exists.', 'ルールがまだ存在します。'));}
        catch (err) {if (!/^404:/.test(err.message)) throw err;}
        if (task.current !== active) return;
        setRows(prior => prior.filter(row => row.Arn !== known)); setArn(''); setDetail(null);
      } else {
        const fresh = await api.detail(known, active.signal);
        if (task.current !== active) return;
        if (!matchesSavedRule(fresh, body)) throw Error(t('The saved settings differ from the request.', '保存された設定がリクエストと異なります。'));
        setDetail(fresh); setArn(known);
      }
      setPending(null); setMode(''); setConfirmation('');
      setNotice(t('Saved state verified.', '保存後の状態を確認しました。'));
    } catch (err) {
      if (task.current !== active) return;
      setError(err.message);
      if (acknowledged || !/^4\d\d:/.test(err.message) || pending) {
        setPending({operation, body, arn: known});
        setMode(''); setArn(known); setDetail(null);
      }
    } finally {end(active);}
  }
  const change = (key, value) => setDraft(old => ({...old, [key]: value}));
  const form = ruleInput(draft, region, mode === 'edit' ? arn : '');
  const locked = !!busy || !!pending;
  const startForm = kind => {setMode(kind); setNotice(''); setError(''); setDraft(ruleDraft(kind === 'edit' ? detail : null)); setConfirmation('');};
  const cancel = <Button disabled={!!busy} onClick={() => setMode('')}>{t('Cancel', 'キャンセル')}</Button>;
  const field = (key, disabled = false) => <FormField label={label[key]}><Input disabled={disabled}
    value={draft[key]} onChange={({detail: entry}) => change(key, entry.value)} /></FormField>;

  return <ContentLayout header={<Header variant="h1">{title}</Header>}><SpaceBetween size="m">
    {error && <Alert type="error">{error}</Alert>}
    {notice && <Alert type="success">{notice}</Alert>}
    {pending && <Alert type="warning" header={t('Write outcome needs verification', '書き込み結果の確認が必要です')}>
      <SpaceBetween size="s">
        {pending.arn ? t('Refresh this rule to read its current state.', 'このルールを再読み込みして現在の状態を確認してください。') :
          t('Retry the original create request using its saved client token.', '保存したクライアントトークンで元の作成リクエストを再試行してください。')}
        <Button disabled={!!busy} onClick={() => pending.arn ? browse(pending.arn) : mutate(pending.operation, pending.body)}>
          {pending.arn ? t('Refresh affected rule', '対象ルールを再読み込み') : t('Retry original request', '元のリクエストを再試行')}</Button>
      </SpaceBetween>
    </Alert>}
    {busy === 'read' && <Button onClick={cancelRead}>{t('Cancel loading', '読み込みを中止')}</Button>}
    {!mode && !arn && <Table variant="container" items={rows} trackBy="Arn" loading={busy === 'read'}
      loadingText={t('Loading rules', 'ルールを読み込み中')} ariaLabels={{tableLabel: title}}
      header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">
        <Button disabled={locked} onClick={() => browse()}>{t('Refresh rules', 'ルールを再読み込み')}</Button>
        <Button variant="primary" disabled={locked} onClick={() => startForm('create')}>{t('Create rule', 'ルールを作成')}</Button>
      </SpaceBetween>}>{t('Rules', 'ルール')}</Header>}
      columnDefinitions={[{id:'id',header:t('Rule ID', 'ルール ID'),cell: row => <Button variant="inline-link" disabled={locked} onClick={() => browse(row.Arn)}>{row.Id}</Button>}]}
      empty={<Box>{error ? t('Rules could not be loaded', 'ルールを読み込めませんでした') : t('No notification rules', '通知ルールはありません')}</Box>} />}
    {!mode && !arn && token && <Button disabled={locked} onClick={() => browse('', token)}>{t('Load more rules', 'さらにルールを読み込む')}</Button>}
    {!mode && arn && <Container header={<Header variant="h2" actions={<Button disabled={locked} onClick={() => browse()}>{t('Back to rules', 'ルール一覧に戻る')}</Button>}>{detail?.Name || t('Rule details', 'ルールの詳細')}</Header>}>
      <SpaceBetween size="m">
        <KeyValuePairs items={[{label:t('Rule ARN','ルール ARN'),value:arn}, ...(detail ? [
          {label:label.source,value:detail.Resource},{label:t('Created by','作成者'),value:detail.CreatedBy},
          {label:t('Status','状態'),value:detail.Status},{label:t('Events','イベント'),value:detail.EventTypes.map(event => event.EventTypeId === branchCreated ? label.created : label.updated).join(', ')},
        ] : [])]} />
        {detail && <Table variant="embedded" items={detail.Targets} trackBy="TargetAddress" ariaLabels={{tableLabel: label.topics}}
          columnDefinitions={[{id:'address',header:t('Topic ARN','トピック ARN'),cell:r=>r.TargetAddress},{id:'status',header:t('Target status','ターゲットの状態'),cell:r=>r.TargetStatus}]}
          empty={<Box>{t('No SNS targets','SNS ターゲットはありません')}</Box>} />}
        <SpaceBetween direction="horizontal" size="xs">
          <Button disabled={locked} onClick={() => browse(arn)}>{t('Refresh rule','ルールを再読み込み')}</Button>
          <Button disabled={!detail || locked} onClick={() => startForm('edit')}>{t('Edit rule','ルールを編集')}</Button>
          <Button disabled={!detail || locked} onClick={() => startForm('delete')}>{t('Delete rule','ルールを削除')}</Button>
        </SpaceBetween>
      </SpaceBetween>
    </Container>}
    {(mode === 'create' || mode === 'edit') && <Container header={<Header variant="h2">{mode === 'create' ? t('Create rule','ルールを作成') : t('Edit rule','ルールを編集')}</Header>}>
      <Form actions={<SpaceBetween direction="horizontal" size="xs">{cancel}<Button variant="primary" disabled={!form || locked}
        onClick={() => mode === 'edit' ? mutate('UpdateNotificationRule', form) : mutate('CreateNotificationRule', {...form, ClientRequestToken: crypto.randomUUID()})}>
        {t('Save rule','ルールを保存')}</Button></SpaceBetween>}>
        <SpaceBetween size="m">{field('name', !!busy)}{field('source', !!busy || mode === 'edit')}
          <FormField label={label.topics} description={t('Enter one existing Standard SNS topic ARN per line.', '既存の Standard SNS トピック ARN を 1 行に 1 つ入力してください。')}>
            <Textarea value={draft.topics} disabled={!!busy} onChange={({detail}) => change('topics', detail.value)} /></FormField>
          {['created','updated','enabled'].map(key => <Checkbox key={key} disabled={!!busy} checked={draft[key]} onChange={({detail}) => change(key, detail.checked)}>{label[key]}</Checkbox>)}
        </SpaceBetween>
      </Form>
    </Container>}
    {mode === 'delete' && detail && <Container header={<Header variant="h2">{t('Delete rule','ルールを削除')}</Header>}>
      <Form actions={<SpaceBetween direction="horizontal" size="xs">{cancel}<Button variant="primary" disabled={locked || confirmation !== detail.Name} onClick={() => mutate('DeleteNotificationRule',{Arn:arn})}>
        {t('Confirm deletion','削除を確定')}</Button></SpaceBetween>}>
        <FormField label={t('Confirm rule name','ルール名を確認')} description={detail.Name}><Input disabled={!!busy} value={confirmation} onChange={({detail}) => setConfirmation(detail.value)} /></FormField>
      </Form>
    </Container>}
  </SpaceBetween></ContentLayout>;
}
