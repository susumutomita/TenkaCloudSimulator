import React, {useCallback, useEffect, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import {useLocale} from './i18n.jsx';
import {getDocumentVersion, listDocumentVersions, listOwnedDocuments} from './ssm-document-client.mjs';
import './ssm-documents.css';

function useRead(load) {
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState(null);
  useEffect(() => {
    if (!load) return;
    const controller = new AbortController();
    const finish = result => {
      if (!controller.signal.aborted) setResult({load, revision, ...result});
    };
    load(controller.signal).then(data => finish({data}), failure => finish({failure}));
    return () => controller.abort();
  }, [load, revision]);
  const visible = result?.load === load && result.revision === revision ? result : null;
  return {...visible, loading: Boolean(load && !visible), retry: () => setRevision(n => n + 1)};
}

function Result({result, children}) {
  const {t} = useLocale();
  if (result.loading) return <StatusIndicator type="loading">{t('Loading documents','ドキュメントを読み込み中')}</StatusIndicator>;
  if (result.failure) return <Alert type="error" header={t('Systems Manager could not read this request','Systems Manager の読み取りに失敗しました')}
    action={<Button onClick={result.retry}>{t('Retry','再試行')}</Button>}>{result.failure.message}</Alert>;
  return result.data !== undefined ? children(result.data) : null;
}

function StoredContent({item}) {
  const {t} = useLocale();
  return <Container header={<Header variant="h3">{t('Version ','バージョン ')}{item.id}</Header>}>
    <pre className="ssm-definition-content">{item.content}</pre>
  </Container>;
}

function ComparisonChoices({versions, get, choose}) {
  const {t, locale} = useLocale();
  const makeOption = item => ({
    value: item.id,
    label: 'v' + item.id + (item.isDefault ? t(' (default)','（デフォルト）') : ''),
    description: new Date(item.created * 1000).toLocaleString(locale === 'ja' ? 'ja-JP' : 'en-US'),
  });
  const options = versions.map(makeOption);
  const defaultId = versions.find(item => item.isDefault).id;
  const [leftId, setLeftId] = useState(defaultId);
  const [rightId, setRightId] = useState(versions.findLast(item => item.id !== defaultId)?.id);
  const left = options.find(option => option.value === leftId);
  const right = options.find(option => option.value === rightId);
  const open = ids => choose(ids.map(id => versions.find(item => item.id === id)));
  return <SpaceBetween size="s">
    <ColumnLayout columns={2}>
      <FormField label={t('Base version','比較元バージョン')}><Select options={options} selectedOption={left || null}
        onChange={({detail}) => {choose(null); setLeftId(detail.selectedOption.value);}} /></FormField>
      <FormField label={t('Compare with','比較対象')}><Select options={options} selectedOption={right || null}
        onChange={({detail}) => {choose(null); setRightId(detail.selectedOption.value);}} /></FormField>
    </ColumnLayout>
    {versions.length < 2 && <Box>{t('Only one version is stored.','保存されているバージョンは1つだけです。')}</Box>}
    <Button onClick={() => open([leftId,rightId])} disabled={!left || !right || leftId === rightId}>{t('Compare selected versions','選択したバージョンを比較')}</Button>
    <Button onClick={() => open([defaultId])}>{t('View default content','デフォルトの内容を表示')}</Button>
    {get.loading || get.failure || get.data ? <Result result={get}>{items =>
      <ColumnLayout columns={items.length}>{items.map(item => <StoredContent key={item.id} item={item}/>)}</ColumnLayout>
    }</Result> : null}
  </SpaceBetween>;
}

function DocumentDetails({name, request, region, onClose}) {
  const {t} = useLocale();
  const [showHistory, setShowHistory] = useState(false);
  const [opened, setOpened] = useState(null);
  const loadVersions = useCallback(signal => listDocumentVersions(request, region, signal, name), [request, region, name]);
  const loadContent = useCallback(signal => Promise.all(opened.map(item => getDocumentVersion(request, region, signal, name, item.id, item.created))),
    [request, region, name, opened]);
  const versions = useRead(showHistory ? loadVersions : null);
  const get = useRead(opened ? loadContent : null);
  return <SpaceBetween size="m">
    <Header variant="h1" actions={<Button onClick={onClose}>{t('Back to documents','ドキュメント一覧へ戻る')}</Button>}>{name}</Header>
    <Tabs activeTabId="content" tabs={[{id:'content', label:t('Content','コンテンツ'), content:
      <SpaceBetween size="m">
        <Alert type="info">{t('These are stored document definitions. Opening them does not run their commands.','保存済みのドキュメント定義です。表示してもコマンドは実行されません。')}</Alert>
        <Button variant="primary" disabled={versions.loading} onClick={() => {setOpened(null); if (showHistory) versions.retry(); else setShowHistory(true);}}>
          {t('Compare versions','バージョンを比較')}
        </Button>
        <Result result={versions}>{items => <ComparisonChoices versions={items} get={get} choose={setOpened} />}</Result>
      </SpaceBetween>
    }]} />
  </SpaceBetween>;
}

// The parent must remount on principal, region, or navigation changes.
export default function SSMDocuments({request, region}) {
  const {t} = useLocale();
  const [name, setName] = useState(null);
  const browse = useCallback(signal => listOwnedDocuments(request, region, signal), [request, region]);
  const result = useRead(name === null ? browse : null);
  if (name !== null)
    return <DocumentDetails key={name} name={name} request={request} region={region} onClose={() => {setName(null); result.retry();}} />;
  return <SpaceBetween size="m">
    <Header variant="h1" actions={<Button disabled={result.loading} onClick={result.retry}>{t('Refresh documents','ドキュメントを更新')}</Button>}>{t('Documents','ドキュメント')}</Header>
    <Tabs activeTabId="owned" tabs={[{id:'owned', label:t('Owned by me','自分が所有'), content:
      <Result result={result}>{items => <Table items={items} trackBy="id" wrapLines
        ariaLabels={{tableLabel:t('Owned documents','自分が所有するドキュメント')}}
        columnDefinitions={[{id:'name', header:t('Name','名前'), cell:item => <Button variant="inline-link" onClick={() => setName(item.id)}>{item.id}</Button>}]}
        empty={<Box>{t('No owned documents in this Region.','このリージョンに自分が所有するドキュメントはありません。')}</Box>}
      />}</Result>
    }]} />
  </SpaceBetween>;
}
