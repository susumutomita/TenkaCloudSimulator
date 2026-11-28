import React, {useEffect, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {cloudWatchMetrics} from './cloudwatch-metrics.mjs';

export default function CloudWatchMetricsView({request, region}) {
  const {t} = useLocale();
  const current = useRef(null);
  const [rows, setRows] = useState(null);
  const [next, setNext] = useState('');
  const [name, setName] = useState('');
  const [detail, setDetail] = useState(null);
  const [measurement, setMeasurement] = useState(undefined);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const api = cloudWatchMetrics(request, region);

  function begin() {
    current.current?.abort();
    const controller = new AbortController();
    current.current = controller;
    setLoading(true); setError(''); setDetail(null); setMeasurement(undefined);
    return controller.signal;
  }

  async function browse(continuation = '') {
    const signal = begin();
    setName(''); setNext('');
    if (!continuation) setRows(null);
    try {
      const result = await api.list(continuation, signal);
      signal.throwIfAborted();
      const joined = continuation ? [...rows, ...result.alarms] : result.alarms;
      if (new Set(joined.map(row => row.arn)).size !== joined.length) throw new Error(t('The alarm list changed. Refresh it.', 'アラーム一覧を更新してください。'));
      setRows(joined); setNext(result.next);
    } catch (e) {
      if (!signal.aborted) {setRows(null); setNext(''); setError(e.message);}
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }

  async function open(selected) {
    const signal = begin();
    setRows(null); setNext(''); setName(selected);
    try {
      const alarm = await api.named(selected, signal);
      signal.throwIfAborted();
      if (!alarm) throw new Error(t('The alarm was not found in this region.', 'このリージョンにアラームが見つかりません。'));
      setDetail(alarm);
      const measured = await api.measurement(alarm, signal);
      signal.throwIfAborted(); setMeasurement(measured);
    } catch (e) {
      if (!signal.aborted) setError(e.message);
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }

  useEffect(() => {browse(); return () => current.current?.abort();}, [request, region]);
  const title = t('Metric alarms', 'メトリクスアラーム');
  return <SpaceBetween size="l">
    <Header variant="h1" actions={<Button onClick={() => browse()}>{t('Refresh alarm list', 'アラーム一覧を更新')}</Button>}>{title}</Header>
    <form onSubmit={event => {event.preventDefault(); if (name) open(name);}}>
      <SpaceBetween direction="horizontal" size="s">
        <FormField label={t('Exact alarm name', 'アラーム名（完全一致）')}><Input value={name} onChange={({detail}) => setName(detail.value)}/></FormField>
        <Button formAction="submit" disabled={!name}>{t('View alarm', 'アラームを表示')}</Button>
      </SpaceBetween>
    </form>
    {error && <Alert type="error">{error}</Alert>}
    {rows !== null && <Table items={rows} ariaLabels={{tableLabel: title}} columnDefinitions={[
      {id:'name', header:t('Alarm name','アラーム名'), cell:row=><Link onFollow={event=>{event.preventDefault();open(row.name);}} href="#/cloudwatch">{row.name}</Link>},
      {id:'state', header:t('State','状態'), cell:row=>row.state},
      {id:'metric', header:t('Metric','メトリクス'), cell:row=>row.metric},
    ]} empty={<Box textAlign="center">{t('No metric alarms','メトリクスアラームはありません')}</Box>}/>}
    {next && <Button disabled={loading} onClick={()=>browse(next)}>{t('Load more alarms','さらに読み込む')}</Button>}
    {loading && <Box role="status">{t('Loading CloudWatch data…','CloudWatch データを読み込み中…')}</Box>}
    {detail && <Container header={<Header variant="h2" actions={<Button onClick={()=>open(detail.name)}>{t('Refresh alarm','アラームを更新')}</Button>}>{detail.name}</Header>}>
      <KeyValuePairs columns={2} items={[
        {label:t('State','状態'),value:detail.state}, {label:t('State reason','状態の理由'),value:detail.reason},
        {label:'ARN', value:detail.arn}, {label:t('Namespace','名前空間'),value:detail.namespace},
        {label:t('Metric','メトリクス'),value:detail.metric}, {label:t('Dimensions','ディメンション'),value:detail.dimensions.map(d=>d.name+' = '+d.value).join(', ') || '—'},
        {label:t('Alarm condition','アラーム条件'),value:detail.statistic+' '+detail.operator+' '+detail.threshold+' ('+detail.unit+')'},
        {label:t('Evaluated UTC minute','評価対象の UTC 時間帯'),value:detail.start+' – '+detail.end},
        ...(measurement===undefined?[]:[{label:t('Measured statistic','測定された統計値'),value:measurement===null?t('No datapoints','データポイントはありません'):String(measurement)+' '+detail.unit}]),
      ]}/>
    </Container>}
  </SpaceBetween>;
}
