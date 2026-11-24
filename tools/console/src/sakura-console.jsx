import React, {createContext, useCallback, useMemo, useRef, useState} from 'react';
import {useLocale} from './i18n.jsx';
import './sakura-console.css';

// Local presentation coordination only: never credentials or provider authority.
export const SakuraNavigation = createContext(null);
export default function SakuraConsole({children, controls, configuration, service = 'sakura-disks', canOpenDisks = true, canOpenServers = false, onServiceChange}) {
  const {t} = useLocale();
  const [expanded, setExpanded] = useState(false);
  const listAction = useRef(null);
  const registerList = useCallback(action => {
    listAction.current = action;
    return () => { if (listAction.current === action) listAction.current = null; };
  }, []);
  const navigation = useMemo(() => ({registerList}), [registerList]);
  function select(target) {
    if (service === target) listAction.current?.();
    else onServiceChange?.(target);
    setExpanded(false);
  }
  return <SakuraNavigation.Provider value={navigation}>
    <div className="sakura-console">
      <header className="sakura-header">
        <div className="sakura-brand"><strong>Sakura Cloud</strong><span>{t('Independent local simulator', '独立したローカルシミュレーター')}</span></div>
        <div className="sakura-controls">{controls}</div>
      </header>
      <div className="sakura-scope"><span>{t('Configured zone', '設定済みゾーン')}: <strong>{configuration?.zone || '—'}</strong></span><span>{t('Scope is fixed by the host; this view cannot change it.', 'スコープはホスト側の固定設定です。この画面では変更できません。')}</span></div>
      <div className="sakura-workspace">
        <aside className="sakura-rail" aria-label={t('Sakura resource navigation', 'Sakura リソースナビゲーション')}>
          <div className="sakura-rail-title"><strong>{canOpenServers ? t('IaaS', 'IaaS') : t('Storage', 'ストレージ')}</strong><button type="button" className="sakura-menu-toggle" aria-expanded={expanded} aria-controls="sakura-disk-navigation" onClick={() => setExpanded(v => !v)}>{t('Navigation', 'ナビゲーション')}</button></div>
          <nav id="sakura-disk-navigation" className={expanded ? 'sakura-resource-nav is-expanded' : 'sakura-resource-nav'}>
            <button type="button" aria-current={service === 'sakura-disks' ? 'page' : undefined} disabled={!canOpenDisks} onClick={() => select('sakura-disks')}>{t('Disks', 'ディスク')}</button>
            {canOpenServers && <button type="button" aria-current={service === 'sakura-servers' ? 'page' : undefined} onClick={() => select('sakura-servers')}>{t('Servers', 'サーバ')}</button>}
            <p>{canOpenServers
              ? t('Disk configuration and read-only Server metadata. Networking and AppRun are not implemented.', 'ディスク構成と読み取り専用のサーバメタデータに対応します。ネットワークとAppRunは未実装です。')
              : t('Disk configuration only. Servers, networking and AppRun are not implemented.', 'ディスク構成のみ。サーバ・ネットワーク・AppRun は未実装です。')}</p>
          </nav>
        </aside>
        <main className="sakura-content">{children}</main>
      </div>
    </div>
  </SakuraNavigation.Provider>;
}
