import React, {useEffect, useRef, useState} from 'react';
import TopNavigation from '@cloudscape-design/components/top-navigation';
import {awsLanguageSettings} from './aws-language-settings.mjs';
import {useLocale} from './i18n.jsx';
import {PROVIDERS, providerForService, servicesForProvider} from './console-navigation.mjs';

// Neutral simulator controls. Provider shells own their project/account chrome.
export default function GlobalNavigation({service, capabilities, saving, onNavigate,
  principal, authenticationRequired, identitySelection, onIdentity, onSignOut,
  region, regions, onRegion, sessionError}) {
  const {locale, setLocale, t} = useLocale();
  const provider = providerForService(service) ?? 'aws';
  const [open, setOpen] = useState(false), [query, setQuery] = useState('');
  const trigger = useRef(null), panel = useRef(null), input = useRef(null);
  const results = servicesForProvider(provider, capabilities, query);
  function close() { setOpen(false); requestAnimationFrame(() => trigger.current?.focus()); }
  useEffect(() => {
    if (!open) return;
    input.current?.focus();
    function outside(event) {
      if (!panel.current?.contains(event.target) && !trigger.current?.contains(event.target)) setOpen(false);
    }
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [open]);
  useEffect(() => { setOpen(false); setQuery(''); }, [provider, service]);
  function choose(id) {
    if (saving) return;
    const nextProvider = providerForService(id);
    onNavigate(id); setOpen(false); setQuery('');
    requestAnimationFrame(() => {
      if (nextProvider === provider) trigger.current?.focus();
      else document.querySelector('.cloud-provider-choices button[aria-pressed="true"]')?.focus();
    });
  }
  function menuKeys(event) {
    if (event.key === 'Escape') { event.preventDefault(); close(); return; }
    if (!['ArrowDown','ArrowUp','Home','End'].includes(event.key)) return;
    if (event.target === input.current && event.key !== 'ArrowDown') return;
    const items = [...panel.current.querySelectorAll('[role="menuitem"]')];
    if (!items.length) return;
    const index = items.indexOf(document.activeElement);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length-1
      : event.key === 'ArrowDown' ? (index+1)%items.length : (index<0?items.length-1:(index-1+items.length)%items.length);
    event.preventDefault(); items[next].focus();
  }
  const aws=provider==='aws';
  return <div id="navigation">
    {aws&&<div className="aws-console-top-navigation"><TopNavigation visualContext="top-navigation" identity={{title:'TenkaCloud Simulator',href:'/_simulator/console/'}}
      utilities={[
        awsLanguageSettings(locale, setLocale, t),
        {type:'menu-dropdown',text:service==='iam'?t('Global','グローバル'):region,title:t('Region','リージョン'),
          items:regions.map(id=>({id,text:id,disabled:saving||service==='iam'})),onItemClick:({detail})=>{if(!saving&&service!=='iam')onRegion(detail.id);}},
        {type:'menu-dropdown',text:principal,title:t('Account','アカウント'),ariaLabel:t('Account menu','アカウントメニュー'),iconName:'user-profile',disableUtilityCollapse:true,
          description:authenticationRequired?t('Local browser session','ローカルブラウザーセッション'):t('Authentication disabled','認証無効'),
          items:[...(identitySelection?[{id:'identity',text:t('Choose identity','演習IDを選択'),disabled:saving}]:[]),
            {id:'signout',text:authenticationRequired?t('Sign out','サインアウト'):t('Sign out (authentication disabled)','サインアウト（認証無効）'),disabled:saving||!authenticationRequired}],
          onItemClick:({detail})=>{if(saving)return;if(detail.id==='identity'&&identitySelection)onIdentity();else if(detail.id==='signout'&&authenticationRequired)onSignOut();}},
        {type:'button',text:t('API reference','APIリファレンス'),href:'/_simulator/docs/'}
      ]} i18nStrings={{overflowMenuTriggerText:t('More','その他'),overflowMenuTitleText:t('Utilities','ユーティリティ'),overflowMenuBackIconAriaLabel:t('Back','戻る'),overflowMenuDismissIconAriaLabel:t('Close','閉じる')}}/></div>}
    <div className="cloud-global-nav">
    <nav className="cloud-provider-choices" aria-label={t('Cloud provider','クラウドプロバイダー')}>
      {PROVIDERS.map(item => <button type="button" key={item.id} aria-pressed={provider===item.id}
        disabled={saving || !servicesForProvider(item.id, capabilities).length}
        onClick={() => { if (provider!==item.id) choose(item.defaultService); }}>{item.label}</button>)}
    </nav>
    <div className="cloud-catalog-control">
      <button type="button" ref={trigger} aria-expanded={open} aria-controls="cloud-service-catalog"
        disabled={saving || !capabilities} onClick={() => setOpen(value => !value)}>{t('Services','サービス')}</button>
      {open && <div className="cloud-service-panel" id="cloud-service-catalog" ref={panel} onKeyDown={menuKeys}>
        <label htmlFor="cloud-service-query">{t('Search services in','サービスを検索:')} {PROVIDERS.find(item=>item.id===provider).label}</label>
        <input ref={input} id="cloud-service-query" type="search" value={query} autoComplete="off"
          onChange={event=>setQuery(event.target.value)} placeholder={t('Service name','サービス名')}/>
        <div role="menu" aria-label={t('Services','サービス')}>
          {results.map(item=><button type="button" role="menuitem" aria-label={item.label} key={item.id} disabled={saving}
            onClick={()=>choose(item.id)}>{item.label}{service===item.id&&<span className="cloud-current-service">{t('Current','選択中')}</span>}</button>)}
        </div>
        {!results.length&&<p role="status">{t('No matching implemented services.','一致する実装済みサービスはありません。')}</p>}
        <button type="button" onClick={close}>{t('Close','閉じる')}</button>
      </div>}
    </div>
    {!aws&&<label className="cloud-language">{t('Language','言語')}<select aria-label={t('Language','言語')} value={locale}
      onChange={event=>setLocale(event.target.value)}><option value="en">English</option><option value="ja">日本語</option></select></label>}
    {!aws&&<details className="cloud-session"><summary aria-label={t('Account menu','アカウントメニュー')}>{principal}</summary>
      <div><p>{authenticationRequired?t('Local browser session','ローカルブラウザーセッション'):t('Authentication disabled','認証無効')}</p>
        {identitySelection&&<button type="button" disabled={saving} onClick={event=>{event.currentTarget.closest('details').open=false;onIdentity();}}>{t('Choose identity','演習IDを選択')}</button>}
        <button type="button" disabled={saving||!authenticationRequired} onClick={event=>{event.currentTarget.closest('details').open=false;onSignOut();}}>{t('Sign out','サインアウト')}</button>
      </div></details>}
    {sessionError&&<div className="cloud-session-error" role="alert"><strong>{t('Could not sign out','サインアウトできませんでした')}</strong><p>{sessionError}</p></div>}
    {!aws&&<a className="cloud-api-reference" href="/_simulator/docs/">{t('API reference','APIリファレンス')}</a>}
  </div></div>;
}
