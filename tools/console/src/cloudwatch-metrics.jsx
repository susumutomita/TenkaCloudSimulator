import React, {useEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import LineChart from '@cloudscape-design/components/line-chart';
import Pagination from '@cloudscape-design/components/pagination';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import {useLocale} from './i18n.jsx';
import {metricStatisticLabel} from './metrics-presentation.mjs';

// Original read-only local projection of ListMetrics/GetMetricStatistics.
// Request is the parent's authenticated HTTP seam, never an independent fetch.
// Parent must key the view by its session/navigation authority, not locale.
const namespaceURI = 'http://monitoring.amazonaws.com/doc/2010-08-01/';
const stats = ['Average', 'Sum', 'Minimum', 'Maximum', 'SampleCount'];
const units = ['Seconds', 'Microseconds', 'Milliseconds', 'Bytes', 'Kilobytes', 'Megabytes', 'Gigabytes', 'Terabytes', 'Bits', 'Kilobits', 'Megabits', 'Gigabits', 'Terabits', 'Percent', 'Count', 'Bytes/Second', 'Kilobytes/Second', 'Megabytes/Second', 'Gigabytes/Second', 'Terabytes/Second', 'Bits/Second', 'Kilobits/Second', 'Megabits/Second', 'Gigabits/Second', 'Terabits/Second', 'Count/Second', 'None'];
const identity = m => JSON.stringify([m.namespace, m.name, m.dimensions]);
const members = node => Array.from(node.children).filter(n => n.localName === 'member' && n.namespaceURI === namespaceURI);
function child(node, name, optional = false) {
 const found = Array.from(node.children).filter(n => n.localName === name && n.namespaceURI === namespaceURI);
 if (found.length === 1) return found[0];
 if (optional && found.length === 0) return null;
 throw Error('Invalid native metric response.');
}
const text = (node, name) => child(node, name).textContent;
const safeError = error => error instanceof Error && error.name === 'AbortError' ? '' : 'The metric request failed. Refresh or retry the read; no metric data was changed.';

export default function CloudWatchMetricsView({region, principal = '', request}) {
 const {locale, t} = useLocale();
 const lifeRef = useRef(null), scopeRef = useRef(null), graphStartRef = useRef(null), focusGraphRef = useRef(false);
 scopeRef.current = {region, principal, request};
 const [items, setItems] = useState([]), [selected, setSelected] = useState([]), [points, setPoints] = useState([]);
 const [namespace, setNamespace] = useState(''), [metricName, setMetricName] = useState('');
 const [token, setToken] = useState(''), [pageFilters, setPageFilters] = useState({});
 const [listing, setListing] = useState(false), [reading, setReading] = useState(false), [error, setError] = useState('');
 const [tab, setTab] = useState('browse'), [statistic, setStatistic] = useState({label:'Average', value:'Average'});
 const [unit, setUnit] = useState({label:'All native units', value:''}), [period, setPeriod] = useState('60');
 const [start, setStart] = useState(() => new Date(Date.now() - 3600000).toISOString());
 const [end, setEnd] = useState(() => new Date().toISOString());
 const [loadedQuery, setLoadedQuery] = useState(null), [pointPage, setPointPage] = useState(1);
 const current = (life, kind, revision) => lifeRef.current === life && life.active && life.region === scopeRef.current.region && life.principal === scopeRef.current.principal && life.request === scopeRef.current.request && life[kind] === revision;
 function invalidateStatistics(life) {
  if (life) { life.statistics++; life.statisticsController?.abort(); }
  setPoints([]); setLoadedQuery(null); setPointPage(1); setReading(false);
 }
 async function call(life, action, fields, signal) {
  const form = new URLSearchParams({Action:action, Version:'2010-08-01', ...fields});
  const response = await life.request('/', {method:'POST', signal,
   headers:{'Content-Type':'application/x-www-form-urlencoded; charset=utf-8', 'X-Simulator-Service':'monitoring', 'X-Simulator-Region':life.region}, body:form.toString()});
  const body = await response.text();
  if (body.length > 2097152) throw Error('Invalid native metric response.');
  const doc = new DOMParser().parseFromString(body, 'application/xml');
  const root = doc.documentElement;
  if (doc.querySelector('parsererror') || root.namespaceURI !== namespaceURI || !response.ok || root.localName !== action + 'Response') throw Error('Metric read refused.');
  return child(root, action + 'Result');
 }
 async function browse(life = lifeRef.current, filters = {}, next = '') {
  if (!life?.active || lifeRef.current !== life) return;
  const revision = ++life.list;
  life.listController?.abort(); life.listController = new AbortController();
  invalidateStatistics(life); setSelected([]); setItems([]); setToken(''); setListing(true); setError('');
  try {
   const result = await call(life, 'ListMetrics', {...filters, ...(next ? {NextToken:next} : {})}, life.listController.signal);
   if (!current(life, 'list', revision)) return;
   const rows = members(child(result, 'Metrics')).map(n => ({namespace:text(n, 'Namespace'), name:text(n, 'MetricName'), dimensions:members(child(n, 'Dimensions')).map(d => ({Name:text(d,'Name'), Value:text(d,'Value')}))}));
   if (rows.length > 500 || rows.some(m => !m.namespace || !m.name || m.dimensions.length > 30 || new Set(m.dimensions.map(d=>d.Name)).size !== m.dimensions.length || (filters.Namespace && m.namespace !== filters.Namespace) || (filters.MetricName && m.name !== filters.MetricName)) || new Set(rows.map(identity)).size !== rows.length) throw Error('Invalid native metric list.');
   const continuation = child(result, 'NextToken', true)?.textContent || '';
   if (continuation.length > 8192 || (next && continuation === next)) throw Error('Invalid native continuation.');
   setItems(rows); setToken(continuation); setPageFilters({...filters});
  } catch (e) { if (current(life, 'list', revision)) setError(safeError(e)); }
  finally { if (current(life, 'list', revision)) setListing(false); }
 }
 async function readStatistics(event) {
  event?.preventDefault();
  const life = lifeRef.current, metric = selected[0];
  if (!life?.active || !metric || listing) return;
  const revision = ++life.statistics;
  life.statisticsController?.abort(); life.statisticsController = new AbortController();
  setPoints([]); setLoadedQuery(null); setPointPage(1); setError(''); setReading(true);
  try {
   // Require explicit UTC/offset times; do not reinterpret a user's wall clock.
   if (!/^\d{4}-\d\d-\d\dT.*(?:Z|[+-]\d\d:\d\d)$/.test(start) || !/^\d{4}-\d\d-\d\dT.*(?:Z|[+-]\d\d:\d\d)$/.test(end) || !Number.isFinite(Date.parse(start)) || !Number.isFinite(Date.parse(end)) || Date.parse(start) >= Date.parse(end) || !/^[1-9]\d*$/.test(period) || Number(period) < 60 || Number(period) % 60) throw Error('Invalid read range.');
   const fields = {Namespace:metric.namespace, MetricName:metric.name, StartTime:start, EndTime:end, Period:period};
   metric.dimensions.forEach((d,i) => { fields[`Dimensions.member.${i+1}.Name`] = d.Name; fields[`Dimensions.member.${i+1}.Value`] = d.Value; });
   stats.forEach((s,i) => { fields[`Statistics.member.${i+1}`] = s; });
   if (unit.value) fields.Unit = unit.value;
   const result = await call(life, 'GetMetricStatistics', fields, life.statisticsController.signal);
   if (!current(life, 'statistics', revision)) return;
   if (text(result, 'Label') !== metric.name) throw Error('Wrong native metric.');
   const rows = members(child(result, 'Datapoints')).map((n,i) => {
    const row = {id:i, Timestamp:text(n,'Timestamp'), Unit:text(n,'Unit')};
    if (!Number.isFinite(Date.parse(row.Timestamp)) || Date.parse(row.Timestamp) < Math.floor(Date.parse(start)/60000)*60000 || Date.parse(row.Timestamp) >= Date.parse(end) || !units.includes(row.Unit)) throw Error('Invalid native datapoint.');
    for (const s of stats) { const raw=text(n,s); row[s]=Number(raw); if (!raw.trim() || !Number.isFinite(row[s])) throw Error('Invalid native statistic.'); }
    return row;
   }).sort((a,b) => Date.parse(a.Timestamp) - Date.parse(b.Timestamp) || a.Unit.localeCompare(b.Unit));
   if (rows.length > 1440 || new Set(rows.map(p => JSON.stringify([p.Timestamp,p.Unit]))).size !== rows.length || rows.some(p => unit.value && p.Unit !== unit.value)) throw Error('Invalid native datapoint page.');
   setPoints(rows); setLoadedQuery({metric:identity(metric), start, end, period});
  } catch (e) { if (current(life, 'statistics', revision)) setError(safeError(e)); }
  finally { if (current(life, 'statistics', revision)) setReading(false); }
 }
 useEffect(() => {
  focusGraphRef.current=false;
  const life = {active:true, region, principal, request, list:0, statistics:0}; lifeRef.current = life;
  setTab('browse'); setError(''); setItems([]); setSelected([]); setToken(''); setPageFilters({}); setNamespace(''); setMetricName('');
  browse(life);
  return () => { life.active=false; life.listController?.abort(); life.statisticsController?.abort(); if (lifeRef.current===life) lifeRef.current=null; };
 }, [region, principal, request]);
 // An external tab transition must not leave keyboard focus in a hidden panel.
 function openGraph() { focusGraphRef.current=true; setTab('graphed'); }
 useEffect(() => {
  if (tab==='graphed' && selected.length && focusGraphRef.current) {
   focusGraphRef.current=false; graphStartRef.current?.focus();
  }
 }, [tab, selected]);
 const series = useMemo(() => [...new Set(points.map(p => p.Unit))].map(u => ({title:`${metricStatisticLabel(statistic.value, locale)} (${u})`, type:'line', data:points.filter(p => p.Unit===u).map(p => ({x:new Date(p.Timestamp), y:p[statistic.value]}))})), [points, statistic.value, locale]);
 const changeQuery = setter => value => { invalidateStatistics(lifeRef.current); setter(value); };
 const actions = <Button disabled={listing} onClick={() => browse(lifeRef.current, pageFilters)}>{t('Refresh metrics','メトリクスを更新')}</Button>;
 return <SpaceBetween size="l">
  <Alert type="info">{t('Unofficial local CloudWatch Metrics simulator. Read-only browser for explicitly ingested raw custom metrics; no automatic AWS/Lambda delivery, alarms, dashboards, metric math or percentiles. Regional local retention is 15 days; listing includes the last 14 days.','非公式のローカル CloudWatch Metrics シミュレーターです。明示的に取り込まれた生のカスタムメトリクスを参照します。AWS/Lambda の自動配信、アラーム、ダッシュボード、メトリクス数式、パーセンタイルは未対応です。ローカル保持期間はリージョンごとに15日、一覧の対象は直近14日です。')}</Alert>
  <Header variant="h1" actions={actions}>{t('Metrics','メトリクス')}</Header>
  {error && <Alert type="error">{t(error, 'メトリクスの読み取りに失敗しました。更新または読み取りを再試行してください。メトリクスデータは変更されていません。')}</Alert>}
  <Tabs activeTabId={tab} onChange={({detail}) => setTab(detail.activeTabId)} tabs={[
   {id:'browse', label:t('Browse','参照'), content:<SpaceBetween size="m">
    <Container header={<Header variant="h2">{t('All metrics','すべてのメトリクス')}</Header>}>
     <form onSubmit={e => {e.preventDefault(); browse(lifeRef.current, {...(namespace ? {Namespace:namespace} : {}), ...(metricName ? {MetricName:metricName} : {})});}}>
      <SpaceBetween size="m"><FormField label={t('Namespace (exact, optional)','名前空間（完全一致、省略可）')}><Input value={namespace} onChange={({detail}) => setNamespace(detail.value)}/></FormField>
       <FormField label={t('Metric name (exact, optional)','メトリクス名（完全一致、省略可）')}><Input value={metricName} onChange={({detail}) => setMetricName(detail.value)}/></FormField>
       <Button formAction="submit" disabled={listing}>{t('Browse metrics','メトリクスを参照')}</Button></SpaceBetween>
     </form>
    </Container>
    <Box variant="small">{t('Applied exact filters','適用中の完全一致フィルター')}: {pageFilters.Namespace || t('All namespaces','すべての名前空間')} / {pageFilters.MetricName || t('All metric names','すべてのメトリクス名')}</Box>
    <Table wrapLines loading={listing} items={items} selectionType="single" selectedItems={selected} trackBy={identity}
     onSelectionChange={({detail}) => {invalidateStatistics(lifeRef.current); setSelected(detail.selectedItems);}}
     ariaLabels={{tableLabel:t('Native metrics in current page','現在のページのネイティブメトリクス'), selectionGroupLabel:t('Select one metric','メトリクスを1つ選択'), itemSelectionLabel:(_, item) => `${item.namespace}: ${item.name} — ${item.dimensions.map(d=>`${d.Name}=${d.Value}`).join(', ') || t('No dimensions','ディメンションなし')}`}}
     columnDefinitions={[{id:'namespace',header:t('Namespace','名前空間'),cell:m=>m.namespace},{id:'name',header:t('Metric name','メトリクス名'),cell:m=>m.name},{id:'dimensions',header:t('Dimensions','ディメンション'),cell:m=>m.dimensions.map(d=>`${d.Name}=${d.Value}`).join(', ') || t('No dimensions','ディメンションなし')}]}
     empty={<Box padding="m">{t('No metrics on this page. Publish raw data through the native API first, or change the exact filters.','このページにメトリクスはありません。ネイティブ API で生データを送信するか、完全一致フィルターを変更してください。')}</Box>}/>
    <SpaceBetween direction="horizontal" size="xs"><Button disabled={listing || !token} onClick={()=>browse(lifeRef.current,pageFilters,token)}>{t('Next page','次のページ')}</Button><Button disabled={!selected.length || listing} onClick={openGraph}>{t('Graph selected metric','選択したメトリクスをグラフ化')}</Button></SpaceBetween>
   </SpaceBetween>},
   {id:'graphed',label:t('Graphed metrics','グラフ化したメトリクス'),content:!selected.length ? <Box padding="l">{t('Select one metric in Browse.','「参照」でメトリクスを1つ選択してください。')}</Box> : <SpaceBetween size="m">
    <KeyValuePairs columns={1} items={[{label:t('Namespace','名前空間'),value:selected[0].namespace},{label:t('Metric name','メトリクス名'),value:selected[0].name},{label:t('Dimensions','ディメンション'),value:selected[0].dimensions.map(d=>`${d.Name}=${d.Value}`).join(', ') || t('No dimensions','ディメンションなし')},{label:t('Region','リージョン'),value:region}]}/>
    <Container><form onSubmit={readStatistics}><SpaceBetween size="m">
     <FormField label={t('Start time (ISO8601 with UTC/offset)','開始日時（UTC/オフセット付き ISO8601）')}><Input ref={graphStartRef} value={start} onChange={({detail})=>changeQuery(setStart)(detail.value)}/></FormField>
     <FormField label={t('End time (exclusive, ISO8601)','終了日時（含まない、ISO8601）')}><Input value={end} onChange={({detail})=>changeQuery(setEnd)(detail.value)}/></FormField>
     <FormField label={t('Period (seconds, multiple of 60)','期間（秒、60の倍数）')} description={t('At most 1,440 time buckets and 1,440 returned unit/time datapoints. Filter the unit or increase the period if needed. Start must be within local 15-day retention.','時間バケット数・単位と時刻別の返却データポイント数はどちらも最大1,440です。必要に応じて単位を絞るか期間を長くしてください。開始日時はローカル保持期間15日以内で指定してください。')}><Input value={period} onChange={({detail})=>changeQuery(setPeriod)(detail.value)}/></FormField>
     <FormField label={t('Unit filter','単位フィルター')}><Select selectedOption={{...unit,label:unit.value || t('All native units','すべてのネイティブ単位')}} options={[{label:t('All native units','すべてのネイティブ単位'),value:''},...units.map(u=>({label:u,value:u}))]} onChange={({detail})=>changeQuery(setUnit)(detail.selectedOption)}/></FormField>
     <Button formAction="submit" loading={reading} disabled={listing}>{t('Read statistics','統計を読み取る')}</Button>
    </SpaceBetween></form></Container>
    <FormField label={t('Graph statistic','グラフの統計')}><Select selectedOption={{...statistic,label:metricStatisticLabel(statistic.value, locale)}} options={stats.map(s=>({label:metricStatisticLabel(s, locale),value:s}))} onChange={({detail})=>setStatistic(detail.selectedOption)}/></FormField>
    <LineChart series={series} xScaleType="time" yScaleType="linear" height={260} ariaLabel={t('Metric statistic by UTC time and unit','UTC 時刻・単位ごとのメトリクス統計')} xTitle={t('Time','時刻')} yTitle={metricStatisticLabel(statistic.value, locale)} statusType={reading?'loading':'finished'} empty={<Box>{t('No datapoints loaded. Read statistics; missing data is not zero.','データポイントは読み込まれていません。統計を読み取ってください。欠損データはゼロではありません。')}</Box>}/>
    {loadedQuery && <Box variant="small">{t('Last completed read','最後に完了した読み取り')}: {loadedQuery.start} → {loadedQuery.end} ({loadedQuery.period}s)</Box>}
    <Table wrapLines loading={reading} items={points.slice((pointPage-1)*50,pointPage*50)} pagination={<Pagination currentPageIndex={pointPage} pagesCount={Math.max(1,Math.ceil(points.length/50))} onChange={({detail})=>setPointPage(detail.currentPageIndex)} ariaLabels={{paginationLabel:t('Statistic pages','統計のページ'),nextPageLabel:t('Next page','次のページ'),previousPageLabel:t('Previous page','前のページ'),pageLabel:n=>String(n)}}/>} trackBy="id" ariaLabels={{tableLabel:t('Native statistic values','ネイティブ統計値')}} columnDefinitions={[{id:'time',header:t('Timestamp (UTC)','タイムスタンプ（UTC）'),cell:p=>p.Timestamp},{id:'unit',header:t('Unit','単位'),cell:p=>p.Unit},...stats.map(s=>({id:s,header:metricStatisticLabel(s, locale),cell:p=>p[s]}))]} empty={<Box padding="m">{t('No returned datapoints.','返されたデータポイントはありません。')}</Box>}/>
   </SpaceBetween>}
  ]}/>
 </SpaceBetween>;
}
