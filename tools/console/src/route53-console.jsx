import React, {useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {focusPTRResult, initialPTRDraft, ptrRecord, route53Client, route53ConsoleAvailable, updatePTRDraft} from './route53-console.mjs';

const identifier = text => <span className="resource-identifier">{text}</span>;

function useOperation(onSaving) {
  const pending = useRef(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState('');
  const run = useCallback(async (operation, write = false, onError) => {
    if (pending.current) return;
    const controller = new AbortController();
    const entry = {controller, write};
    pending.current = entry;
    setBusy(true);
    setError('');
    if (write) onSaving?.(true);
    try {
      await operation(controller.signal);
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(cause.message);
        onError?.();
      }
    } finally {
      if (pending.current === entry) {
        pending.current = null;
        setBusy(false);
        if (write) onSaving?.(false);
      }
    }
  }, [onSaving]);
  useLayoutEffect(() => () => {
    const entry = pending.current;
    if (!entry) return;
    pending.current = null;
    entry.controller.abort();
    if (entry.write) onSaving?.(false);
  }, [onSaving]);
  return {busy, error, run};
}

export function Route53PTRForm({zone, fields, busy, mustRefresh, onChange, onSave}) {
  const {t} = useLocale();
  const valid = ptrRecord(zone, fields);
  const locked = busy || mustRefresh;
  const set = key => ({detail}) => { if (!locked) onChange(key, detail.value); };
  const reason = mustRefresh ? t('Refresh the records before submitting again.', '再送信するにはレコードを再読み込みしてください。')
    : busy ? t('A Route 53 request is in progress.', 'Route 53へのリクエストを処理中です。')
    : !valid ? t('Fill in a valid record name, TTL and target.', '有効なレコード名、TTL、値を入力してください。') : undefined;
  const action = t('Create PTR record', 'PTRレコードを作成');
  return <Container header={<Header variant="h2">{t('Create one PTR record', 'PTRレコードを1件作成')}</Header>}>
    <form onSubmit={event => {
      event.preventDefault();
      if (!locked && valid) {
        const form = event.currentTarget, active = form.ownerDocument.activeElement;
        onSave(fields, form.contains(active) ? active : null);
      }
    }}>
      <SpaceBetween size="s">
        {!valid && fields.name && fields.target && <Alert type="warning">{t('Use a name within this zone, a valid TTL and one ASCII DNS target.', 'このゾーン内の名前、有効なTTL、ASCIIのDNS名1件を指定してください。')}</Alert>}
        <FormField label={t('Full record name', '完全なレコード名')} constraintText={t('Use the full name in the selected hosted zone.', '選択したホストゾーン内の完全な名前を入力してください。')}>
          <Input value={fields.name} onChange={set('name')} readOnly={locked} placeholder="7.2.0.192.in-addr.arpa."/>
        </FormField>
        <FormField label={t('TTL (seconds)', 'TTL（秒）')} constraintText="0–2147483647">
          <Input value={fields.ttl} onChange={set('ttl')} readOnly={locked} inputMode="numeric"/>
        </FormField>
        <FormField label={t('PTR target', 'PTRの値')} constraintText={t('One ASCII DNS name. No addresses are looked up.', 'ASCIIのDNS名を1件指定します。名前解決は行いません。')}>
          <Input value={fields.target} onChange={set('target')} readOnly={locked} placeholder="web.example.test."/>
        </FormField>
        <Button formAction="submit" variant="primary" disabled={!!reason} disabledReason={reason} ariaLabel={action}>{action}</Button>
      </SpaceBetween>
    </form>
  </Container>;
}

function ZoneList({api, choose, returning}) {
  const {t} = useLocale();
  const [rows, setRows] = useState(null);
  const heading = useRef(null);
  const {busy, error, run} = useOperation();
  const load = useCallback(() => run(async signal => {
    setRows(null);
    const zones = await api.zones(signal);
    signal.throwIfAborted();
    setRows(zones);
  }), [api, run]);
  useEffect(() => { load(); }, [load]);
  useEffect(() => { if (returning) heading.current?.focus(); }, [returning]);
  return <SpaceBetween size="l">
    {error && <Alert type="error">{error}</Alert>}
    <Table items={rows ?? []} loading={busy} loadingText={t('Loading hosted zones', 'ホストゾーンを読み込み中')}
      ariaLabels={{tableLabel:t('Public hosted zones', '公開ホストゾーン')}} header={
        <div tabIndex={-1} ref={heading}><Header variant="h2" actions={
          <Button disabled={busy} onClick={load}>{t('Refresh hosted zones', 'ホストゾーンを再読み込み')}</Button>
        }>{t('Public hosted zones', '公開ホストゾーン')}</Header></div>}
      empty={rows ? t('No local zones. Create a public hosted zone with the native API first.', 'ローカルゾーンはありません。先にネイティブAPIで公開ホストゾーンを作成してください。')
        : t('No hosted zones loaded.', 'ホストゾーンを取得していません。')}
      columnDefinitions={[
        {id:'name', header:t('Name', '名前'), cell:zone =>
          <Button variant="inline-link" disabled={busy} onClick={() => choose(zone)}>{identifier(zone.name)}</Button>},
        {id:'id', header:t('Hosted zone ID', 'ホストゾーンID'), cell:zone => identifier(zone.id)},
        {id:'count', header:t('Record sets', 'レコードセット数'), cell:zone => zone.count},
      ]}/>
  </SpaceBetween>;
}

function SelectedZone({api, selected, back, onSaving}) {
  const {t} = useLocale();
  const heading = useRef(null), verification = useRef(null), refresh = useRef(null), recovery = useRef(null);
  const focusIntent = useRef(null);
  const [current, setCurrent] = useState(null), [notice, setNotice] = useState(null);
  const [draft, updateDraft] = useReducer(updatePTRDraft, initialPTRDraft);
  const {busy, error, run} = useOperation(onSaving);
  const load = useCallback((start = null) => run(async signal => {
    setCurrent(null);
    setNotice(null);
    const zone = await api.zone(selected.id, signal);
    if (zone.name !== selected.name) throw Error('The selected local Route 53 zone no longer matches.');
    const page = await api.records(zone.id, signal, start);
    signal.throwIfAborted();
    setCurrent({zone, page});
    updateDraft({type:'fresh'});
  }), [api, run, selected]);
  useEffect(() => { load(); }, [load]);
  useEffect(() => { heading.current?.focus(); }, []);
  useLayoutEffect(() => {
    if (busy || !focusIntent.current) return;
    const {origin, success} = focusIntent.current;
    focusIntent.current = null;
    focusPTRResult(origin, () => {
      if (success) verification.current?.focus();
      else if (refresh.current) refresh.current.focus();
      else recovery.current?.focus();
    });
  }, [busy, notice, draft.mustRefresh]);

  function create(fields, origin) {
    if (!current || draft.mustRefresh) return;
    run(async signal => {
      setNotice(null);
      const {changeID, page} = await api.createPTR(current.zone, fields, signal);
      signal.throwIfAborted();
      setCurrent({zone: current.zone, page});
      focusIntent.current = {origin, success:true};
      updateDraft({type:'verified'});
      setNotice({changeID, name:page.rows[0].name});
    }, true, () => {
      focusIntent.current = {origin, success:false};
      updateDraft({type:'unconfirmed'});
    });
  }
  const page = current?.page;
  return <SpaceBetween size="l">
    <div tabIndex={-1} ref={heading}><Header variant="h2" description={identifier(selected.id)}>{identifier(selected.name)}</Header></div>
    <Button disabled={busy} onClick={back}>{t('Back to hosted zones', 'ホストゾーン一覧へ戻る')}</Button>
    {error && <Alert type="error">{error}</Alert>}
    {draft.mustRefresh && <div tabIndex={-1} ref={recovery}>
      <Alert type="warning">{t('No save was confirmed. Refresh the records before submitting again.', '保存を確認できませんでした。再送信する前にレコードを再読み込みしてください。')}</Alert>
    </div>}
    {notice && <div tabIndex={-1} ref={verification}><Alert type="success">
      {t('Verified local PTR record:', 'ローカルPTRを確認しました:')} {identifier(notice.name)} {'('}{identifier(notice.changeID)}{')'}
    </Alert></div>}
    <Table items={page?.rows ?? []} loading={busy} loadingText={t('Working with local records', 'ローカルレコードを処理中')}
      ariaLabels={{tableLabel:t('Route 53 record sets', 'Route 53レコードセット')}}
      header={<Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
        <Button ref={refresh} loading={busy} onClick={() => load()}>{t('Refresh from first record', '先頭から再読み込み')}</Button>
        <Button disabled={busy || !page?.next} onClick={() => load(page?.next)}>{t('Next records', '次のレコード')}</Button>
      </SpaceBetween>}>{t('Record sets', 'レコードセット')}</Header>}
      empty={page ? t('No records on this page.', 'このページにレコードはありません。') : t('No records loaded.', 'レコードを取得していません。')}
      columnDefinitions={[
        {id:'name', header:t('Name', '名前'), cell:row => identifier(row.name)},
        {id:'type', header:t('Type', '種類'), cell:row => row.type},
        {id:'ttl', header:'TTL', cell:row => row.ttl},
        {id:'values', header:t('Values', '値'), cell:row => row.values.map(value => <div key={value}>{identifier(value)}</div>)},
      ]}/>
    {current && <Route53PTRForm zone={current.zone.name} fields={draft.fields} busy={busy} mustRefresh={draft.mustRefresh}
      onChange={(field, value) => updateDraft({type:'edit', field, value})} onSave={create}/>}
  </SpaceBetween>;
}

