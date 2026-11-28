import React, {useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import {useLocale} from './i18n.jsx';
import {amsIncidentCode, localDownloadName, supportClient, validSupportFile} from './support-client.mjs';

const textStyle = {whiteSpace:'pre-wrap', overflowWrap:'anywhere'};

function useRequest(onSaving) {
  const pending = useRef(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState('');
  const cancelRead = useCallback(() => {
    if (pending.current?.write) return false;
    pending.current?.controller.abort();
    pending.current = null;
    setBusy(false);
    setError('');
    return true;
  }, []);
  const run = useCallback(async (task, write = false) => {
    if (!cancelRead()) return;
    const entry = {controller:new AbortController(), write};
    pending.current = entry;
    setBusy(true);
    if (write) onSaving(true);
    try {
      await task(entry.controller.signal);
    } catch (err) {
      if (pending.current === entry) setError(err.message);
    } finally {
      if (pending.current === entry) {
        pending.current = null;
        setBusy(false);
        if (write) onSaving(false);
      }
    }
  }, [cancelRead, onSaving]);
  useLayoutEffect(() => () => {
    const entry = pending.current;
    pending.current = null;
    entry?.controller.abort();
    if (entry?.write) onSaving(false);
  }, [onSaving]);
  return {run, busy, error, cancelRead};
}

export default function SupportConsole({authenticationRequired, ...props}) {
  const {t} = useLocale();
  return authenticationRequired === true ? <Cases {...props}/> :
    <Alert type="info">{t('Sign in to open local support cases.', 'ローカルのサポートケースを開くにはサインインしてください。')}</Alert>;
}

function Cases({request, onSaving}) {
  const {t, locale} = useLocale();
  const api = useMemo(() => supportClient(request), [request]);
  const {run, busy, error, cancelRead} = useRequest(onSaving);
  const [view, setView] = useState('list'), [id, setId] = useState('');
  const [cases, setCases] = useState(null), [detail, setDetail] = useState(null);
  const [resolved, setResolved] = useState(false), [notice, setNotice] = useState('');
  const [recovery, setRecovery] = useState(null), [inspectCreate, setInspectCreate] = useState(false);
  const [subject, setSubject] = useState(''), [body, setBody] = useState('');
  const [incident, setIncident] = useState(false);
  const incidentLabel = t('AMS Advanced incident', 'AMS Advanced インシデント');
  const [file, setFile] = useState(null), [fileVersion, setFileVersion] = useState(0);
  const staged = useRef(null), heading = useRef(null), previousView = useRef(view);
  const [confirm, setConfirm] = useState(false);
  const clearDraft = () => {
    setSubject(''); setBody(''); setIncident(false); setFile(null); setFileVersion(v => v + 1); staged.current = null;
  };
  const list = useCallback(async (showResolved, signal) => {
    const data = await api.cases(showResolved, '', signal);
    signal.throwIfAborted();
    setCases(data);
  }, [api]);
  useEffect(() => { run(signal => list(false, signal)); }, [run, list]);
  useEffect(() => {
    if (previousView.current !== view) heading.current?.focus();
    previousView.current = view;
  }, [view]);
  const stateName = state => state === 'opened' ? t('Open', '対応中') : t('Resolved', '解決済み');

  function openList(showResolved = resolved) {
    if (recovery || !cancelRead()) return;
    clearDraft(); setConfirm(false); setNotice(''); setView('list'); setId(''); setDetail(null);
    setResolved(showResolved); setCases(null);
    run(signal => list(showResolved, signal));
  }
  function choose(caseId) {
    if (recovery || !cancelRead()) return;
    clearDraft(); setConfirm(false); setNotice(''); setView('detail'); setId(caseId); setDetail(null);
    run(async signal => {
      const data = await api.detail(caseId, signal);
      signal.throwIfAborted(); setDetail(data);
    });
  }
  function create() {
    if (recovery || !cancelRead()) return;
    clearDraft(); setInspectCreate(false); setNotice(''); setView('create'); setId(''); setDetail(null);
  }
  function refresh() {
    const caseId = recovery?.id || id;
    if (caseId) setDetail(null); else setCases(null);
    run(async signal => {
      if (caseId) {
        const data = await api.detail(caseId, signal);
        signal.throwIfAborted();
        setView('detail'); setId(caseId); setDetail(data);
      } else {
        const showResolved = recovery ? true : resolved;
        await list(showResolved, signal);
        signal.throwIfAborted();
        setView('list'); setResolved(showResolved);
      }
      if (recovery) {
        clearDraft();
        if (!caseId) setInspectCreate(true);
      }
      setConfirm(false); setRecovery(null); setNotice('');
    });
  }
  function moreCases() {
    run(async signal => {
      const data = await api.cases(resolved, cases.next, signal);
      signal.throwIfAborted();
      setCases({rows:[...cases.rows, ...data.rows], next:data.next});
    });
  }
  function moreMessages() {
    run(async signal => {
      const data = await api.messages(id, detail.history.next, signal);
      signal.throwIfAborted();
      setDetail({...detail, history:{rows:[...detail.history.rows, ...data.rows], next:data.next}});
    });
  }
  function save(resolving) {
    if (recovery || busy) return;
    setNotice('');
    run(async signal => {
      let caseId = id, acknowledged = false, sending = resolving;
      try {
        if (resolving) {
          await api.resolve(caseId, signal);
        } else {
          let setId;
          if (file) {
            if (staged.current?.file !== file || staged.current.expires <= Date.now()) {
              staged.current = await api.stage(file, signal);
            }
            setId = staged.current.id;
          }
          sending = true;
          caseId = await api.send(caseId, subject, body, setId, locale, signal, incident);
        }
        acknowledged = true;
        const data = await api.detail(caseId, signal);
        signal.throwIfAborted();
        const matches = msg => msg.body === body && (!file || msg.attachmentSet?.some(f => f.fileName === file.name));
        if (resolving ? data.item.status !== 'resolved' : ((!id && (data.item.subject !== subject || data.item.serviceCode !== (incident ? amsIncidentCode : undefined))) || !data.history.rows.some(matches))) {
          throw Error(t('The latest case details did not confirm the change.', '最新のケース詳細で変更を確認できませんでした。'));
        }
        setView('detail'); setId(caseId); setDetail(data); clearDraft(); setConfirm(false);
        setNotice(resolving ? t('Case resolved.', 'ケースを解決しました。') : id ?
          t('Message added.', 'メッセージを追加しました。') : t('Case created.', 'ケースを作成しました。'));
      } catch (err) {
        if (!signal.aborted && (acknowledged || !err.rejected)) {
          setRecovery({id:caseId, uploadOnly:!sending});
          if (sending) setDetail(null);
          setConfirm(false);
        }
        throw err;
      }
    }, true);
  }
  function download(meta) {
    run(async signal => {
      const blob = await api.download(meta, signal);
      signal.throwIfAborted();
      const url = URL.createObjectURL(blob), link = document.createElement('a');
      link.href = url; link.download = localDownloadName(meta.fileName);
      document.body.appendChild(link);
      try { link.click(); } finally { link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); }
    });
  }

  const fileValid = !file || validSupportFile(file);
  const canSave = body.trim() && [...body].length <= 8000 && fileValid &&
    (view !== 'create' || (subject.trim() && [...subject].length <= 256));
  const locked = busy || !!recovery;
  const form = <Container header={<Header variant="h2">{view === 'create' ? t('Case details', 'ケース情報') : t('Add a message', 'メッセージを追加')}</Header>}>
    <form onSubmit={event => {event.preventDefault(); if (canSave && !locked) save(false);}}>
      <SpaceBetween size="m">
        {view === 'create' && <FormField label={t('Subject', '件名')} constraintText={t('Up to 256 characters.', '256文字以内。')}>
          <Input value={subject} onChange={({detail}) => setSubject(detail.value)} disabled={locked}/>
        </FormField>}
        {view === 'create' && <Checkbox checked={incident} disabled={locked} onChange={({detail}) => setIncident(detail.checked)}>
          {incidentLabel}</Checkbox>}
        <FormField label={t('Message', 'メッセージ')} constraintText={t('Up to 8,000 characters.', '8,000文字以内。')}>
          <Textarea value={body} onChange={({detail}) => setBody(detail.value)} disabled={locked}/>
        </FormField>
        <FormField label={t('Attachment (optional)', '添付ファイル（任意）')} controlId="support-file"
          constraintText={t('One file per message, up to 64 KiB.', 'メッセージごとに1ファイル、64 KiB以内。')}
          errorText={!fileValid ? t('Choose a file up to 64 KiB with a name of up to 255 characters.', 'ファイルは64 KiB以内、ファイル名は255文字以内にしてください。') : undefined}>
          <input type="file" id="support-file" key={fileVersion} disabled={locked}
            onChange={event => {staged.current = null; setFile(event.target.files?.[0] || null);}}/>
        </FormField>
        <Button variant="primary" formAction="submit" disabled={locked || !canSave}>{view === 'create' ?
          t('Create case', 'ケースを作成') : t('Send message', 'メッセージを送信')}</Button>
      </SpaceBetween>
    </form>
  </Container>;

  return <ContentLayout header={<div ref={heading} tabIndex={-1}><Header variant="h1"
    description={view === 'list' ? t('Cases and messages are stored in this simulator team.', 'ケースとメッセージはこのシミュレーターのチーム内に保存されます。') : undefined}>
    {view === 'list' ? t('Support cases', 'サポートケース') : view === 'create' ? t('Create a support case', 'サポートケースを作成') :
      t('Support case details', 'サポートケースの詳細')}</Header></div>}>
    <SpaceBetween size="l">
      {error && <Alert type="error">{error}</Alert>}
      {notice && <Alert type="success">{notice}</Alert>}
      {recovery?.uploadOnly && <Alert type="warning" action={<Button onClick={() => {
        cancelRead(); staged.current = null; setFile(null); setFileVersion(v => v + 1); setRecovery(null);
      }}>{t('Clear file selection', 'ファイル選択を解除')}</Button>}>
        {t('The attachment upload could not be confirmed. The message is still a draft. Clear the file selection and choose a file to try again.',
          'ファイルのアップロード結果を確認できませんでした。メッセージは下書きです。ファイル選択を解除し、ファイルを選び直してください。')}
      </Alert>}
      {recovery && !recovery.uploadOnly && <Alert type="warning" action={<Button disabled={busy} onClick={refresh}>{recovery.id ?
        t('Review this case', 'このケースを再確認') : t('Review the case list', 'ケース一覧を確認')}</Button>}>
        {t('Submission could not be confirmed. Review the stored cases and messages before submitting again.',
          '送信結果を確認できませんでした。再送信する前に保存されたケースとメッセージを確認してください。')}
        {!recovery.id && <p>{t('No case ID was received. Compare the messages in matching cases, including older pages.',
          'ケースIDを受け取れませんでした。以前のページも含め、同じ件名のケース内のメッセージを確認してください。')}</p>}
      </Alert>}
      {busy && <StatusIndicator type="loading">{t('Loading AWS Support', 'AWS Support を読み込み中')}</StatusIndicator>}
      {view !== 'list' && <Button disabled={!!recovery} onClick={() => openList()}>{t('Back to cases', 'ケース一覧に戻る')}</Button>}
      {view === 'list' && <SpaceBetween size="m">
        {inspectCreate && <Alert type="info">{t('The last creation result is unknown. Open cases with matching subjects and compare the initial messages.',
          '直前の作成結果は不明です。同じ件名のケースを開き、最初のメッセージを照合してください。')}</Alert>}
        <Checkbox checked={resolved} disabled={locked} onChange={({detail}) => openList(detail.checked)}>
          {t('Include resolved cases', '解決済みのケースを含める')}</Checkbox>
        <Table items={cases?.rows || []} loading={busy} loadingText={t('Loading cases', 'ケースを読み込み中')}
          ariaLabels={{tableLabel:t('Cases', 'ケース一覧')}} empty={cases ? t('No cases match this list.', '該当するケースはありません。') :
            t('Cases have not been loaded.', 'ケースを読み込んでいません。')}
          header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">
            <Button disabled={locked} onClick={refresh}>{t('Refresh cases', 'ケースを再読み込み')}</Button>
            <Button variant="primary" disabled={locked} onClick={create}>{t('Create case', 'ケースを作成')}</Button>
          </SpaceBetween>}>{t('Cases', 'ケース一覧')}</Header>}
          columnDefinitions={[
            {id:'subject', header:t('Subject', '件名'), cell: row => <Button variant="inline-link" disabled={!!recovery} onClick={() => choose(row.caseId)}>{row.subject}</Button>},
            {id:'service', header:t('Service', 'サービス'), cell: row => row.serviceCode === amsIncidentCode ? incidentLabel : '—'},
            {id:'display', header:t('Case number', 'ケース番号'), cell: row => row.displayId},
            {id:'status', header:t('Status', 'ステータス'), cell: row => stateName(row.status)},
          ]}/>
        {cases?.next && <Button disabled={locked} onClick={moreCases}>{t('Load older cases', '以前のケースを読み込む')}</Button>}
      </SpaceBetween>}
      {view === 'create' && form}
      {view === 'detail' && !detail && !recovery && !busy && <Button onClick={refresh}>{t('Refresh case', 'ケースを再読み込み')}</Button>}
      {view === 'detail' && detail && <>
        <Container header={<Header variant="h2" actions={<Button disabled={locked} onClick={refresh}>{t('Refresh case', 'ケースを再読み込み')}</Button>}>
          <span style={textStyle}>{detail.item.subject}</span></Header>}>
          <dl><dt>{t('Case number', 'ケース番号')}</dt><dd>{detail.item.displayId}</dd>
            <dt>{t('Case ID', 'ケースID')}</dt><dd style={textStyle}>{id}</dd>
            {detail.item.serviceCode === amsIncidentCode && <><dt>{t('Service', 'サービス')}</dt><dd>{incidentLabel}</dd></>}
            <dt>{t('Status', 'ステータス')}</dt><dd>{stateName(detail.item.status)}</dd>
            <dt>{t('Created', '作成日時')}</dt><dd>{detail.item.timeCreated}</dd></dl>
        </Container>
        <Container header={<Header variant="h2">{t('Messages', 'メッセージ履歴')}</Header>}>
          <SpaceBetween size="m">
            <ol>{detail.history.rows.map((msg, i) => <li key={i} style={textStyle}>
              <p>{msg.body}</p><small>{msg.submittedBy} · <time>{msg.timeCreated}</time></small>
              {msg.attachmentSet?.length > 0 && <ul aria-label={t('Attachments', '添付ファイル')}>{msg.attachmentSet.map(meta =>
                <li key={meta.attachmentId}><span>{meta.fileName}</span> <Button variant="inline-link" disabled={locked}
                  ariaLabel={t('Download ' + meta.fileName, meta.fileName + ' をダウンロード')} onClick={() => download(meta)}>
                  {t('Download', 'ダウンロード')}</Button><br/>{t('Attachment ID', '添付ファイルID')} {meta.attachmentId}</li>)}</ul>}
            </li>)}</ol>
            {detail.history.next && <Button disabled={locked} onClick={moreMessages}>{t('Load older messages', '以前のメッセージを読み込む')}</Button>}
          </SpaceBetween>
        </Container>
        {detail.item.status === 'opened' && <>{form}
          {confirm ? <Alert type="warning" header={t('Resolve this case?', 'このケースを解決しますか？')}
            action={<SpaceBetween direction="horizontal" size="s"><Button disabled={locked} onClick={() => setConfirm(false)}>{t('Cancel', 'キャンセル')}</Button>
              <Button disabled={locked} onClick={() => save(true)}>{t('Confirm resolution', '解決を確定')}</Button></SpaceBetween>}>
            {t('Resolving the case closes it to further messages.', '解決すると、このケースへのメッセージの追加を終了します。')}</Alert> :
            <Button disabled={locked} onClick={() => setConfirm(true)}>{t('Resolve case', 'ケースを解決')}</Button>}
        </>}
      </>}
    </SpaceBetween>
  </ContentLayout>;
}
