import React, {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Link from '@cloudscape-design/components/link';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import RadioGroup from '@cloudscape-design/components/radio-group';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {isLocalTopicArn, snsClient, SNS_ENDPOINT_PROTOCOLS} from './sns-client.mjs';
import './sns.css';

const noOp = () => {};
const pageSize = 20;
const identifier = value => <span className="sns-identifier">{value}</span>;

export default function SNSView({region, request, onSaving = noOp}) {
  const {t} = useLocale();
  const api = useMemo(() => snsClient(request, region), [request, region]);
  const active = useRef(null);
  const [pending, setPending] = useState('');
  const [topics, setTopics] = useState([]);
  const [topicsIssue, setTopicsIssue] = useState('');
  const [lookup, setLookup] = useState('');
  const [topic, setTopic] = useState('');
  const [list, setList] = useState(null);
  const [page, setPage] = useState(1);
  const [selection, setSelection] = useState(null);
  const [deleting, setDeleting] = useState(null);
  const [creating, setCreating] = useState(false);
  const [protocol, setProtocol] = useState('https');
  const [endpoint, setEndpoint] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState(null);

  const run = useCallback(async (name, work, report = setError) => {
    if (active.current) return;
    const controller = new AbortController();
    const signal = controller.signal;
    const mutating = name === 'subscribe' || name === 'unsubscribe';
    const operation = {controller, mutating};
    const valid = () => active.current === operation && !signal.aborted;
    active.current = operation;
    setPending(name);
    if (mutating) onSaving(true);
    try {
      await work(signal);
    } catch (reason) {
      if (valid()) report(reason.message);
    } finally {
      if (valid()) {
        active.current = null;
        if (mutating) onSaving(false);
        setPending('');
      }
    }
  }, [onSaving]);

  const loadTopics = useCallback(() => run('topics', async signal => {
    setTopicsIssue('');
    const fresh = await api.listTopics(signal);
    signal.throwIfAborted();
    setTopics(fresh);
  }, setTopicsIssue), [api, run]);

  useEffect(() => {
    loadTopics();
    return () => {
      const operation = active.current;
      active.current = null;
      operation?.controller.abort();
      if (operation?.mutating) onSaving(false);
    };
  }, [loadTopics, onSaving]);

  function openTopic(event) {
    event.preventDefault();
    const arn = lookup.trim();
    if (active.current) return;
    setError(''); setNotice(null);
    if (!isLocalTopicArn(arn, region)) {
      setError(t('Enter a local topic ARN in the selected region.', '選択したリージョンのローカルトピック ARN を入力してください。'));
      return;
    }
    loadTopic(arn);
  }

  function loadTopic(arn) {
    return run('list', async signal => {
      setTopic(arn); setList(null); setPage(1); setSelection(null);
      setError(''); setNotice(null);
      const fresh = await api.listSubscriptions(arn, signal);
      signal.throwIfAborted();
      setList(fresh);
    });
  }

  // A successful write and a successful read-back are distinct outcomes.
  // Never display invented rows or advise retrying a write whose ACK was observed.
  async function verifyChange(signal, check, success, accepted) {
    setSelection(null); setList(null); setPage(1);
    try {
      const fresh = await api.listSubscriptions(topic, signal);
      signal.throwIfAborted();
      setList(fresh);
      const outcome = check(fresh);
      if (outcome) {
        setPage(Math.floor((outcome - 1) / pageSize) + 1);
        setNotice({type: 'success', text: success});
      } else {
        setNotice({type: 'warning', text: accepted});
      }
    } catch (cause) {
      signal.throwIfAborted();
      setNotice({type: 'warning', text: accepted + ' ' + cause.message});
    }
  }

  function subscribe(event) {
    event.preventDefault();
    if (!topic || !endpoint) return;
    run('subscribe', async signal => {
      setError(''); setNotice(null);
      const arn = await api.subscribe(topic, protocol, endpoint, signal);
      signal.throwIfAborted();
      setCreating(false); setEndpoint('');
      await verifyChange(signal, fresh => fresh.findIndex(row =>
        row.arn === arn && row.protocol === protocol && row.endpoint === endpoint) + 1,
      t('Subscription saved locally.', 'サブスクリプションをローカルに保存しました。'),
      t('Subscribe was accepted but read-back was not verified. Refresh before retrying.', '作成は受け付けられましたが、一覧で確認できませんでした。再試行する前に一覧を更新してください。'));
    });
  }

  function unsubscribe() {
    const target = deleting;
    if (!target || !SNS_ENDPOINT_PROTOCOLS.includes(target.protocol)) return;
    run('unsubscribe', async signal => {
      setError(''); setNotice(null);
      await api.unsubscribe(topic, target.arn, signal);
      signal.throwIfAborted();
      setDeleting(null);
      await verifyChange(signal, fresh => fresh.some(row => row.arn === target.arn) ? 0 : 1,
        t('Subscription removed.', 'サブスクリプションを削除しました。'),
        t('Unsubscribe was accepted but removal was not verified. Refresh before retrying.', '削除は受け付けられましたが、一覧で確認できませんでした。再試行する前に一覧を更新してください。'));
    });
  }

  const busy = pending !== '';
  const options = topics.map(arn => ({label: arn, value: arn}));
  const currentPage = list?.slice((page - 1) * pageSize, page * pageSize) || [];
  const localOnly = <Alert type="info" header={t('Local simulation only', 'ローカルシミュレーションのみ')}>
    <SpaceBetween size="xs">
      <span>{t('HTTP and HTTPS endpoints are saved as metadata. No delivery, reachability check, or AWS confirmation occurs.',
               'HTTP と HTTPS のエンドポイントはメタデータとして保存されます。実際の配信、疎通確認、AWS の購読確認は行いません。')}</span>
      <Link href="/_simulator/docs/sns-http-subscriptions.html">{t('HTTP/S API reference (signed-in identity, us-east-1)', 'HTTP/S API リファレンス（ログイン中の ID、us-east-1）')}</Link>
    </SpaceBetween>
  </Alert>;
  const createTitle = t('Create subscription', 'サブスクリプションを作成');

  return <div className="sns-console">
    <ContentLayout header={<Header variant="h1">{creating ? createTitle : t('Subscriptions', 'サブスクリプション')}</Header>}>
      <SpaceBetween size="l">
        {localOnly}
        {notice && <Alert type={notice.type}>{notice.text}</Alert>}
        {creating ? (
          <form onSubmit={subscribe}>
            <Form errorText={error} actions={<SpaceBetween direction="horizontal" size="xs">
              <Button formAction="none" disabled={busy} onClick={() => {setCreating(false); setError('');}}>{t('Cancel', 'キャンセル')}</Button>
              <Button variant="primary" formAction="submit" disabled={!endpoint || busy} loading={pending === 'subscribe'}>{createTitle}</Button>
            </SpaceBetween>}>
              <Container header={<Header variant="h2">{t('Details', '詳細')}</Header>}>
                <SpaceBetween size="m">
                  <FormField label={t('Topic ARN', 'トピック ARN')}><Input value={topic} readOnly/></FormField>
                  <FormField label={t('Protocol', 'プロトコル')}>
                    <RadioGroup value={protocol} items={SNS_ENDPOINT_PROTOCOLS.map(value => ({value, label: value.toUpperCase(), disabled: busy}))}
                      onChange={({detail}) => setProtocol(detail.value)}/>
                  </FormField>
                  <FormField label={t('Endpoint', 'エンドポイント')} constraintText={t(
                    'Use an absolute URL with a hostname and the selected protocol.', 'ホスト名があり、選択したプロトコルと一致する絶対 URL を入力してください。')}>
                    <Input value={endpoint} disabled={busy} placeholder={protocol + '://127.0.0.1:18443/events'}
                      onChange={({detail}) => setEndpoint(detail.value)}/>
                  </FormField>
                </SpaceBetween>
              </Container>
            </Form>
          </form>
        ) : <>
          <Container header={<Header variant="h2" actions={
            <Button formAction="none" disabled={busy} loading={pending === 'topics'} onClick={loadTopics}>{t('Refresh topics', 'トピックを更新')}</Button>
          }>{t('Choose a topic', 'トピックを選択')}</Header>}>
            <form onSubmit={openTopic}>
              <SpaceBetween size="m">
                {!!topicsIssue && <Alert type="warning" header={t('Topic listing is unavailable', 'トピック一覧を取得できません')}>{topicsIssue}</Alert>}
                {options.length > 0 && <FormField label={t('Topics in this region', 'このリージョンのトピック')}>
                  <Select options={options} selectedOption={options.find(option => option.value === lookup) || null}
                    disabled={busy} filteringType="auto" placeholder={t('Select a topic', 'トピックを選択')}
                    onChange={({detail}) => setLookup(detail.selectedOption.value)}/>
                </FormField>}
                <FormField label={t('Topic ARN', 'トピック ARN')} constraintText={t(
                  'Enter an existing topic ARN directly if you cannot list topics.', '一覧を取得できない場合は、既存トピックの ARN を直接入力してください。')}>
                  <Input value={lookup} disabled={busy} onChange={({detail}) => setLookup(detail.value)}/>
                </FormField>
              </SpaceBetween>
              <div className="sns-form-actions"><Button formAction="submit" disabled={!lookup.trim() || busy}>{t('Open topic', 'トピックを開く')}</Button></div>
            </form>
          </Container>
          {!!error && !deleting && <Alert type="error">{error}</Alert>}
          {!!topic && <Table variant="container" trackBy="arn" items={currentPage}
            loading={pending === 'list' || pending === 'subscribe' || pending === 'unsubscribe'}
            loadingText={t('Reading local subscriptions', 'ローカルのサブスクリプションを読み込み中')}
            selectionType="single" selectedItems={selection ? [selection] : []}
            onSelectionChange={({detail}) => {if (!busy) setSelection(detail.selectedItems[0] || null);}}
            isItemDisabled={row => busy || !SNS_ENDPOINT_PROTOCOLS.includes(row.protocol)}
            ariaLabels={{
              tableLabel: t('Saved subscriptions', '保存済みサブスクリプション'),
              selectionGroupLabel: t('Select subscription', 'サブスクリプションを選択'),
              itemSelectionLabel: (_, row) => row.arn,
            }}
            header={<Header variant="h2" description={identifier(topic)} actions={
              <SpaceBetween direction="horizontal" size="xs">
                <Button formAction="none" disabled={busy} onClick={() => loadTopic(topic)}>{t('Refresh subscriptions', 'サブスクリプションを更新')}</Button>
                <Button formAction="none" disabled={busy || !selection} onClick={() => {setError(''); setDeleting(selection);}}>{t('Delete subscription', 'サブスクリプションを削除')}</Button>
                <Button formAction="none" variant="primary" disabled={busy} onClick={() => {setCreating(true); setProtocol('https'); setEndpoint(''); setError(''); setNotice(null);}}>{createTitle}</Button>
              </SpaceBetween>
            }>{t('Saved subscriptions', '保存済みサブスクリプション')}</Header>}
            columnDefinitions={[
              {id: 'arn', header: t('Subscription ARN', 'サブスクリプション ARN'), cell: row => identifier(row.arn)},
              {id: 'protocol', header: t('Protocol', 'プロトコル'), cell: row => row.protocol},
              {id: 'endpoint', header: t('Endpoint', 'エンドポイント'), cell: row => identifier(row.endpoint)},
            ]}
            empty={<Box textAlign="center" padding="l">{list === null ? t('Subscriptions could not be read.', 'サブスクリプションを取得できません。') :
              t('No subscriptions for this topic.', 'このトピックにサブスクリプションはありません。')}</Box>}
            pagination={list?.length > pageSize ? <Pagination currentPageIndex={page}
              pagesCount={Math.ceil(list.length / pageSize)} disabled={busy}
              ariaLabels={{
                nextPageLabel: t('Next page', '次のページ'),
                previousPageLabel: t('Previous page', '前のページ'),
                pageLabel: number => t('Page ', 'ページ ') + number,
              }}
              onChange={({detail}) => {setPage(detail.currentPageIndex); setSelection(null);}}/> : undefined}
          />}
        </>}
      </SpaceBetween>
    </ContentLayout>
    <Modal visible={!!deleting} header={t('Delete subscription', 'サブスクリプションを削除')}
      onDismiss={() => {if (!busy) setDeleting(null);}} footer={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={busy} onClick={() => setDeleting(null)}>{t('Cancel', 'キャンセル')}</Button>
        <Button formAction="none" variant="primary" loading={pending === 'unsubscribe'} disabled={busy} onClick={unsubscribe}>{t('Unsubscribe', '購読を解除')}</Button>
      </SpaceBetween>}>
      <SpaceBetween size="m">
        <Box>{t('Only this saved subscription is removed. Its topic is kept.', 'このサブスクリプションだけを削除します。トピックは残ります。')}</Box>
        {deleting && identifier(deleting.arn)}
        {!!error && <Alert type="error">{error}</Alert>}
      </SpaceBetween>
    </Modal>
  </div>;
}
