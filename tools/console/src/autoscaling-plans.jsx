import React, {useEffect, useLayoutEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import RadioGroup from '@cloudscape-design/components/radio-group';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {capacityBounds, dimensions, newPlan, scalingPlanClient} from './autoscaling-plans.mjs';
import {useLocale} from './i18n.jsx';

const emptyForm = {name: '', tag: '', tagValue: '', table: '', dimension: 'read', minimum: '', maximum: '', confirmation: ''};

export default function AutoScalingPlans({region, request, onSaving}) {
  const {t} = useLocale();
  const api = useMemo(() => scalingPlanClient(request, region), [request, region]);
  const inflight = useRef(null);
  const [busy, setBusy] = useState('');
  const [rows, setRows] = useState([]);
  const [pageToken, setPageToken] = useState('');
  const [selected, setSelected] = useState('');
  const [detail, setDetail] = useState(null);
  const [capacity, setCapacity] = useState(null);
  const [mode, setMode] = useState('');
  const [form, setForm] = useState(emptyForm);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [uncertain, setUncertain] = useState('');
  const change = (key, value) => setForm(old => ({...old, [key]: value}));
  const readLabel = t('Read capacity units', '読み込みキャパシティーユニット');
  const writeLabel = t('Write capacity units', '書き込みキャパシティーユニット');
  const title = t('Scaling plans', 'スケーリングプラン');

  useLayoutEffect(() => () => {
    inflight.current?.abort();
    inflight.current = null;
    onSaving(false);
  }, [api, onSaving]);

  function start(kind) {
    inflight.current?.abort();
    const task = new AbortController();
    inflight.current = task;
    setBusy(kind);
    setError('');
    return task;
  }
  function finish(task) {
    if (task !== inflight.current) return;
    inflight.current = null;
    setBusy('');
    onSaving(false);
  }
  function cancelRead() {
    if (busy !== 'read') return;
    inflight.current?.abort();
    inflight.current = null;
    setBusy('');
  }
  async function browse(name = '', token = '') {
    const task = start('read');
    setMode('');
    setSelected(name);
    setDetail(null);
    setCapacity(null);
    if (!token) { setRows([]); setPageToken(''); }
    try {
      if (name) {
        const result = await api.detail(name, task.signal);
        if (task.signal.aborted) return;
        setDetail(result);
        if (!result) setError(t('This plan was not found in this region.', 'このリージョンにプランが見つかりませんでした。'));
      } else {
        const page = await api.list(token, task.signal);
        if (task.signal.aborted) return;
        setRows(old => token ? [...old, ...page.ScalingPlans] : page.ScalingPlans);
        setPageToken(page.NextToken || '');
      }
      if (name) setUncertain(previous => previous === name ? '' : previous);
    } catch (e) { if (!task.signal.aborted) setError(e.message); }
    finally { finish(task); }
  }
  useEffect(() => { browse(); }, [api]);

  async function observe(table) {
    const task = start('read');
    setCapacity(null);
    try {
      const values = await api.capacity(table, task.signal);
      if (!task.signal.aborted) setCapacity({table, ...values});
    } catch (e) { if (!task.signal.aborted) setError(e.message); }
    finally { finish(task); }
  }
  function open(next) {
    const target = detail?.resources[0];
    setForm(next === 'edit'
      ? {...emptyForm, minimum: String(target.MinCapacity), maximum: String(target.MaxCapacity)}
      : emptyForm);
    setMode(next);
    setError('');
    setNotice('');
  }
  const createBody = mode === 'create' ? newPlan(form) : null;
  const canSubmit = mode === 'create' ? !!createBody : mode === 'edit'
    ? !!capacityBounds(form.minimum, form.maximum) : !!selected && form.confirmation === selected;

  async function submit(event) {
    event.preventDefault();
    if (inflight.current || uncertain || !canSubmit) return;
    const task = start('write');
    onSaving(true);
    let sent = false;
    let acknowledged = false;
    const name = mode === 'create' ? form.name : selected;
    try {
      if (mode !== 'create') {
        const latest = await api.detail(name, task.signal);
        if (JSON.stringify(detail) !== JSON.stringify(latest)) {
          setUncertain(name);
          throw Error(t('The plan changed. Refresh before editing it.', 'プランが変更されました。更新してから操作してください。'));
        }
      }
      task.signal.throwIfAborted();
      sent = true;
      if (mode === 'create') await api.create(createBody, task.signal);
      else if (mode === 'edit') await api.update(detail.plan, form.minimum, form.maximum, task.signal);
      else await api.remove(name, task.signal);
      task.signal.throwIfAborted();
      acknowledged = true;
      setNotice(mode === 'delete' ? t('Plan deleted. The table keeps its capacity.', 'プランを削除しました。テーブルの容量は維持されます。')
        : mode === 'edit' ? t('Capacity bounds updated.', '容量の範囲を更新しました。') : t('Scaling plan created.', 'スケーリングプランを作成しました。'));
      onSaving(false);
      await browse(mode === 'delete' ? '' : name);
    } catch (e) {
      if (task.signal.aborted) return;
      if (sent && !acknowledged && !/^(400|403|404):/.test(e.message)) {
        setUncertain(name);
        setMode('');
        setSelected(name);
        setDetail(null);
        setCapacity(null);
        setError(t('The write outcome is unknown. Refresh to check the plan.', '書き込み結果を確認できませんでした。更新してプランを確認してください。'));
      } else { setError(e.message); }
    } finally { finish(task); }
  }
  const button = (label, action, options = {}) => <Button formAction="none" disabled={!!busy} onClick={action} {...options}>{label}</Button>;
  const field = (key, label, description) => <FormField label={label} description={description}><Input value={form[key]} disabled={busy === 'write'} onChange={e => change(key, e.detail.value)}/></FormField>;
  const nameLabel = t('Plan name', 'プラン名');
  const tableLabel = t('DynamoDB table', 'DynamoDB テーブル');
  const statusLabel = t('Status', 'ステータス');
  const boundsFields = <>
    {field('minimum', t('Minimum capacity', '最小容量'), t('An integer from 1 to 40,000.', '1 ～ 40,000 の整数。'))}
    {field('maximum', t('Maximum capacity', '最大容量'), t('At least the minimum and at most 40,000.', '最小容量以上、40,000 以下。'))}
  </>;
  const newLabel = t('Create scaling plan', 'スケーリングプランを作成');
  const editLabel = t('Edit capacity bounds', '容量の範囲を編集');
  const deleteLabel = t('Delete plan', 'プランを削除');
  const currentTitle = mode === 'create' ? newLabel : mode === 'edit' ? editLabel : mode === 'delete' ? deleteLabel : selected || title;
  const selectedTable = detail?.resources[0].ResourceId.slice(6);
  const actions = <SpaceBetween direction="horizontal" size="xs">
    {selected && button(title, () => { setNotice(''); browse(); }, {disabled:!!busy || !!uncertain})}
    {busy === 'read' ? <Button onClick={cancelRead}>{t('Cancel read', '読み込みをキャンセル')}</Button>
      : button(t('Refresh', '更新'), () => browse(selected), {iconName:'refresh'})}
    {!selected && button(newLabel, () => open('create'), {variant:'primary', disabled:!!busy || !!uncertain})}
    {detail && button(deleteLabel, () => open('delete'), {disabled:!!busy || !!uncertain})}
    {detail?.resources.length === 1 && button(editLabel, () => open('edit'), {variant:'primary', disabled:!!busy || !!uncertain})}
  </SpaceBetween>;
  return <ContentLayout header={<Header variant="h1" actions={!mode && actions}>{currentTitle}</Header>}>
    <SpaceBetween size="l">
      {error && <Alert type="error" header={t('Request failed', 'リクエストに失敗しました')}>{error}</Alert>}
      {notice && <Alert type="success">{notice}</Alert>}
      {mode ? <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        {button(t('Cancel', 'キャンセル'), () => { setMode(''); setError(''); })}
        <Button variant="primary" formAction="submit" loading={busy === 'write'} disabled={!canSubmit || !!uncertain}>{mode === 'delete' ? deleteLabel : mode === 'edit' ? t('Save changes', '変更を保存') : newLabel}</Button>
      </SpaceBetween>}><Container><SpaceBetween size="m">
        {mode === 'create' && <>
          {field('name', nameLabel, t('Up to 128 printable ASCII characters. Omit |, : and /.', '半角英数字・記号で 128 文字以内。|、:、/ は使用できません。'))}
          {field('tag', t('Source tag key', 'ソースタグのキー'), t('The tag identifies the application source. Specify the target table separately.', 'タグはアプリケーションソースを識別します。対象テーブルは別途指定してください。'))}
          {field('tagValue', t('Source tag value', 'ソースタグの値'))}
          {field('table', tableLabel, t('Enter an existing provisioned table in this region.', 'このリージョンにあるプロビジョンドテーブル名を入力してください。'))}
          <FormField label={t('Capacity dimension', '容量の種類')}><RadioGroup value={form.dimension} onChange={e => change('dimension', e.detail.value)} items={[{value:'read', label:readLabel, disabled:!!busy},{value:'write', label:writeLabel, disabled:!!busy}]}/></FormField>
        </>}
        {mode === 'edit' && <Box>{selected} · {selectedTable} · {detail.resources[0].ScalableDimension === dimensions.read ? readLabel : writeLabel}</Box>}
        {mode !== 'delete' ? boundsFields : field('confirmation', t('Confirm plan name', 'プラン名を確認'), t('Enter the exact name. Deleting the plan leaves the table and its capacity in place.', 'プラン名を正確に入力してください。削除後もテーブルと現在の容量は維持されます。') + ' ' + selected)}
      </SpaceBetween></Container></Form></form> : !selected ? <Table
        header={<Header variant="h2">{t('Plans in this region', 'このリージョンのプラン')}</Header>}
        loading={busy === 'read'} loadingText={t('Loading plans', 'プランの読み込み中')} items={rows}
        columnDefinitions={[
          {id:'name', header:nameLabel, cell:p => button(p.ScalingPlanName, () => { setNotice(''); browse(p.ScalingPlanName); }, {variant:'inline-link'})},
          {id:'status', header:statusLabel, cell:p => p.StatusCode},
          {id:'targets', header:t('Targets', 'ターゲット数'), cell:p => p.ScalingInstructions.length}
        ]}
        empty={<Box textAlign="center">{t('No scaling plans found.', 'スケーリングプランが見つかりません。')}</Box>}
        footer={pageToken && button(t('Load more plans', 'プランをさらに読み込む'), () => browse('', pageToken))}
      /> : detail ? <>
        <Container header={<Header variant="h2">{t('Plan details', 'プランの詳細')}</Header>}>
          <KeyValuePairs columns={2} items={[
            {label:statusLabel, value:detail.plan.StatusCode},
            {label:t('Application source', 'アプリケーションソース'), value:detail.plan.ApplicationSource.CloudFormationStackARN ||
              detail.plan.ApplicationSource.TagFilters.map(tag => tag.Key + (tag.Values?.length ? ' = ' + tag.Values.join(', ') : '')).join(' · ')}
          ]}/>
        </Container>
        <Table items={detail.resources} wrapLines header={<Header variant="h2" description={t('Create and update apply the bounds once. Stored tracking policies are not evaluated.', '作成・更新時に容量の範囲を一度適用します。保存済みの追跡ポリシーは評価されません。')}>{t('Scalable resources', 'スケーラブルリソース')}</Header>}
          columnDefinitions={[
            {id:'table', header:tableLabel, cell:r => r.ResourceId.slice(6)},
            {id:'dimension', header:t('Capacity dimension', '容量の種類'), cell:r => r.ScalableDimension === dimensions.read ? readLabel : writeLabel},
            {id:'range', header:t('Capacity bounds', '容量の範囲'), cell:r => r.MinCapacity + ' – ' + r.MaxCapacity},
            {id:'status', header:statusLabel, cell:r => r.state},
            {id:'policies', header:t('Stored policies', '保存済みポリシー数'), cell:r => r.policies},
            {id:'capacity', header:t('Table capacity', 'テーブル容量'), cell:r => button(t('Read capacity', '容量を取得'), () => observe(r.ResourceId.slice(6)), {ariaLabel:t('Read capacity for ', '容量を取得：') + r.ResourceId.slice(6)})}
          ]}/>
        {capacity && <Container header={<Header variant="h2">{t('Observed table capacity', '取得したテーブル容量')}</Header>}>
          <KeyValuePairs columns={3} items={[{label:tableLabel, value:capacity.table}, {label:readLabel, value:String(capacity.ReadCapacityUnits)}, {label:writeLabel, value:String(capacity.WriteCapacityUnits)}]}/>
        </Container>}
      </> : busy === 'read' && <Box role="status">{t('Loading plan', 'プランの読み込み中')}</Box>}
    </SpaceBetween>
  </ContentLayout>;
}
