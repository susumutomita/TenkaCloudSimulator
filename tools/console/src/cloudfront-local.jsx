import React, {useEffect, useLayoutEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {cloudFrontClient, cloudFrontTasks, confirmedRefusal, localObjectKey} from './cloudfront-local.mjs';
import {useLocale} from './i18n.jsx';

export default function CloudFrontLocal({request, onSaving}) {
  const {t} = useLocale();
  const api = useMemo(() => cloudFrontClient(request), [request]);
  const active = useMemo(() => cloudFrontTasks(), [api]);
  const modalOpener = useRef(null), createButton = useRef(null), replayButton = useRef(null), previousModal = useRef(false);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [rows, setRows] = useState([]);
  const [known, setKnown] = useState(false);
  const [marker, setMarker] = useState('');
  const [history, setHistory] = useState([]);
  const [next, setNext] = useState('');
  const [selected, setSelected] = useState('');
  const [detail, setDetail] = useState(null);
  const [key, setKey] = useState('');
  const [object, setObject] = useState(null);
  const [attempt, setAttempt] = useState(null);
  const [receipt, setReceipt] = useState(null);
  const [modal, setModal] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const title = t('Distributions', 'ディストリビューション');
  useLayoutEffect(() => () => {
    active.abort(); onSaving(false);
  }, [active, onSaving]);
  useEffect(() => {
    let frame;
    if (previousModal.current && !modal) frame = requestAnimationFrame(() => {
      if (modalOpener.current?.isConnected && !modalOpener.current.disabled) modalOpener.current.focus();
      else if (attempt?.uncertain) replayButton.current?.focus();
      else createButton.current?.focus();
    });
    previousModal.current = modal;
    return () => cancelAnimationFrame(frame);
  }, [modal]);
  function begin(kind) {
    const task = active.start(kind); if (!task) return null;
    setBusy(kind); setError('');
    if (kind === 'write') onSaving(true);
    return task;
  }
  const current = task => active.current(task);
  function finish(task) {
    if (!active.finish(task)) return;
    setBusy(''); onSaving(false);
  }
  function cancelRead() {
    if (active.cancelRead()) setBusy('');
  }
  async function read(operation, publish) {
    const task = begin('read');
    if (!task) return;
    try { const value = await operation(task.signal); if (current(task)) publish(value); }
    catch (e) { if (current(task)) setError(e.message); }
    finally { finish(task); }
  }
  async function browse(value = '', previous = []) {
    if (active.writing()) return;
    setSelected(''); setDetail(null); setObject(null); setKey(''); setReceipt(null); setAttempt(null); setModal(false);
    setRows([]); setKnown(false); setNext(''); setMarker(value); setHistory(previous);
    await read(signal => api.list(value, signal), result => { setRows(result.rows); setNext(result.next); setKnown(true); });
  }
  useEffect(() => { browse(); }, [api]);
  async function select(id, reset = true) {
    if (active.writing()) return;
    setSelected(id); setDetail(null); setObject(null);
    if (reset) { setKey(''); setReceipt(null); setAttempt(null); setModal(false); }
    await read(signal => api.detail(id, signal), setDetail);
  }
  function changeKey(value) {
    cancelRead(); setKey(value); setObject(null); setError('');
  }
  async function readObject() {
    const wanted = key; setObject(null);
    await read(signal => api.object(selected, wanted, signal), value => setObject({...value, key: wanted}));
  }
  function download() {
    if (!object || object.key !== key || busy) return;
    // An untrusted stored MIME never becomes an executable browser document.
    const url = URL.createObjectURL(new Blob([object.body], {type: 'application/octet-stream'}));
    const link = document.createElement('a'); link.href = url; link.download = 'cloudfront-object.bin';
    document.body.appendChild(link); link.click(); link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }
  function openInvalidation(replay = false) {
    if (busy) return;
    modalOpener.current = document.activeElement;
    if (!replay) setAttempt({distribution: selected, caller: 'console-' + crypto.randomUUID(), uncertain: false});
    setConfirmed(false); setModal(true); setError('');
  }
  async function invalidate() {
    if (!confirmed || !attempt || attempt.distribution !== selected) return;
    const task = begin('write'); if (!task) return;
    const original = attempt;
    setReceipt(null); setObject(null);
    try {
      const result = await api.invalidate(original.distribution, original.caller, task.signal);
      if (!current(task)) return;
      setReceipt(result); setAttempt({...original, uncertain: false}); setModal(false);
    } catch (e) {
      if (!current(task)) return;
      setAttempt({...original, uncertain: !confirmedRefusal(e)});
      setError(confirmedRefusal(e) ? e.message : t(
        'The invalidation outcome is unknown. Replay the same request explicitly to recover its receipt.',
        '無効化の結果を確認できません。同じリクエストを明示的に再送して結果を確認してください。'));
      setModal(false);
    } finally { finish(task); }
  }
  async function refreshReceipt() {
    const saved = receipt; setReceipt(null);
    await read(signal => api.invalidation(saved, signal), setReceipt);
  }
  const controls = <SpaceBetween direction="horizontal" size="xs">
    {busy === 'read' && <Button onClick={cancelRead}>{t('Cancel read', '読み取りをキャンセル')}</Button>}
    <Button disabled={!!busy} onClick={() => selected ? select(selected, false) : browse()}>{t('Refresh', '更新')}</Button>
  </SpaceBetween>;
  return <ContentLayout header={<Header variant="h1" actions={controls}>{selected ? t('Distribution details', 'ディストリビューションの詳細') : title}</Header>}>
    <SpaceBetween size="l">
      <Alert type="info">{t('Global configuration and saved local S3 content only. No external origin, DNS or live CDN traffic.', 'グローバル設定と保存済みのローカル S3 コンテンツのみを扱います。外部オリジン、DNS、CDN の通信は行いません。')}</Alert>
      {error && <Alert type="error" header={t('Request failed', 'リクエストに失敗しました')}>{error}</Alert>}
      {selected ? <>
        <Button disabled={busy === 'write'} onClick={() => browse(marker, history)}>{t('Back to distributions', 'ディストリビューションに戻る')}</Button>
        {!detail ? <Box>{busy ? t('Loading distribution…', 'ディストリビューションを読み込み中…') : t('Refresh to read this distribution.', '更新してディストリビューションを読み取ってください。')}</Box> : <>
          <Container header={<Header variant="h2">{detail.id}</Header>}>
            <KeyValuePairs columns={2} minColumnWidth={250} items={[
              {label:'ARN', value:detail.arn}, {label:'ETag', value:detail.etag},
              {label:t('Status', '状態'), value:detail.status}, {label:t('Enabled', '有効'), value:detail.enabled ? t('Yes','はい') : t('No','いいえ')},
              {label:t('Origin', 'オリジン'), value:detail.origin}, {label:t('Origin ID', 'オリジン ID'), value:detail.originId},
              {label:t('Default TTL (seconds)', 'デフォルト TTL（秒）'), value:String(detail.ttl)},
              {label:t('Min / max TTL', '最小 / 最大 TTL'), value:detail.minimum + ' / ' + detail.maximum},
              {label:t('Comment', 'コメント'), value:detail.comment || '—'}, {label:t('Last modified', '最終更新'), value:detail.modified},
            ]}/>
          </Container>
          <Container header={<Header variant="h2">{t('Saved local content', '保存済みのローカルコンテンツ')}</Header>}>
            <SpaceBetween size="m">
              <FormField label={t('Object key', 'オブジェクトキー')} description={t('Enter a saved key, such as assets/message.txt. Reads recheck both distribution and S3 permissions.', '保存済みのキー（例: assets/message.txt）を入力します。読み取りごとにディストリビューションと S3 の両方の権限を確認します。')}>
                <Input value={key} disabled={busy === 'write'} onChange={({detail:d}) => changeKey(d.value)}/>
              </FormField>
              <SpaceBetween direction="horizontal" size="xs">
                <Button disabled={!!busy || !detail.enabled || !localObjectKey(key)} onClick={readObject}>{t('Read local content', 'ローカルコンテンツを読み取る')}</Button>
                <Button disabled={!!busy || !object || object.key !== key} onClick={download}>{t('Download saved bytes', '保存済みのデータをダウンロード')}</Button>
              </SpaceBetween>
              {object && <>
                <KeyValuePairs columns={2} minColumnWidth={220} items={[
                  {label:t('Bytes', 'バイト数'), value:String(object.body.length)}, {label:t('Content type', 'コンテンツタイプ'), value:object.type},
                  {label:'ETag', value:object.etag}, {label:t('Local cache', 'ローカルキャッシュ'), value:object.cache},
                  {label:t('Age (seconds)', '経過時間（秒）'), value:object.age || '0'},
                ]}/>
                <FormField label={t('Text preview', 'テキストプレビュー')} description={object.truncated ? t('First 64 KiB. Download contains all saved bytes.', '先頭の 64 KiB を表示しています。ダウンロードには保存済みの全データが含まれます。') : undefined}>
                  <pre style={{whiteSpace:'pre-wrap',overflowWrap:'anywhere',maxHeight:320,overflow:'auto'}}>{object.preview}</pre>
                </FormField>
              </>}
            </SpaceBetween>
          </Container>
          <Container header={<Header variant="h2" actions={<Button ref={createButton} disabled={!!busy || attempt?.uncertain} onClick={() => openInvalidation()}>{t('Create invalidation', '無効化を作成')}</Button>}>{t('Cache invalidation', 'キャッシュの無効化')}</Header>}>
            <SpaceBetween size="m">
              <Box>{t('Invalidate all cached objects in this distribution (/*). Read the object again after completion to observe current S3 bytes.', 'このディストリビューション内のすべてのキャッシュ（/*）を無効化します。完了後にもう一度読み取り、現在の S3 データを確認してください。')}</Box>
              {attempt?.uncertain && <Alert type="warning" action={<Button ref={replayButton} disabled={!!busy} onClick={() => openInvalidation(true)}>{t('Replay same invalidation', '同じ無効化リクエストを再送')}</Button>}>
                {t('Unconfirmed caller reference: ', '未確認の呼び出し元参照: ')}{attempt.caller}
              </Alert>}
              {receipt && <>
                <KeyValuePairs columns={2} minColumnWidth={220} items={[
                  {label:t('Invalidation ID', '無効化 ID'),value:receipt.id}, {label:t('Status', '状態'),value:receipt.status},
                  {label:t('Caller reference', '呼び出し元参照'),value:receipt.caller}, {label:t('Created', '作成日時'),value:receipt.created},
                ]}/>
                <Button disabled={!!busy} onClick={refreshReceipt}>{t('Refresh invalidation', '無効化の状態を更新')}</Button>
              </>}
            </SpaceBetween>
          </Container>
        </>}
      </> : <>
        <Table ariaLabels={{tableLabel:title}} loading={!!busy} items={rows} trackBy="id"
          header={<Header variant="h2" counter={known ? `(${rows.length})` : undefined}>{title}</Header>}
          empty={<Box textAlign="center">{known ? t('No distributions on this page.', 'このページにディストリビューションはありません。') : t('Refresh to read distributions.', '更新してディストリビューションを読み取ってください。')}</Box>}
          columnDefinitions={[
            {id:'id',header:t('Distribution ID','ディストリビューション ID'),cell:row=><Button variant="inline-link" onClick={() => select(row.id)}>{row.id}</Button>},
            {id:'origin',header:t('Origin','オリジン'),cell:row=>row.origin},
            {id:'enabled',header:t('Enabled','有効'),cell:row=>row.enabled ? t('Yes','はい') : t('No','いいえ')},
            {id:'ttl',header:t('Default TTL','デフォルト TTL'),cell:row=>String(row.ttl)},
          ]}/>
        <SpaceBetween direction="horizontal" size="xs">
          <Button disabled={!!busy || !history.length} onClick={() => browse(history.at(-1), history.slice(0,-1))}>{t('Previous page','前のページ')}</Button>
          <Button disabled={!!busy || !next} onClick={() => browse(next, [...history, marker])}>{t('Next page','次のページ')}</Button>
        </SpaceBetween>
      </>}
    </SpaceBetween>
    <Modal visible={modal} onDismiss={() => { if (!busy) {setModal(false);setConfirmed(false);} }}
      header={t('Confirm invalidation','無効化を確認')} closeAriaLabel={t('Close invalidation','無効化を閉じる')}
      footer={<SpaceBetween direction="horizontal" size="xs">
        <Button disabled={!!busy} onClick={() => {setModal(false);setConfirmed(false);}}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" loading={busy === 'write'} disabled={!confirmed || !!busy} onClick={invalidate}>{t('Invalidate all objects','すべてのオブジェクトを無効化')}</Button>
      </SpaceBetween>}>
      <SpaceBetween size="m">
        <Box>{selected}</Box>
        <FormField label={t('Caller reference','呼び出し元参照')}><Input readOnly value={attempt?.caller || ''}/></FormField>
        <Checkbox checked={confirmed} disabled={!!busy} onChange={({detail:d}) => setConfirmed(d.checked)}>{t('Invalidate every cached object in this distribution (/*).','このディストリビューションのすべてのキャッシュ（/*）を無効化します。')}</Checkbox>
      </SpaceBetween>
    </Modal>
  </ContentLayout>;
}
