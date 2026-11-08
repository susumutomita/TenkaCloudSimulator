import React from 'react';
import {useLocale} from './i18n.jsx';
import './azure-blob.css';

// Integration owns provider/session controls. Resource views own their scope and
// data permissions; this chrome does not introduce subscriptions or authority.
export default function AzureConsoleShell({children,controls}) {
  const {t}=useLocale();
  return <div className="azure-console-shell">
    <header className="azure-portal-header"><span className="azure-console-brand">Azure <span>{t('local simulator','ローカルシミュレーター')}</span></span><div className="azure-shared-controls">{controls}</div></header>
    <div className="azure-console-content">{children}</div>
  </div>;
}
