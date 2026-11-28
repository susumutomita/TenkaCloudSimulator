import React, {useContext, useEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {GCPConsoleContext} from './gcp-console-shell.jsx';
import {cloudTrace} from './gcp-trace.mjs';

function recentRange() {
  const end = Date.now();
  return {startTime: new Date(end - 3600000).toISOString(), endTime: new Date(end).toISOString()};
}
const exactText = value => <code style={{overflowWrap: 'anywhere'}}>{value}</code>;

export default function GCPTraceView({request, capability}) {
  const {t} = useLocale();
  const shell = useContext(GCPConsoleContext);
  const api = useMemo(() => cloudTrace(request, capability), [request, capability]);
  const [draft, setDraft] = useState(recentRange), [range, setRange] = useState(recentRange);
  const [items, setItems] = useState([]), [detail, setDetail] = useState(null);
  const [selected, setSelected] = useState(''), [page, setPage] = useState('list');
  const [tokens, setTokens] = useState(['']), [pageIndex, setPageIndex] = useState(0), [next, setNext] = useState('');
  const [loading, setLoading] = useState(true), [error, setError] = useState('');
  const pending = useRef(null);

  function cancel() { pending.current?.abort(); pending.current = null; }
  function begin() {
    cancel();
    const handle = new AbortController(); pending.current = handle;
    setError(''); setLoading(true);
    return handle;
  }
  const active = handle => pending.current === handle && !handle.signal.aborted;
  async function load(query, token = '', index = 0, history = ['']) {
    const handle = begin();
    setPage('list'); setSelected(''); setDetail(null); setItems([]); setNext('');
    setRange({...query}); setTokens(history); setPageIndex(index);
    try {
      const result = await api.list(query, token, handle.signal);
      if (active(handle)) { setItems(result.traces); setNext(result.nextPageToken); }
    } catch (cause) {
      if (active(handle)) setError(cause.message);
    } finally { if (active(handle)) setLoading(false); }
  }
  async function choose(id) {
    const handle = begin();
    setPage('detail'); setSelected(id); setDetail(null);
    try {
      const result = await api.get(id, handle.signal);
      if (active(handle)) setDetail(result);
    } catch (cause) {
      if (active(handle)) setError(cause.message);
    } finally { if (active(handle)) setLoading(false); }
  }
  function back() { cancel(); setPage('list'); setSelected(''); setDetail(null); setLoading(false); setError(''); }
  useEffect(() => {
    const initial = recentRange(); setDraft(initial); load(initial);
    return cancel;
  }, [api]);
  useEffect(() => { shell?.setProject(capability.project); }, [shell, capability.project]);
  useEffect(() => shell?.registerNavigation(() => load(range)), [shell, api, range]);

  const spanColumns = [
    {id: 'observation', header: t('Observation', '観測'), cell: row => row.observation},
    {id: 'spanId', header: t('Span ID (decimal)', 'スパン ID（10進数）'), cell: row => exactText(row.spanId)},
    {id: 'parentSpanId', header: t('Parent span ID', '親スパン ID'), cell: row => exactText(row.parentSpanId ?? '—')},
    {id: 'name', header: t('Name', '名前'), cell: row => <span style={{whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}}>{row.name}</span>},
    {id: 'kind', header: t('Kind', '種類'), cell: row => row.kind || '—'},
    {id: 'startTime', header: t('Start time', '開始時刻'), cell: row => exactText(row.startTime)},
    {id: 'endTime', header: t('End time', '終了時刻'), cell: row => exactText(row.endTime)},
    {id: 'labels', header: t('Labels (JSON)', 'ラベル（JSON）'), cell: row => <pre style={{margin: 0, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}}>{JSON.stringify(row.labels, null, 2)}</pre>},
  ];
  const header = <Header variant="h1" description={t('Read saved local trace observations. Submit traces through the SDK or API reference.', '保存済みのローカルトレースを参照します。送信には SDK または API リファレンスを使用してください。')}
    actions={<Link href={capability.reference}>{t('API reference', 'API リファレンス')}</Link>}>
    {page === 'list' ? t('Trace explorer', 'トレース エクスプローラ') : t('Trace details', 'トレースの詳細')}
  </Header>;
  return <ContentLayout header={header}>
    <SpaceBetween size="l">
      {error && <Alert type="error" header={t('Could not load trace data', 'トレースを取得できませんでした')}>{error}</Alert>}
      {page === 'list' ? <>
        <form onSubmit={event => { event.preventDefault(); load({...draft}); }}>
          <Form actions={<Button variant="primary" formAction="submit" loading={loading}>{t('Search traces', 'トレースを検索')}</Button>}>
            <Container header={<Header variant="h2">{t('Time range', '期間')}</Header>}>
              <ColumnLayout columns={2}>
                <FormField label={t('Start time (RFC 3339)', '開始時刻（RFC 3339）')} controlId="gcp-trace-start">
                  <Input value={draft.startTime} disabled={loading} onChange={({detail}) => setDraft(current => ({...current, startTime: detail.value}))}/>
                </FormField>
                <FormField label={t('End time (RFC 3339)', '終了時刻（RFC 3339）')} controlId="gcp-trace-end">
                  <Input value={draft.endTime} disabled={loading} onChange={({detail}) => setDraft(current => ({...current, endTime: detail.value}))}/>
                </FormField>
              </ColumnLayout>
            </Container>
          </Form>
        </form>
        <Table items={items} trackBy="traceId" loading={loading} loadingText={t('Loading traces', 'トレースを読み込み中')}
          wrapLines columnDefinitions={[{id: 'traceId', header: t('Trace ID', 'トレース ID'), cell: row =>
            <Button variant="link" onClick={() => choose(row.traceId)}>{row.traceId}</Button>}]}
          header={<Header variant="h2" counter={'(' + items.length + (next ? '+' : '') + ')'}
            description={range.startTime + ' — ' + range.endTime}
            actions={<Button iconName="refresh" disabled={loading} onClick={() => load(range)}>{t('Refresh traces', 'トレースを更新')}</Button>}>
            {t('Traces', 'トレース')}
          </Header>}
          empty={<Box textAlign="center">{error ? t('No traces loaded.', 'トレースは読み込まれていません。') : t('No saved traces in this interval.', 'この期間に保存されたトレースはありません。')}</Box>}
          footer={<SpaceBetween direction="horizontal" size="s">
            <Button disabled={loading || pageIndex === 0} onClick={() => load(range, tokens[pageIndex - 1], pageIndex - 1, tokens.slice(0, pageIndex))}>{t('Previous page', '前のページ')}</Button>
            <Box>{t('Page', 'ページ')} {pageIndex + 1}</Box>
            <Button disabled={loading || !next} onClick={() => load(range, next, pageIndex + 1, [...tokens.slice(0, pageIndex + 1), next])}>{t('Next page', '次のページ')}</Button>
          </SpaceBetween>}/>
      </> : <>
        <SpaceBetween direction="horizontal" size="s">
          <Button onClick={back}>{t('Back to traces', 'トレース一覧に戻る')}</Button>
          <Button iconName="refresh" disabled={loading} onClick={() => choose(selected)}>{t('Refresh trace', 'トレースを更新')}</Button>
        </SpaceBetween>
        <Box>{t('Trace ID', 'トレース ID')}: {exactText(selected)}</Box>
        <Table items={detail?.observations || []} trackBy="observation" columnDefinitions={spanColumns} wrapLines
          loading={loading} loadingText={t('Loading saved span observations', '保存済みスパンを読み込み中')}
          header={<Header variant="h2" counter={'(' + (detail?.observations.length || 0) + ')'}
            description={t('The full saved trace may include observations outside the list interval. Repeated span IDs are separate observations.', '一覧の期間外の観測も含む保存済みトレース全体です。同じスパン ID の観測も別々に表示します。')}>
            {t('Span observations', 'スパンの観測')}
          </Header>}
          empty={<Box textAlign="center">{t('No span observations loaded.', 'スパンの観測は読み込まれていません。')}</Box>}/>
      </>}
    </SpaceBetween>
  </ContentLayout>;
}
