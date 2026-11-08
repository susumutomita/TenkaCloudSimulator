import React, {useCallback, useEffect, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import {useLocale} from './i18n.jsx';
import {queryStack, stackEvents, stackOutputs} from './cloudformation-events.mjs';

const identifier = value => <span className="resource-identifier">{value}</span>;

export function CloudFormationLookup({onOpen}) {
  const {t} = useLocale();
  const [name, setName] = useState('');
  return <Container header={<Header variant="h2">{t('Open stack history','スタック履歴を開く')}</Header>}>
    <form onSubmit={event => {event.preventDefault(); if (name.trim()) onOpen(name.trim());}}>
      <Form actions={<Button variant="primary" formAction="submit" disabled={!name.trim()}>{t('Open stack history','スタック履歴を開く')}</Button>}>
        <FormField label={t('Stack name or ID','スタック名またはID')} description={t('Use an exact stack ID for deleted stacks. The selected region and your existing permissions apply.','削除済みスタックには正確なスタックIDを指定してください。選択中のリージョンと現在の権限が適用されます。')}>
          <Input value={name} onChange={({detail}) => setName(detail.value)}/>
        </FormField>
      </Form>
    </form>
  </Container>;
}

export default function CloudFormationDetails({request, region, stackName, onDismiss}) {
  const {t} = useLocale();
  const [tab, setTab] = useState('events');
  const [events, setEvents] = useState([]), [outputs, setOutputs] = useState([]);
  const [nextToken, setNextToken] = useState(''), [busy, setBusy] = useState(false), [error, setError] = useState('');
  const current = useRef({id: 0, controller: null});
  const load = useCallback(async (append = false, token = '') => {
    current.current.controller?.abort();
    const controller = new AbortController(), id = ++current.current.id;
    current.current.controller = controller;
    setBusy(true); setError('');
    if (!append) {setEvents([]); setOutputs([]); setNextToken('');}
    try {
      const xml = await queryStack(request, region, tab === 'events' ? 'DescribeStackEvents' : 'DescribeStacks', stackName,
        {signal: controller.signal, nextToken: append ? token : undefined});
      if (current.current.id !== id) return;
      if (tab === 'events') {
        const page = stackEvents(xml);
        if (page.nextToken && page.nextToken === token) throw Error('CloudFormation returned a repeated event cursor. Refresh the history.');
        setEvents(old => append ? [...old, ...page.events.filter(event => !old.some(previous => previous.id === event.id))] : page.events);
        setNextToken(page.nextToken);
      } else setOutputs(stackOutputs(xml));
    } catch (failure) {
      if (current.current.id === id && failure.name !== 'AbortError') {
        // Never retain previously authorized rows after a denied refresh/page.
        setEvents([]); setOutputs([]); setNextToken(''); setError(failure.message);
      }
    } finally {if (current.current.id === id) setBusy(false);}
  }, [request, region, stackName, tab]);
  useEffect(() => {
    load();
    return () => {current.current.id++; current.current.controller?.abort();};
  }, [load]);
  return <Container header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">
    <Button iconName="refresh" disabled={busy} ariaLabel={t('Refresh stack details','スタック詳細を更新')} onClick={() => load()}/>
    <Button onClick={onDismiss}>{t('Close stack details','スタック詳細を閉じる')}</Button>
  </SpaceBetween>}>{t('Stack details','スタック詳細')}</Header>}>
    <SpaceBetween size="l">
      <Box>{identifier(stackName)}</Box>
      <Tabs activeTabId={tab} onChange={({detail}) => setTab(detail.activeTabId)} tabs={[
        {id:'events', label:t('Events','イベント')}, {id:'outputs', label:t('Outputs','出力')},
      ]}/>
      {error && <Alert type="error">{error}</Alert>}
      {tab === 'events' ? <>
        <Alert type="info">{t('Newest first. Events describe committed synchronous local stack operations, not CloudTrail or application logs. Failed requests are returned as API errors and do not create failure events. Parameter values, outputs and failure reasons are not recorded.','新しいイベントから表示します。ローカルで確定した同期スタック操作の履歴であり、CloudTrailやアプリケーションログではありません。失敗したリクエストはAPIエラーになり、失敗イベントは作成されません。パラメータ値、出力、失敗理由は記録しません。')}</Alert>
        <Table ariaLabels={{tableLabel:t('Stack events','スタックイベント')}} trackBy="id" wrapLines
          loading={busy} loadingText={t('Loading stack events','スタックイベントを読み込み中')} items={events}
          header={<Header variant="h3" counter={'('+events.length+')'}>{t('Stack events','スタックイベント')}</Header>}
          columnDefinitions={[
            {id:'time', header:t('Timestamp (UTC)','時刻（UTC）'), cell:event => identifier(event.timestamp)},
            {id:'logical', header:t('Logical ID','論理ID'), cell:event => identifier(event.logicalId)},
            {id:'status', header:t('Status','ステータス'), cell:event => event.status},
            {id:'type', header:t('Resource type','リソースタイプ'), cell:event => identifier(event.resourceType)},
            {id:'physical', header:t('Physical ID','物理ID'), cell:event => identifier(event.physicalId)},
            {id:'event', header:t('Event ID','イベントID'), cell:event => identifier(event.id)},
          ]}
          empty={<Box textAlign="center">{error ? t('Events could not be loaded.','イベントを読み込めませんでした。') : t('No recorded events. Stacks created before event recording was added have no earlier history.','記録されたイベントはありません。イベント記録機能の追加前に作成したスタックの過去の履歴はありません。')}</Box>}/>
        {nextToken && <Button disabled={busy} onClick={() => load(true, nextToken)}>{t('Load older events','過去のイベントを読み込む')}</Button>}
      </> : busy ? <StatusIndicator type="loading">{t('Loading stack outputs','スタックの出力を読み込み中')}</StatusIndicator> : !error && (outputs.length ? <KeyValuePairs columns={1} items={outputs.map(item => ({...item,value:identifier(item.value)}))}/> : <Box>{t('No stack outputs.','スタックの出力はありません。')}</Box>)}
    </SpaceBetween>
  </Container>;
}
