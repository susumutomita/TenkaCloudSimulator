import React, {useId, useState} from 'react';
import {useLocale} from './i18n.jsx';
import {blankEndpoint, srvForm, srvRecord} from './gcp-dns-srv.mjs';
import './gcp-dns-srv.css';

export default function SRVForm({mode, original, zone, busy, onCancel, onSave}) {
  const {t} = useLocale(), id = useId();
  const [draft, setDraft] = useState(() => srvForm(original));
  const [invalid, setInvalid] = useState('');
  const titles = {create: t('Add SRV record', 'SRV レコードを追加'), edit: t('Edit SRV record', 'SRV レコードを編集'),
    delete: t('Delete SRV record', 'SRV レコードを削除')};
  const messages = {
    name: t('Enter an absolute lowercase DNS name inside this zone, ending with a dot.', 'ゾーン内の絶対 DNS 名を小文字で入力し、末尾にドットを付けてください。'),
    ttl: t('TTL must be a whole number from 0 to 2147483647.', 'TTL は 0〜2147483647 の整数で入力してください。'),
    numbers: t('Priority, weight and port must each be 0–65535, without leading zeros.', '優先度、重み、ポートはそれぞれ 0〜65535 の整数で、先頭に余分なゼロは付けないでください。'),
    target: t('Use an absolute lowercase target ending with a dot, or just a dot for an unavailable service.', 'ターゲットは末尾にドットを含む小文字の絶対名で入力してください。利用不可の場合はドットのみです。'),
    duplicate: t('Each endpoint must be distinct.', '同じエンドポイントが重複しています。'),
    endpoints: t('Use 1–32 endpoints.', 'エンドポイントは 1〜32 件です。'),
  };
  function submit(e) {
    e.preventDefault();
    if (busy) return;
    let next = null;
    if (mode !== 'delete') {
      try { next = srvRecord(draft, zone); } catch (error) { setInvalid(error.message); return; }
    }
    setInvalid(''); onSave(original, next);
  }
  function updateEndpoint(index, key, value) {
    setDraft(s => ({...s, endpoints: s.endpoints.map((row, i) => i === index ? {...row, [key]: value} : row)}));
  }
  const fields = [['priority', t('Priority', '優先度')], ['weight', t('Weight', '重み')],
    ['port', t('Port', 'ポート')], ['target', t('Target', 'ターゲット')]];
  return <form className="gcp-storage-form gcp-dns-srv-form" aria-labelledby={id} onSubmit={submit} aria-busy={busy}>
    <h3 id={id}>{titles[mode]}</h3>
    {invalid && <p role="alert">{messages[invalid]}</p>}
    <fieldset disabled={busy}>
      {mode === 'delete' ? <>
        <p>{t('Delete the entire SRV record set?', 'この SRV レコードセット全体を削除しますか？')} <strong>{original.name}</strong></p>
        <p>TTL: {original.ttl}</p><ul>{original.rrdatas.map(value => <li key={value}>{value}</li>)}</ul>
      </> : <>
        <label htmlFor={id + '-name'}>{t('SRV record name', 'SRV レコード名')}</label>
        <input id={id + '-name'} value={draft.name} required readOnly={!!original} maxLength={253}
          placeholder={'_service._tcp.' + zone} onChange={e => setDraft({...draft, name: e.target.value})}/>
        <label htmlFor={id + '-ttl'}>{t('TTL (seconds)', 'TTL（秒）')}</label>
        <input id={id + '-ttl'} inputMode="numeric" required value={draft.ttl}
          onChange={e => setDraft({...draft, ttl: e.target.value})}/>
        {draft.endpoints.map((row, index) => <fieldset key={index}>
          <legend>{t('Endpoint', 'エンドポイント')} {index + 1}</legend>
          {fields.map(([key, label]) => <React.Fragment key={key}>
            <label htmlFor={id + '-' + index + '-' + key}>{label}</label>
            <input id={id + '-' + index + '-' + key} required value={row[key]}
              inputMode={key === 'target' ? 'text' : 'numeric'} maxLength={key === 'target' ? 253 : 5}
              placeholder={key === 'target' ? 'host.' + zone : undefined}
              onChange={e => updateEndpoint(index, key, e.target.value)}/>
          </React.Fragment>)}
          {draft.endpoints.length > 1 && <button type="button" onClick={() =>
            setDraft(s => ({...s, endpoints: s.endpoints.filter((_, i) => i !== index)}))}>{t('Remove endpoint', 'エンドポイントを外す')}</button>}
        </fieldset>)}
        <button type="button" disabled={draft.endpoints.length >= 32} onClick={() =>
          setDraft(s => ({...s, endpoints: [...s.endpoints, blankEndpoint()]}))}>{t('Add endpoint', 'エンドポイントを追加')}</button>
      </>}
      <div className="gcp-storage-actions">
        <button type="button" onClick={onCancel}>{t('Cancel', 'キャンセル')}</button>
        <button type="submit" className="gcp-storage-primary">{mode === 'delete' ? t('Confirm deletion', '削除を確定') : t('Save SRV record', 'SRV レコードを保存')}</button>
      </div>
    </fieldset>
  </form>;
}
