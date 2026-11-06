// SPDX-License-Identifier: MIT
import React, {useEffect, useLayoutEffect, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import TextFilter from '@cloudscape-design/components/text-filter';
import {useLocale} from './i18n.jsx';
import {disableMotion} from '@cloudscape-design/global-styles';

// Original local UI over native KMS APIs. Public AWS docs put create/delete on
// the key-detail Aliases tab; retargeting is explicitly a local API tool because
// AWS does not offer UpdateAlias in its console. No AWS assets are copied.
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const validName = name => name.length <= 256 && /^alias\/[a-zA-Z0-9/_-]+$/.test(name) && !name.startsWith('alias/aws/');
const keyArn = (region, id) => `arn:aws:kms:${region}:000000000000:key/${id}`;
const validDate = value => typeof value === 'number' && Number.isFinite(value) && value > 0 && value <= 8640000000000;
const aliasArn = (region, name) => `arn:aws:kms:${region}:000000000000:${name}`;
const identifier = value => <span className="resource-identifier">{value}</span>;
const aborted = () => new DOMException('The alias view changed.', 'AbortError');

export default function KMSAliasesView({region, principal = '', keyId = '', request, onSaving}) {
 const {locale, t} = useLocale();
 const [items, setItems] = useState([]), [selected, setSelected] = useState([]);
 const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false);
 const [mode, setMode] = useState('list'), [name, setName] = useState('');
 const [target, setTarget] = useState(null), [targetMetadata, setTargetMetadata] = useState(null);
 const [options, setOptions] = useState([]), [keysLoading, setKeysLoading] = useState(false), [targetLoading, setTargetLoading] = useState(false);
 const [error, setError] = useState(''), [readFailed, setReadFailed] = useState(false), [lookupError, setLookupError] = useState('');
 const [notice, setNotice] = useState(null), [invalidName, setInvalidName] = useState(false), [filter, setFilter] = useState('');
 const [deleting, setDeleting] = useState(null), [confirmation, setConfirmation] = useState('');
 const motionRoot = useRef(null);
 const lifeRef = useRef(null), requestRef = useRef(request), savingRef = useRef(false);
 const onSavingRef = useRef(onSaving), errorRef = useRef(null), nameRef = useRef(null), confirmRef = useRef(null), modalErrorRef = useRef(null);
 requestRef.current = request; onSavingRef.current = onSaving;
 const current = (life, revision) => lifeRef.current === life && life?.active && !life.controller.signal.aborted && (revision === undefined || life.revision === revision);
 const formCurrent = (life, form) => current(life) && life.form === form;

 async function call(life, op, body = {}) {
  if (!current(life)) throw aborted();
  const response = await requestRef.current('/', {method: 'POST', signal: life.controller.signal,
   headers: {'Content-Type': 'application/x-amz-json-1.1', 'X-Amz-Target': 'TrentService.' + op, 'X-Simulator-Service': 'kms', 'X-Simulator-Region': life.region}, body: JSON.stringify(body)});
  const text = await response.text();
  if (!current(life)) throw aborted();
  const result = text ? JSON.parse(text) : {};
  if (!response.ok) throw Error(`${response.status}: ${typeof result.message === 'string' ? result.message : 'KMS request failed.'}`);
  return result;
 }
 async function load(life = lifeRef.current) {
  if (!current(life)) return false;
  const revision = ++life.revision;
  setLoading(true); setError(''); setReadFailed(false); setSelected([]);
  try {
   const aliases = [], names = new Set(), markers = new Set(); let marker;
   do {
    const page = await call(life, 'ListAliases', {Limit: 100, ...(life.keyId ? {KeyId: life.keyId} : {}), ...(marker ? {Marker: marker} : {})});
    if (!current(life, revision)) return false;
    if (!Array.isArray(page.Aliases) || page.Aliases.length > 100 || typeof page.Truncated !== 'boolean') throw Error('Invalid native alias list.');
    for (const item of page.Aliases) {
     if (!item || !validName(item.AliasName || '') || !uuid.test(item.TargetKeyId || '') || item.AliasArn !== aliasArn(life.region, item.AliasName) || !validDate(item.CreationDate) || !validDate(item.LastUpdatedDate) || item.LastUpdatedDate < item.CreationDate || names.has(item.AliasName) || (life.keyId && item.TargetKeyId !== life.keyId)) throw Error('Invalid native alias metadata.');
     names.add(item.AliasName); aliases.push(item);
    }
    if (aliases.length > 1000) throw Error('Local alias list exceeded its limit.');
    marker = page.Truncated ? page.NextMarker : null;
    if (page.Truncated && (!page.Aliases.length || typeof marker !== 'string' || !marker || marker.length > 1024 || markers.has(marker))) throw Error('Invalid native alias continuation.');
    if (marker) markers.add(marker);
   } while (marker);
   if (current(life, revision)) setItems(aliases);
   return current(life, revision);
  } catch (e) {
   if (current(life, revision)) { setItems([]); setError(e.message); setReadFailed(true); }
   return false;
  } finally { if (current(life, revision)) setLoading(false); }
 }
 async function inspectTarget(life, form, option) {
  if (!formCurrent(life, form)) return;
  const targetRevision = ++life.targetRevision;
  setTarget(option); setTargetMetadata(null); setLookupError(''); setTargetLoading(!!option);
  if (!option) return;
  try {
   const result = await call(life, 'DescribeKey', {KeyId: option.value});
   if (!formCurrent(life, form) || life.targetRevision !== targetRevision) return;
   const key = result.KeyMetadata;
   if (!key || key.KeyId !== option.value || key.Arn !== keyArn(life.region, option.value) || key.KeySpec !== 'SYMMETRIC_DEFAULT' || key.KeyUsage !== 'ENCRYPT_DECRYPT' || !['Enabled', 'Disabled'].includes(key.KeyState) || key.KeyManager !== 'CUSTOMER' || key.AWSAccountId !== '000000000000' || key.MultiRegion !== false || key.Enabled !== (key.KeyState === 'Enabled')) throw Error('Invalid native target key metadata.');
   setTargetMetadata(key);
  } catch (e) { if (formCurrent(life, form) && life.targetRevision === targetRevision) { setLookupError(e.message); setTargetMetadata(null); } }
  finally { if (formCurrent(life, form) && life.targetRevision === targetRevision) setTargetLoading(false); }
 }
 async function loadKeys(life, form, initial) {
  if (!formCurrent(life, form)) return;
  const keysRevision = ++life.keysRevision;
  const keyCurrent = () => formCurrent(life, form) && life.keysRevision === keysRevision;
  setKeysLoading(true); setOptions([]); setTargetMetadata(null); setLookupError('');
  try {
   const keys = [], ids = new Set(), markers = new Set(); let marker;
   do {
    const page = await call(life, 'ListKeys', {Limit: 100, ...(marker ? {Marker: marker} : {})});
    if (!keyCurrent()) return;
    if (!Array.isArray(page.Keys) || page.Keys.length > 100 || typeof page.Truncated !== 'boolean') throw Error('Invalid native key list.');
    for (const key of page.Keys) {
     if (!uuid.test(key?.KeyId || '') || key.KeyArn !== keyArn(life.region, key.KeyId) || ids.has(key.KeyId)) throw Error('Invalid native key identifier.');
     ids.add(key.KeyId); keys.push({label: key.KeyId, value: key.KeyId, description: key.KeyArn});
    }
    if (keys.length > 1000) throw Error('Local key list exceeded its limit.');
    marker = page.Truncated ? page.NextMarker : null;
    if (page.Truncated && (!page.Keys.length || typeof marker !== 'string' || !marker || marker.length > 1024 || markers.has(marker))) throw Error('Invalid native key continuation.');
    if (marker) markers.add(marker);
   } while (marker);
   if (keyCurrent()) {
    setOptions(keys);
    const option = initial ? keys.find(item => item.value === initial) : null;
    if (initial && !option) throw Error('The requested target key is not in the current native key list.');
    if (option) await inspectTarget(life, form, option);
   }
  } catch (e) { if (keyCurrent()) setLookupError(e.message); }
  finally { if (keyCurrent()) setKeysLoading(false); }
 }
 const clearForm = life => { life.form++; life.targetRevision++; life.keysRevision++; setName(''); setTarget(null); setTargetMetadata(null); setOptions([]); setLookupError(''); setInvalidName(false); setKeysLoading(false); setTargetLoading(false); };
 function open(kind) {
  const life = lifeRef.current; if (!current(life) || busy || loading || (kind === 'update' && selected.length !== 1)) return;
  clearForm(life); setError(''); setNotice(null); setReadFailed(false); setMode(kind);
  setName(kind === 'update' ? selected[0].AliasName.slice(6) : '');
  const form = life.form;
  void loadKeys(life, form, kind === 'update' ? selected[0].TargetKeyId : life.keyId);
 }
 function cancel() { const life = lifeRef.current; if (!current(life) || busy) return; clearForm(life); setMode('list'); setError(''); }
 async function mutate(op, body) {
  const life = lifeRef.current; if (!current(life) || savingRef.current) return;
  const saving = {life}; savingRef.current = saving; setBusy(true); setError(''); setNotice(null); onSavingRef.current?.(true);
  try {
   await call(life, op, body);
   if (!current(life)) return;
   // ACK ends the write intent BEFORE the independent metadata read. A failed
   // read can only retry ListAliases; it must never re-open/replay the mutation.
   clearForm(life); setMode('list'); setDeleting(null); setConfirmation(''); setSelected([]); setItems([]); setNotice(op);
   await load(life);
  } catch (e) { if (current(life)) setError(e.message); }
  finally { if (savingRef.current === saving) { savingRef.current = false; if (current(life)) { setBusy(false); onSavingRef.current?.(false); } } }
 }
 function submit(event) {
  event.preventDefault(); const fullName = 'alias/' + name;
  if (!validName(fullName)) { setInvalidName(true); nameRef.current?.focus(); return; }
  if (!targetMetadata || !target || targetMetadata.KeyId !== target.value || keysLoading || targetLoading) return;
  void mutate(mode === 'update' ? 'UpdateAlias' : 'CreateAlias', {AliasName: fullName, TargetKeyId: targetMetadata.KeyId});
 }
 useLayoutEffect(() => {const target = motionRoot.current; if (!target) return; disableMotion(true, target); return () => disableMotion(false, target);}, []);
 useLayoutEffect(() => {
  const life = {region, keyId, active: true, revision: 0, form: 0, targetRevision: 0, keysRevision: 0, controller: new AbortController()}; lifeRef.current = life;
  setItems([]); setSelected([]); setMode('list'); setName(''); setTarget(null); setTargetMetadata(null); setOptions([]); setError(''); setLookupError(''); setNotice(null); setReadFailed(false); setDeleting(null); setConfirmation(''); setFilter(''); setBusy(false); setKeysLoading(false); setTargetLoading(false);
  void load(life);
  return () => { life.active = false; life.controller.abort(); if (lifeRef.current === life) lifeRef.current = null; if (savingRef.current?.life === life) { savingRef.current = false; onSavingRef.current?.(false); } };
 }, [region, principal, keyId]);
 useEffect(() => { if (error) (deleting ? modalErrorRef : errorRef).current?.focus(); }, [error, deleting]);
 useEffect(() => { if (mode !== 'list') nameRef.current?.focus(); }, [mode]);
 useEffect(() => { if (deleting) confirmRef.current?.focus(); }, [deleting]);
 const row = selected[0], deletingName = deleting?.AliasName || '';
 const notices = {CreateAlias: t('Alias created.', 'エイリアスを作成しました。'), UpdateAlias: t('Alias target changed.', 'エイリアスの対象キーを変更しました。'), DeleteAlias: t('Alias deleted. The key is unchanged.', 'エイリアスを削除しました。キーは変更されていません。')};
 const date = value => new Intl.DateTimeFormat(locale === 'ja' ? 'ja-JP' : 'en-US', {dateStyle: 'medium', timeStyle: 'medium'}).format(new Date(value * 1000));
 return <div ref={motionRoot} data-kms-aliases-view><SpaceBetween size="l">
  <Alert type="info">{t('Customer aliases in this local account and Region. AWS-managed aliases, grants and alias-based access conditions are unsupported. Changing the target is a local API tool; the AWS console does not offer this action.', 'このローカルアカウントとリージョンのカスタマーエイリアスです。AWS マネージドエイリアス、グラント、エイリアスに基づくアクセス条件には未対応です。対象キーの変更はローカル API ツールであり、AWS コンソールにはない操作です。')}</Alert>
  {error && !deleting && <Alert ref={errorRef} type="error" header={readFailed ? t('Alias list could not be refreshed', 'エイリアス一覧を更新できませんでした') : t('Alias operation failed', 'エイリアスの操作に失敗しました')} dismissible onDismiss={() => setError('')} i18nStrings={{dismissAriaLabel: t('Dismiss alias error', 'エイリアスのエラーを閉じる'), errorIconAriaLabel: t('Error', 'エラー')}}>{error}</Alert>}
  {notice && <Alert type="success">{notices[notice]}{readFailed && <Box>{t('The write succeeded. Retry only the read to see current metadata.', '書き込みは成功しました。現在の情報を確認するには読み取りのみを再試行してください。')}</Box>}</Alert>}
  {readFailed && <Button formAction="none" disabled={busy} onClick={() => load()}>{t('Retry alias list (read only)', 'エイリアス一覧を再試行（読み取りのみ）')}</Button>}
  {mode === 'list' ? <Table variant="embedded" trackBy="AliasName" items={items.filter(item => `${item.AliasName} ${item.TargetKeyId}`.toLowerCase().includes(filter.toLowerCase()))} loading={loading} loadingText={t('Loading aliases', 'エイリアスを読み込み中')} selectionType="single" selectedItems={selected} onSelectionChange={({detail}) => setSelected(detail.selectedItems)} ariaLabels={{tableLabel: t('Aliases', 'エイリアス'), selectionGroupLabel: t('Select an alias', 'エイリアスを選択'), itemSelectionLabel: (_, item) => item.AliasName}} header={<Header variant="h2" counter={`(${items.length})`} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" iconName="refresh" ariaLabel={t('Refresh aliases', 'エイリアスを更新')} disabled={busy} onClick={() => load()}/><Button formAction="none" disabled={busy || loading || selected.length !== 1} onClick={() => open('update')}>{t('Change target (local API)', '対象キーを変更（ローカル API）')}</Button><Button formAction="none" disabled={busy || loading || selected.length !== 1} onClick={() => {setDeleting(row); setConfirmation(''); setError('');}}>{t('Delete alias', 'エイリアスを削除')}</Button><Button formAction="none" variant="primary" disabled={busy || loading} onClick={() => open('create')}>{t('Create alias', 'エイリアスを作成')}</Button></SpaceBetween>}>{t('Aliases', 'エイリアス')}</Header>} filter={<TextFilter filteringText={filter} filteringPlaceholder={t('Find aliases', 'エイリアスを検索')} filteringAriaLabel={t('Find aliases', 'エイリアスを検索')} onChange={({detail}) => setFilter(detail.filteringText)}/>} columnDefinitions={[{id: 'name', header: t('Alias name', 'エイリアス名'), cell: item => identifier(item.AliasName)}, {id: 'target', header: t('Target key ID', '対象キー ID'), cell: item => identifier(item.TargetKeyId)}, {id: 'arn', header: t('Alias ARN', 'エイリアス ARN'), cell: item => identifier(item.AliasArn)}, {id: 'updated', header: t('Last updated', '最終更新'), cell: item => date(item.LastUpdatedDate)}]} empty={<Box textAlign="center" padding="l">{readFailed ? t('Alias list unavailable', 'エイリアス一覧を取得できません') : t('No aliases', 'エイリアスがありません')}</Box>}/> : <form onSubmit={submit}><Form header={<Header variant="h2">{mode === 'update' ? t('Change alias target (local API)', 'エイリアスの対象キーを変更（ローカル API）') : t('Create alias', 'エイリアスを作成')}</Header>} actions={<SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={cancel}>{t('Cancel', 'キャンセル')}</Button><Button variant="primary" formAction="submit" loading={busy} disabled={keysLoading || targetLoading || !targetMetadata}>{mode === 'update' ? t('Change target', '対象キーを変更') : t('Create alias', 'エイリアスを作成')}</Button></SpaceBetween>}><Container><SpaceBetween size="l"><FormField label={t('Alias name', 'エイリアス名')} description={t('The alias/ prefix is added automatically. Do not include confidential or sensitive information.', 'alias/ を自動で付加します。機密情報を含めないでください。')} constraintText={t('1–250 characters: letters, numbers, /, _ and -. Names starting with aws/ are reserved.', '1～250 文字。英数字、/、_、- を使用できます。aws/ で始まる名前は予約されています。')} errorText={invalidName ? t('Enter a valid customer alias suffix.', '有効なカスタマーエイリアスの名前を入力してください。') : undefined}><Input ref={nameRef} value={name} readOnly={mode === 'update'} disabled={busy} invalid={invalidName} onChange={({detail}) => {setName(detail.value); setInvalidName(false);}}/></FormField><FormField label={t('Target key', '対象キー')} description={t('The picker reads ListKeys and DescribeKey. Those read permissions do not grant permission to create aliases or use keys.', '選択には ListKeys と DescribeKey の読み取り権限が必要です。これらの権限だけではエイリアスの作成やキーの使用は許可されません。')} errorText={lookupError || undefined}><Select selectedOption={target} options={options} statusType={keysLoading ? 'loading' : lookupError && !options.length ? 'error' : 'finished'} loadingText={t('Loading native keys', 'ネイティブキーを読み込み中')} errorText={lookupError} placeholder={t('Select a customer managed key', 'カスタマーマネージドキーを選択')} disabled={busy || keysLoading} onChange={({detail}) => {const life = lifeRef.current; if (current(life)) inspectTarget(life, life.form, detail.selectedOption);}} filteringType="auto" empty={t('No keys in this Region', 'このリージョンにキーがありません')} /></FormField>{lookupError && <Button formAction="none" disabled={busy || keysLoading || targetLoading} onClick={() => {const life = lifeRef.current; if (current(life)) {if (target && options.length) inspectTarget(life, life.form, target); else loadKeys(life, life.form, target?.value || life.keyId);}}}>{t('Retry target lookup (read only)', '対象キーを再試行（読み取りのみ）')}</Button>}{targetLoading && <div role="status" aria-live="polite">{t('Reading target key metadata…', '対象キーの情報を読み込み中…')}</div>}{targetMetadata && <KeyValuePairs columns={1} items={[{label: t('Verified target key ID', '確認済み対象キー ID'), value: identifier(targetMetadata.KeyId)}, {label: 'ARN', value: identifier(targetMetadata.Arn)}, {label: t('Key state', 'キーの状態'), value: targetMetadata.KeyState}]}/>}</SpaceBetween></Container></Form></form>}
  <Modal visible={!!deleting} header={t('Delete alias', 'エイリアスを削除')} onDismiss={() => {if (!busy) {setDeleting(null); setConfirmation('');}}} closeAriaLabel={t('Close delete alias confirmation', 'エイリアス削除の確認を閉じる')} footer={<Box float="right"><SpaceBetween direction="horizontal" size="xs"><Button formAction="none" disabled={busy} onClick={() => {setDeleting(null); setConfirmation('');}}>{t('Cancel', 'キャンセル')}</Button><Button formAction="none" variant="primary" loading={busy} disabled={confirmation !== deletingName || !deletingName} onClick={() => mutate('DeleteAlias', {AliasName: deletingName})}>{t('Delete alias', 'エイリアスを削除')}</Button></SpaceBetween></Box>}><SpaceBetween size="l">{error && <Alert ref={modalErrorRef} type="error" i18nStrings={{errorIconAriaLabel: t('Error', 'エラー')}}>{error}</Alert>}<Alert type="warning">{t('Deleting this alias does not delete the KMS key. Applications using this alias may fail. The native API rechecks permissions on the actual current target.', 'エイリアスを削除しても KMS キーは削除されません。この名前を使用するアプリケーションが失敗する場合があります。ネイティブ API は現在の対象キーへの権限を再確認します。')}</Alert>{identifier(deletingName)}<FormField label={t('Type alias name to confirm', '確認のためエイリアス名を入力')}><Input ref={confirmRef} value={confirmation} disabled={busy} onChange={({detail}) => setConfirmation(detail.value)}/></FormField></SpaceBetween></Modal>
 </SpaceBetween></div>;
}
