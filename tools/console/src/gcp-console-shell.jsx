import React, {createContext, useCallback, useMemo, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import './gcp-storage.css';

// Presentation-only coordination: neither scope nor navigation grants authority.
export const GCPConsoleContext = createContext(null);
export default function GCPConsoleShell({children, controls, product = 'Cloud Storage', resource}) {
  const {t} = useLocale();
  const [project, setProject] = useState(''), [expanded, setExpanded] = useState(false);
  const navigate = useRef(null);
  const registerNavigation = useCallback(callback => {
    navigate.current = callback;
    return () => { if (navigate.current === callback) navigate.current = null; };
  }, []);
  const context = useMemo(() => ({setProject, registerNavigation}), [registerNavigation]);
  function buckets() { navigate.current?.(); setExpanded(false); }
  return <GCPConsoleContext.Provider value={context}>
    <div className="gcp-console-shell">
      <header className="gcp-console-header">
        <div className="gcp-console-brand"><span>Google Cloud</span><span className="gcp-console-local">{t('Independent local simulator', '独立したローカルシミュレーター')}</span></div>
        <div className="gcp-console-controls">{controls}</div>
      </header>
      <div className="gcp-console-scope"><span>{t('Project', 'プロジェクト')}: <strong>{project || t('Loading…', '読み込み中…')}</strong></span><span>{t('Configured locally · project switching unavailable', 'ローカル設定 · プロジェクト切替は未対応')}</span></div>
      <div className="gcp-console-workspace">
        <aside className="gcp-console-rail" aria-label={product === 'Cloud Storage' ? t('Cloud Storage navigation', 'Cloud Storage ナビゲーション') : product}>
          <div className="gcp-console-product"><strong>{product}</strong><button type="button" className="gcp-console-mobile-toggle" aria-expanded={expanded} aria-controls="gcp-console-resource-nav" onClick={() => setExpanded(value => !value)}>{t('Navigation', 'ナビゲーション')}</button></div>
          {resource ? <nav id="gcp-console-resource-nav" className={expanded ? 'gcp-console-resource-nav is-open' : 'gcp-console-resource-nav'} aria-label={product}>
            <button type="button" aria-current="page">{resource}</button>
          </nav> : <nav id="gcp-console-resource-nav" className={expanded ? 'gcp-console-resource-nav is-open' : 'gcp-console-resource-nav'} aria-label={t('Cloud Storage resources', 'Cloud Storage リソース')}>
            <button type="button" aria-current="page" onClick={buckets}>{t('Buckets', 'バケット')}</button>
            <p>{t('Only implemented Cloud Storage operations appear here.', '実装済みの Cloud Storage 操作のみ表示します。')}</p>
          </nav>}
        </aside>
        <main className="gcp-console-content">{children}</main>
      </div>
    </div>
  </GCPConsoleContext.Provider>;
}
