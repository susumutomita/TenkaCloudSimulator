import React, {useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import RadioGroup from '@cloudscape-design/components/radio-group';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import {useLocale} from './i18n.jsx';
import {simpleDBClient, validDomain, validText} from './simpledb-client.mjs';
import './simpledb.css';

// One request chain per mounted domain/item screen. Navigation or principal
// changes discard stale responses and never retry an unconfirmed mutation.
function useRequestChain() {
  const pending = useRef(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState('');
  const run = useCallback(async action => {
    if (pending.current) return;
    const controller = new AbortController();
    pending.current = controller;
    setError('');
    setBusy(true);
    try {
      await action(controller.signal);
    } catch (e) {
      if (!controller.signal.aborted) setError(e.message);
    } finally {
      if (pending.current === controller) {
        pending.current = null;
        setBusy(false);
      }
    }
  }, []);
  useLayoutEffect(() => () => {
    pending.current?.abort();
    pending.current = null;
  }, []);
  return {busy, error, run, clearError: () => setError('')};
}

function Domains({api, choose, returning}) {
  const {t} = useLocale();
  const [page, setPage] = useState(null), [name, setName] = useState('');
  const {busy, error, run} = useRequestChain();
  const nameInput = useRef(null), restored = useRef(false);
  useLayoutEffect(() => {
    if (!returning || restored.current || busy || (!page && !error)) return;
    restored.current = true;
    if (document.activeElement === document.body) nameInput.current?.focus();
  }, [returning, busy, page, error]);
  const load = useCallback((token = '') => run(async signal => {
    setPage(null);
    setPage(await api.listDomains(token, signal));
  }), [api, run]);
  useEffect(() => {load();}, [load]);
  const columns = [{id: 'name', header: t('Domain name','ドメイン名'), cell: name =>
    <Button variant="inline-link" disabled={busy} onClick={() => choose(name)}>{name}</Button>}];
  return <SpaceBetween size="l">
    {error && <Alert type="error">{error}</Alert>}
    <Table ariaLabels={{tableLabel: t('SimpleDB domains','SimpleDB ドメイン')}} columnDefinitions={columns}
      items={page?.domains ?? []} loading={busy} loadingText={t('Working with domains','ドメインを処理中')}
      empty={<Box textAlign="center">{page ? t('No domains on this page.','このページにドメインはありません。') : t('Domain list not loaded.','ドメイン一覧は未取得です。')}</Box>}
      header={<Header variant="h2" actions={<SpaceBetween size="xs" direction="horizontal">
        <Button disabled={busy} onClick={() => load()}>{t('Refresh domains','ドメインを更新')}</Button>
        <Button disabled={busy || !page?.nextToken} onClick={() => load(page.nextToken)}>{t('Next page','次のページ')}</Button>
      </SpaceBetween>}>{t('Domains','ドメイン')}</Header>}/>
    <Container header={<Header variant="h2">{t('Open or create a domain','ドメインを開く・作成する')}</Header>}>
      <SpaceBetween size="s">
        <FormField label={t('Domain to open or create','開く・作成するドメイン名')} constraintText={t('3–255 ASCII letters, digits, underscores, dots or hyphens.','英数字、アンダースコア、ドット、ハイフンで3〜255文字。')}>
          <Input ref={nameInput} value={name} disabled={busy} onChange={({detail}) => setName(detail.value)}/>
        </FormField>
        <SpaceBetween direction="horizontal" size="xs">
          <Button disabled={busy || !validDomain(name)} onClick={() => choose(name)}>{t('Open domain','ドメインを開く')}</Button>
          <Button variant="primary" disabled={busy || !validDomain(name)} onClick={() => run(async signal => {
            await api.createDomain(name, signal);
            signal.throwIfAborted();
            choose(name);
          })}>{t('Create domain','ドメインを作成')}</Button>
        </SpaceBetween>
      </SpaceBetween>
    </Container>
  </SpaceBetween>;
}

function Item({api, domain, goBack, canDelete}) {
  const {t} = useLocale();
  const {busy, error, run, clearError} = useRequestChain();
  const [item, setItem] = useState(''), [attrs, setAttrs] = useState(null), [notice, setNotice] = useState('');
  const [editor, showEditor] = useState(false), [deleting, setDeleting] = useState(null);
  const itemInput = useRef(null), attributeInput = useRef(null), status = useRef(null), completion = useRef(null);
  useLayoutEffect(() => {itemInput.current?.focus();}, []);
  useLayoutEffect(() => {
    if (busy || !completion.current) return;
    const {origin, target} = completion.current;
    completion.current = null;
    // A request may finish after the user has chosen a different control.
    if (document.activeElement !== origin && document.activeElement !== document.body) return;
    (target === 'attributes' && !error ? attributeInput : status).current?.focus();
  }, [busy, error, attrs, notice]);
  const [name, setName] = useState(''), [value, setValue] = useState(''), [expected, setExpected] = useState(''), [mode, setMode] = useState('absent');
  const options = [{value: 'absent', label: t('Attribute is absent','属性が存在しない')}, {value: 'equal', label: t('Attribute equals expected value','属性が期待値と一致する')}];
  const saveAllowed = attrs !== null && validText(name) && validText(value, true) && (mode === 'absent' || validText(expected, true));
  const errorCode = error.split(':', 1)[0];
  const refusal = {
    ConditionalCheckFailed: t('The condition no longer holds. The write was not applied. Read the item again before retrying.','条件を満たさなくなったため書き込まれていません。再試行の前に項目を読み直してください。'),
    AttributeDoesNotExist: t('The expected attribute is missing. The write was not applied.','比較する属性が存在しないため書き込まれていません。'),
    MultiValuedAttribute: t('The attribute has multiple values; this condition requires at most one. The write was not applied.','属性に複数の値があります。この条件では書き込まれていません。'),
    NumberItemAttributesExceeded: t('An item can contain up to 256 name/value pairs. The write was not applied. Reopen the item and either replace an existing value or use the SDK to repair a legacy item.','項目に保存できる名前と値の組は256件までです。書き込みは行われませんでした。項目を再取得して既存の値を置き換えるか、SDKで既存の上限超過を修正してください。'),
    AccessDenied: t('Your local identity is not allowed to perform this operation.','現在の演習IDにはこの操作の権限がありません。'),
  };
  function editItem(next) {
    clearError(); setDeleting(null); setItem(next); setAttrs(null); showEditor(false); setNotice(''); setName(''); setValue(''); setExpected(''); setMode('absent');
  }
  function read() {
    setDeleting(null);
    run(async signal => {
      completion.current = {origin: document.activeElement, target: 'attributes'};
      setNotice(''); setAttrs(null);
      setAttrs(await api.getAttributes(domain, item, signal));
      showEditor(true);
    });
  }
  function change(apply, result) {
    setDeleting(null);
    run(async signal => {
      completion.current = {origin: document.activeElement, target: 'status'};
      setNotice(''); setAttrs(null);
      await apply(signal);
      setNotice(result);
      try {
        setAttrs(await api.getAttributes(domain, item, signal));
      } catch {
        if (!signal.aborted) setNotice(result + '-no-readback');
      }
    });
  }
  function save() {
    change(signal => api.putAttribute(domain, item, {name, value, expected, mode}, signal), 'saved');
  }
  function remove() {
    if (!canDelete || !deleting || !attrs?.includes(deleting)) return;
    change(signal => api.deleteAttribute(domain, item, deleting, signal), 'deleted');
  }
  const notices = {
    saved: t('Attribute saved.','属性を保存しました。'),
    'saved-no-readback': t('Attribute saved, but current values could not be loaded. Use Read attributes to refresh.','属性は保存済みですが現在の値を取得できませんでした。「属性を読み込む」で更新してください。'),
    deleted: t('Attribute deleted.','属性を削除しました。'),
    'deleted-no-readback': t('Attribute deleted, but current values could not be loaded. Read the item again.','属性は削除済みですが現在の値を取得できませんでした。項目を読み直してください。'),
  };
  const display = value => {
    if (value === '') return <em>{t('Empty string','空文字列')}</em>;
    const visible = !value.trim() || /[\p{Cc}\p{Cf}]/u.test(value) ?
      JSON.stringify(value).replace(/[\p{Cc}\p{Cf}]/gu, char => char.split('').map(unit =>
        '\\u' + unit.charCodeAt(0).toString(16).padStart(4, '0')).join('')) : value;
    return <code className="simpledb-value resource-identifier">{visible}</code>;
  };
  const editingDisabled = busy || attrs === null;
  return <SpaceBetween size="l">
    <Button disabled={busy} onClick={goBack}>{t('Back to domains','ドメイン一覧に戻る')}</Button>
    {(error || notice) && <div ref={status} tabIndex={-1} role="region" aria-label={t('SimpleDB action status','SimpleDB 操作の状態')}>
      {error && <Alert type="error" header={errorCode}>{Object.hasOwn(refusal, errorCode) ? refusal[errorCode] : t('The request did not complete as expected. Read again before repeating a write.','要求が正常に完了しませんでした。書き込みをやり直す前に読み直してください。')}</Alert>}
      {notice && <Alert type={notice.endsWith('no-readback') ? 'warning' : 'success'}>{notices[notice]}</Alert>}
    </div>}
    <Container header={<Header variant="h2" description={<span className="resource-identifier">{domain}</span>}>{t('Look up an item','項目を取得')}</Header>}>
      <SpaceBetween size="s">
        <FormField label={t('Item name','項目名')} constraintText={t('Use a known item name, up to 1024 UTF-8 bytes. An unused name initially has no attributes.','既知の項目名を1024 UTF-8バイト以内で入力します。未使用の名前には属性がありません。')}>
          <Input ref={itemInput} value={item} disabled={busy} onChange={({detail}) => editItem(detail.value)}/>
        </FormField>
        <Button disabled={busy || !validText(item)} onClick={read}>{t('Read attributes','属性を読み込む')}</Button>
      </SpaceBetween>
    </Container>
    {attrs !== null && <Table ariaLabels={{tableLabel: t('Saved attributes','保存された属性')}} items={attrs} trackBy="id"
        columnDefinitions={[{id:'name', header:t('Attribute name','属性名'), cell:x => display(x.name)},
          {id:'value', header:t('Value','値'), cell:x => display(x.value)},
          ...(canDelete ? [{id:'delete', header:t('Action','操作'), cell:x =>
            <Button disabled={busy || attrs.some(a => a !== x && a.name === x.name)}
              onClick={() => {clearError(); setNotice(''); setDeleting(x);}}>{t('Delete attribute','属性を削除')}</Button>}] : [])]}
        header={<Header variant="h2">{t('Saved attributes','保存された属性')}</Header>}
        empty={<Box textAlign="center">{t('This item has no attributes.','この項目に属性はありません。')}</Box>}/>}
    {deleting && canDelete && <Alert type="warning" header={t('Delete this attribute value?','この属性値を削除しますか？')}>
      <SpaceBetween size="s"><span>{display(deleting.name)}: {display(deleting.value)}</span>
        <span>{t('Only the value shown is deleted. If this is the last attribute, the item is removed.','表示中の値だけを削除します。最後の属性を削除すると項目も削除されます。')}</span>
        <SpaceBetween size="xs" direction="horizontal"><Button disabled={busy} onClick={() => setDeleting(null)}>{t('Cancel','キャンセル')}</Button>
          <Button disabled={busy} onClick={remove}>{t('Confirm deletion','削除を確定')}</Button></SpaceBetween>
      </SpaceBetween></Alert>}
    {editor && <Container header={<Header variant="h2" description={t('Replace one attribute only if it is absent or its single value equals your expectation.','対象の属性が存在しないか、単一の値が期待値と一致した場合だけ、その属性を置き換えます。')}>{t('Save with a condition','条件付き保存')}</Header>}>
        <SpaceBetween size="s">
          <FormField label={t('Attribute to change','更新する属性')} constraintText={t('Maximum 1024 UTF-8 bytes.','最大1024 UTF-8バイト。')}>
            <Input ref={attributeInput} disabled={editingDisabled} value={name} onChange={({detail}) => setName(detail.value)}/>
          </FormField>
          <FormField label={t('New value','新しい値')} constraintText={t('Maximum 1024 UTF-8 bytes. An empty value is saved as an empty string.','最大1024 UTF-8バイト。空欄は空文字列として保存されます。')}>
            <Textarea rows={2} disabled={editingDisabled} value={value} onChange={({detail}) => setValue(detail.value)}/>
          </FormField>
          <FormField label={t('Condition','条件')}>
            <RadioGroup items={options.map(option => ({...option, disabled: editingDisabled}))} value={mode} onChange={({detail}) => setMode(detail.value)}/>
          </FormField>
          {mode === 'equal' && <FormField label={t('Expected value','期待値')} constraintText={t('An empty expected value means empty string, not absence.','空欄は空文字列との比較であり、属性の不在とは異なります。')}>
            <Textarea rows={2} disabled={editingDisabled} value={expected} onChange={({detail}) => setExpected(detail.value)}/>
          </FormField>}
          <Button variant="primary" disabled={busy || !saveAllowed} onClick={save}>{t('Save attribute','属性を保存')}</Button>
        </SpaceBetween>
    </Container>}
  </SpaceBetween>;
}

export default function SimpleDBView({request, canDelete = false}) {
  const {t} = useLocale();
  const api = useMemo(() => simpleDBClient(request), [request]);
  const [domain, setDomain] = useState(''), [returning, setReturning] = useState(false);
  function goBack() {setReturning(true); setDomain('');}
  return <ContentLayout header={<Header variant="h1" description={t('Local us-east-1 only. Read a named item and conditionally update one attribute. Item search is not available here.','ローカルの us-east-1 のみ。名前を指定して項目を読み込み、属性を条件付きで1件更新します。項目検索はこの画面では利用できません。')}>Amazon SimpleDB</Header>}>
    {domain ? <Item key={domain} api={api} domain={domain} goBack={goBack} canDelete={canDelete}/> : <Domains api={api} choose={setDomain} returning={returning}/>}
  </ContentLayout>;
}