function AuthorizedRoute53({request, onSaving}) {
  const {t} = useLocale();
  const api = useMemo(() => route53Client(request), [request]);
  const [selected, setSelected] = useState(null), [returning, setReturning] = useState(false);
  const back = () => {setReturning(true);setSelected(null);};
  return <ContentLayout header={<Header variant="h1" description={t(
    'Existing local public zones only. View record sets and create one PTR. No cloud DNS, zone changes, record replacement or deletion.',
    '既存のローカル公開ゾーンのみ。レコードの参照とPTRの新規作成に対応します。外部DNS・ゾーン変更・上書き・削除は対象外です。')}>Route 53</Header>}>
    {selected ? <SelectedZone key={selected.id} api={api} selected={selected} back={back} onSaving={onSaving}/>
      : <ZoneList api={api} choose={setSelected} returning={returning}/>}
  </ContentLayout>;
}

export default function Route53Console({request, capabilities, authenticationRequired, onSaving}) {
  const {t} = useLocale();
  if (!route53ConsoleAvailable(capabilities, authenticationRequired)) {
    return <ContentLayout header={<Header variant="h1">Route 53</Header>}>
      <Alert type="info">{t('This Route 53 console requires a normal signed-in local session.', 'Route 53コンソールには通常のローカルログインが必要です。')}</Alert>
    </ContentLayout>;
  }
  return <AuthorizedRoute53 request={request} onSaving={onSaving}/>;
}
