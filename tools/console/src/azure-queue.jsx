import React, {useEffect, useId, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import {azureQueue, queueErrorCode, queueMaxTTL, queueReferenceForPrincipal, validQueueAccount, validQueueName} from './azure-queue-requests.mjs';
import './azure-queue.css';

const errors = {
  QueueNotFound: ['The named queue was not found. Check the name.', '指定したキューが見つかりません。名前を確認してください。'],
  MessageNotFound: ['The message has expired or is no longer in the queue.', 'メッセージが期限切れか、キューに存在しません。'],
  PopReceiptMismatch: ['The receipt is no longer current. Receive the message again when it is visible.', '受領証が更新されています。メッセージが表示可能になったら再受信してください。'],
  QuotaExceeded: ['The local storage quota was reached.', 'ローカルストレージの上限に達しました。'],
  SignIn: ['Sign in to the simulator first.', '先にシミュレーターにサインインしてください。'],
  Denied: ['This simulator session is not the configured local account owner.', 'このセッションにはローカルアカウントの所有権限がありません。'],
  InvalidLifetime: ['Use a lifetime of -1 or 1 to 604800 seconds, with an initial delay shorter than its lifetime.', '有効期間は -1 または 1〜604800 秒、初回の表示遅延は有効期間未満にしてください。'],
  InvalidNumber: ['Check the numeric limits on the form.', '入力欄の数値の範囲を確認してください。'],
  InvalidText: ['Message text must be valid UTF-8 XML text.', 'メッセージ本文に XML で使えない文字が含まれています。'],
  MessageTooLarge: ['Message text exceeds 64 KiB.', 'メッセージ本文が 64 KiB を超えています。'],
  MissingReceipt: ['Receive the message before editing or deleting it.', '編集・削除するには、まずメッセージを受信してください。'],
  InvalidResponse: ['The server response did not match the Queue Storage contract. Check the queue state before retrying.', 'サーバー応答が Queue Storage の形式と一致しません。再試行の前にキューの状態を確認してください。'],
  ResponseTooLarge: ['The response was too large to display. Try a smaller batch.', '応答が大きすぎて表示できません。件数を減らしてください。'],
  Deadline: ['The request timed out. Check the queue state before retrying a write.', '要求がタイムアウトしました。書き込みの再試行前に状態を確認してください。'],
  RequestFailed: ['The request did not complete. Check the queue state and see the local API reference.', '要求を完了できませんでした。キューの状態とローカル API リファレンスを確認してください。'],
};
function presentedError(error) {
  const code = queueErrorCode(error);
  if (['AuthenticationFailed', 'Sign in to continue.', 'HTTP401', '401'].includes(code)) return 'SignIn';
  if (['AuthorizationFailure', 'HTTP403', '403'].includes(code)) return 'Denied';
  if (['HTTP404', '404'].includes(code)) return 'QueueNotFound';
  if (['HTTP409', '409'].includes(code)) return 'QuotaExceeded';
  return Object.hasOwn(errors, code) ? code : 'RequestFailed';
}

function MessageDetails({item}) {
  const {t} = useLocale();
  return <details>
    <summary>{item.id}</summary>
    <dl>
      <dt>{t('Inserted (UTC)', '追加日時 (UTC)')}</dt><dd>{item.inserted}</dd>
      <dt>{t('Expires (UTC)', '有効期限 (UTC)')}</dt><dd>{item.expires}</dd>
      <dt>{t('Dequeue count', '受信回数')}</dt><dd>{item.count}</dd>
      {item.visible && <><dt>{t('Next visible (UTC)', '次回表示日時 (UTC)')}</dt><dd>{item.visible}</dd></>}
    </dl>
    <pre aria-label={t('Message text', 'メッセージ本文')}>{item.text}</pre>
  </details>;
}

export default function AzureQueueView({request, account, principal}) {
  const reference = queueReferenceForPrincipal(principal);
  // Root also keys this view by authenticated subject and environment.
  return <NamedQueue key={account} request={request} account={account} reference={reference}/>;
}

function NamedQueue({request, account, reference}) {
  const {t} = useLocale(), title = useId(), nameId = useId(), hint = useId(), confirmID = useId();
  const active = useRef(null), heading = useRef(null), input = useRef(null);
  const [name, setName] = useState(''), [queue, setQueue] = useState('');
  const [observed, setObserved] = useState(null), [received, setReceived] = useState(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), [notice, setNotice] = useState(null);
  const [message, setMessage] = useState(''), [lifetime, setLifetime] = useState('604800'), [delay, setDelay] = useState('0');
  const [limit, setLimit] = useState('8'), [duration, setDuration] = useState('30');
  const [selectedId, setSelectedId] = useState(''), [editDelay, setEditDelay] = useState('0');
  const [replaceBody, setReplaceBody] = useState(false), [editText, setEditText] = useState('');
  const [deleteMessage, setDeleteMessage] = useState(false), [queueConfirmation, setQueueConfirmation] = useState('');
  const configured = validQueueAccount(account);
  const selected = received?.find(item => item.id === selectedId);

  useEffect(() => () => {
    const job = active.current;
    active.current = null;
    job?.abort();
  }, []);
  useEffect(() => { if (queue) heading.current?.focus(); }, [queue]);

  async function run(queueName, work, done, failed) {
    if (active.current || !configured || !validQueueName(queueName)) return;
    const job = new AbortController();
    active.current = job;
    const timer = setTimeout(() => job.abort(), 30000);
    setBusy(true);
    setNotice(null);
    setError('');
    try {
      const result = await work(azureQueue(request, account, queueName), job.signal);
      job.signal.throwIfAborted();
      if (active.current === job) done(result);
    } catch (cause) {
      if (active.current === job) {
        const key = job.signal.aborted ? 'Deadline' : presentedError(cause);
        setError(key);
        if (!['InvalidLifetime', 'InvalidNumber', 'InvalidText', 'MessageTooLarge'].includes(key)) failed?.();
      }
    } finally {
      clearTimeout(timer);
      if (active.current === job) {
        active.current = null;
        setBusy(false);
      }
    }
  }

  function choose(item) {
    setSelectedId(item?.id ?? '');
    setEditDelay('0');
    setReplaceBody(false);
    setEditText(item?.text ?? '');
    setDeleteMessage(false);
  }
  function setBatch(batch) {
    setReceived(batch);
    choose(batch?.[0]);
  }
  function resetWork() {
    setObserved(null);
    setBatch(null);
    setMessage('');
    setQueueConfirmation('');
  }
  function open(create) {
    if (create) run(name, (client, signal) => client.create(signal), created => {
      resetWork();
      setQueue(name);
      setNotice(created ? ['Queue created.', 'キューを作成しました。'] : ['Queue already exists; opened without changing it.', '既存のキューを変更せずに開きました。']);
    });
    else run(name, (client, signal) => client.peek(limit, signal), rows => {
      resetWork();
      setQueue(name);
      setObserved(rows);
    });
  }

  return <section className="azure-queue-view" aria-labelledby={title} aria-busy={busy}>
    <nav className="azure-queue-trail" aria-label={t('Queue Storage location', 'Queue Storage の現在地')}>
      {t('Storage accounts', 'ストレージ アカウント')} / {configured ? account : t('Not configured', '未設定')} / {t('Queues', 'キュー')}
      {queue && <> / <strong aria-current="page">{queue}</strong></>}
    </nav>
    <header>
      <h1 id={title} ref={heading} tabIndex={-1}>{queue || 'Azure Queue Storage'}</h1>
      {reference && <a href={reference} referrerPolicy="no-referrer">{t('Local API reference', 'ローカル API リファレンス')}</a>}
    </header>
    <p id={hint}>{t('Named queues only. This simulator does not list queues in the account, manage metadata, or connect to Azure.',
      'キュー名を指定して操作します。アカウント内のキュー一覧、メタデータ管理、実際の Azure 接続には対応していません。')}</p>
    {!configured && <p role="alert">{t('No valid local storage account is configured.', '有効なローカルストレージ アカウントが設定されていません。')}</p>}
    {error && <p className="azure-queue-error" role="alert">{t(...errors[error])}</p>}
    <p className="azure-queue-status" role="status">{busy ? t('Request in progress…', '処理中…') : notice && t(...notice)}</p>
    {!queue ? <form onSubmit={e => {e.preventDefault();open(false);}}>
      <fieldset disabled={busy || !configured}>
        <legend>{t('Queues','キュー')}</legend>
        <label htmlFor={nameId}>{t('Queue name', 'キュー名')}</label>
        <input id={nameId} ref={input} value={name} onChange={e => setName(e.target.value)} autoComplete="off" maxLength={63} aria-describedby={hint}/>
        <p>{t('Use the exact existing name, or create a new queue. Use 3–63 lowercase letters, digits or hyphens; no consecutive hyphens.',
          '既存のキュー名を入力するか、新規作成してください。半角の英小文字・数字・ハイフンで3〜63文字。ハイフンの連続は使用できません。')}</p>
        <div className="azure-queue-toolbar">
          <button className="azure-queue-primary" type="submit" disabled={!validQueueName(name)}>{t('Open queue','キューを開く')}</button>
          <button type="button" disabled={!validQueueName(name)} onClick={() => open(true)}>{t('Create queue','キューを作成')}</button>
        </div>
      </fieldset>
    </form> : <>
      <div className="azure-queue-toolbar">
        <button type="button" disabled={busy} onClick={() => {resetWork();setQueue('');setError('');setNotice(null);requestAnimationFrame(() => input.current?.focus());}}>{t('Switch queue','別のキューを開く')}</button>
      </div>
      <form onSubmit={e => {
        e.preventDefault();
        run(queue, (client, signal) => client.send({text:message, ttl:lifetime, delay}, signal), () => {
          setObserved(null);setMessage('');setNotice(['Message added. Use Peek to see visible messages.', 'メッセージを追加しました。表示可能なメッセージは Peek で確認できます。']);
        }, () => setObserved(null));
      }}>
        <fieldset disabled={busy}>
          <legend>{t('Add message','メッセージを追加')}</legend>
          <label>{t('Message text (up to 64 KiB)','メッセージ本文（最大 64 KiB）')}<textarea value={message} onChange={e => setMessage(e.target.value)} maxLength={65536} rows={3}/></label>
          <div className="azure-queue-fields">
            <label>{t('Lifetime in seconds (-1 to keep indefinitely)','有効期間（秒、-1 で無期限）')}
              <input type="number" min="-1" max={queueMaxTTL} step="1" required value={lifetime} onChange={e => setLifetime(e.target.value)}/></label>
            <label>{t('Initial visibility delay (seconds)','初回表示までの遅延（秒）')}
              <input type="number" min="0" max={queueMaxTTL} step="1" required value={delay} onChange={e => setDelay(e.target.value)}/></label>
          </div>
          <button className="azure-queue-primary" type="submit">{t('Add message','メッセージを追加')}</button>
        </fieldset>
      </form>

      <fieldset disabled={busy}>
        <legend>{t('Messages','メッセージ')}</legend>
        <div className="azure-queue-fields">
          <label>{t('Maximum messages (1–32)','最大件数（1〜32）')}
            <input type="number" min="1" max="32" step="1" value={limit} onChange={e => setLimit(e.target.value)}/></label>
          <label>{t('Receive visibility timeout (seconds)','受信時の非表示時間（秒）')}
            <input type="number" min="1" max={queueMaxTTL} step="1" value={duration} onChange={e => setDuration(e.target.value)}/></label>
        </div>
        <p>{t('Peek only reads. Receive temporarily hides messages without deleting them. This page keeps receipts for the latest receive only.',
          'Peek は読み取りのみです。受信したメッセージは削除せず一時的に非表示にします。この画面には最後に受信した分の受領証だけを保持します。')}</p>
        <div className="azure-queue-toolbar">
          <button type="button" onClick={() => run(queue, (client, signal) => client.peek(limit,signal), setObserved, () => setObserved(null))}>{t('Peek visible messages','表示可能なメッセージを Peek')}</button>
          <button type="button" onClick={() => run(queue, (client, signal) => client.receive(limit,duration,signal),
            rows => {setBatch(rows);setObserved(null);}, () => {setBatch(null);setObserved(null);})}>{t('Receive and reserve','受信して確保')}</button>
        </div>
      </fieldset>
      {observed !== null && <section aria-label={t('Peek results','Peek の結果')}>
        <h2>{t('Last peek','直近の Peek')}</h2>
        {observed.length === 0 ? <p>{t('No visible messages. Reserved messages may still exist.','表示可能なメッセージはありません。確保中のメッセージは残っている場合があります。')}</p> :
          <ul className="azure-queue-results">{observed.map(row => <li key={row.id}><MessageDetails item={row}/></li>)}</ul>}
      </section>}
      {received !== null && <section aria-label={t('Receive results','受信結果')}>
        <h2>{t('Last receive','直近の受信')}</h2>
        {received.length === 0 ? <p>{t('No messages received.','受信したメッセージはありません。')}</p> : <form onSubmit={e => {
          e.preventDefault();
          if (!selected) return;
          run(queue, (client, signal) => client.update(selected,{visibility:editDelay,text:replaceBody?editText:undefined},signal), result => {
            const row = {...selected,...result,text:replaceBody?editText:selected.text};
            setReceived(old => old.map(item => item.id === row.id ? row : item));
            choose(row);setObserved(null);setNotice(['Message updated. This page now uses the new receipt.', 'メッセージを更新しました。新しい受領証に切り替えました。']);
          }, () => {setBatch(null);setObserved(null);});
        }}>
          <fieldset disabled={busy}>
            <legend>{t('Manage a received message','受信したメッセージを操作')}</legend>
            <label>{t('Received message','受信したメッセージ')}
              <select value={selectedId} onChange={e => choose(received.find(item => item.id === e.target.value))}>
                {received.map(item => <option key={item.id} value={item.id}>{item.id}</option>)}
              </select>
            </label>
            {selected && <>
              <MessageDetails item={selected}/>
              <label>{t('New visibility delay (seconds)','新しい表示遅延（秒）')}
                <input type="number" min="0" max={queueMaxTTL} step="1" required value={editDelay} onChange={e => setEditDelay(e.target.value)}/></label>
              <label className="azure-queue-check"><input type="checkbox" checked={replaceBody} onChange={e => setReplaceBody(e.target.checked)}/>
                {t('Also replace the message text','メッセージ本文も上書き')}</label>
              {replaceBody && <label>{t('Replacement text','新しい本文')}<textarea value={editText} onChange={e => setEditText(e.target.value)} maxLength={65536} rows={3}/></label>}
              <button type="submit">{t('Update message','メッセージを更新')}</button>
              <label className="azure-queue-check"><input type="checkbox" checked={deleteMessage} onChange={e => setDeleteMessage(e.target.checked)}/>
                {t('Permanently delete this message','このメッセージを完全に削除する')}</label>
              <button className="azure-queue-danger" type="button" disabled={!deleteMessage} onClick={() =>
                run(queue, (client, signal) => client.remove(selected,signal), () => {
                  setBatch(received.filter(item => item.id !== selected.id));setObserved(null);setNotice(['Message deleted.', 'メッセージを削除しました。']);
                }, () => {setBatch(null);setObserved(null);})}>{t('Delete message','メッセージを削除')}</button>
            </>}
          </fieldset>
        </form>}
      </section>}
      <fieldset disabled={busy}>
        <legend>{t('Queue cleanup','キューの削除・消去')}</legend>
        <label htmlFor={confirmID}>{t('Type the exact queue name to confirm','確認のため正確なキュー名を入力')}</label>
        <input id={confirmID} autoComplete="off" value={queueConfirmation} onChange={e => setQueueConfirmation(e.target.value)} maxLength={63}/>
        <p>{t('Both actions permanently remove visible and reserved messages. Clear keeps the queue; Delete removes it.',
          'どちらの操作も表示可能なメッセージと確保中のメッセージを完全に消去します。「消去」はキューを残し、「削除」はキュー自体を削除します。')}</p>
        <div className="azure-queue-toolbar">
          <button className="azure-queue-danger" type="button" disabled={queueConfirmation!==queue} onClick={() =>
            run(queue, (client, signal) => client.clear(signal), () => {resetWork();setNotice(['All messages cleared. The queue still exists.','すべてのメッセージを消去しました。キューは残っています。']);},
              () => {setBatch(null);setObserved(null);setQueueConfirmation('');})}>{t('Clear messages','メッセージを消去')}</button>
          <button className="azure-queue-danger" type="button" disabled={queueConfirmation!==queue} onClick={() =>
            run(queue, (client, signal) => client.deleteQueue(signal), () => {resetWork();setQueue('');setNotice(['Queue deleted.','キューを削除しました。']);},
              () => {setBatch(null);setObserved(null);setQueueConfirmation('');})}>{t('Delete queue','キューを削除')}</button>
        </div>
      </fieldset>
    </>}
  </section>;
}
