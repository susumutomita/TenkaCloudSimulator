import React, {useEffect, useRef, useState} from 'react';
import TopNavigation from '@cloudscape-design/components/top-navigation';
import {useLocale} from './i18n.jsx';
import {PROVIDERS, providerForService, servicesForProvider} from './console-navigation.mjs';
import {SERVICE_PRESENTATION, ServiceIcon} from './console-home.jsx';

const categories=[
  ['compute','Compute','コンピューティング'],['storage','Storage','ストレージ'],
  ['database','Database','データベース'],['networking','Networking','ネットワーキング'],
  ['integration','Application integration','アプリケーション統合'],
  ['security','Security, identity, and compliance','セキュリティ、ID、およびコンプライアンス'],
  ['management','Management and governance','管理とガバナンス'],
];

// Neutral simulator controls. Provider shells own their project/account chrome.
export default function GlobalNavigation({service, capabilities, saving, onNavigate,
  principal, authenticationRequired, identitySelection, onIdentity, onSignOut,
  region, regions, onRegion, sessionError}) {
  const {locale, setLocale, t} = useLocale();
  const provider = providerForService(service) ?? 'aws', aws=provider==='aws';
  const [open, setOpen] = useState(false), [query, setQuery] = useState('');
  const [category,setCategory]=useState('all');
  const [compact,setCompact]=useState(()=>window.matchMedia('(max-width: 1350px)').matches);
  const [position,setPosition]=useState({top:60,left:16});
  const trigger = useRef(null), panel = useRef(null), input = useRef(null);
  const allServices=servicesForProvider(provider,capabilities);
  const results=servicesForProvider(provider,capabilities,query).filter(item=>!aws||query||category==='all'||SERVICE_PRESENTATION[item.id]?.category===category);
  const providerLabel=PROVIDERS.find(item=>item.id===provider).label;
  const searchLabel=t('Search services in','サービスを検索:')+' '+providerLabel;
  function close() { setOpen(false); requestAnimationFrame(() => trigger.current?.focus()); }
  function show() {
    const rect=trigger.current?.getBoundingClientRect();
    if(rect)setPosition({top:rect.bottom+8,left:Math.max(12,Math.min(rect.left,window.innerWidth-720-12))});
    setOpen(true);
  }
  useEffect(()=>{
    const media=window.matchMedia('(max-width: 1350px)');
    function resize(){setCompact(media.matches);setOpen(false);}
    window.addEventListener('resize',resize);
    return()=>window.removeEventListener('resize',resize);
  },[]);
  useEffect(() => {
    if (!open) return;
    input.current?.focus();
    function outside(event) {
      if (!panel.current?.contains(event.target) && !trigger.current?.contains(event.target) && event.target!==input.current) setOpen(false);
    }
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [open]);
  useEffect(() => { setOpen(false); setQuery('');setCategory('all'); }, [provider, service, principal]);
  function choose(id, focusProvider = false) {
    if (saving) return;
    const nextProvider = providerForService(id);
    onNavigate(id); setOpen(false); setQuery('');
    requestAnimationFrame(() => {
      if (!focusProvider && nextProvider === provider) trigger.current?.focus();
      else document.querySelector('.cloud-provider-choices button[aria-pressed="true"]')?.focus();
    });
  }
  function menuKeys(event) {
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); return; }
    if (!open||!['ArrowDown','ArrowUp','Home','End'].includes(event.key)) return;
    if (event.target === input.current && event.key !== 'ArrowDown') return;
    const items = [...panel.current.querySelectorAll('[role="menuitem"]:not(:disabled)')];
    if (!items.length) return;
    const index = items.indexOf(document.activeElement);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length-1
      : event.key === 'ArrowDown' ? (index+1)%items.length : (index<0?items.length-1:(index-1+items.length)%items.length);
    // The non-AWS search input is inside this panel. Consume the handled key
    // so its input handler cannot advance focus again when it bubbles here.
    event.preventDefault(); event.stopPropagation(); items[next].focus();
  }
  const serviceButton=<button className={aws?'aws-services-trigger':undefined} type="button" ref={trigger} aria-label={t('Services','サービス')} title={t('Services','サービス')}
    aria-expanded={open} aria-controls="cloud-service-catalog" disabled={saving || !capabilities}
    onClick={()=>open?close():show()}>{aws?<span className="aws-services-grid" aria-hidden="true">{Array.from({length:9},(_,index)=><i key={index}/>)}</span>:t('Services','サービス')}</button>;
  const searchInput=<input ref={input} id="cloud-service-query" aria-label={searchLabel} type="search" value={query} autoComplete="off"
    onKeyDown={menuKeys} onChange={event=>{setQuery(event.target.value);setCategory('all');if(!open)show();}} placeholder={t('Service name','サービス名')}/>;
  const awsSearch=<div className="aws-global-search">{serviceButton}<div className="aws-global-search-field"><svg aria-hidden="true" viewBox="0 0 24 24"><circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/></svg>{searchInput}</div></div>;
  return <div id="navigation" onKeyDown={event=>{if(open&&event.key==='Escape')menuKeys(event);}}>
    {aws&&<div className="aws-console-top-navigation"><TopNavigation visualContext="top-navigation" identity={{title:'TenkaCloud Simulator',href:'#/home',onFollow:event=>{event.preventDefault();choose('home');}}}
      search={!compact?awsSearch:undefined}
      utilities={[
        {type:'menu-dropdown',text:service==='iam'?t('Global','グローバル'):region,title:t('Region','リージョン'),
          items:regions.map(id=>({id,text:id,disabled:saving||service==='iam'})),onItemClick:({detail})=>{if(!saving&&service!=='iam')onRegion(detail.id);}},
        {type:'menu-dropdown',text:principal,title:t('Account','アカウント'),ariaLabel:t('Account menu','アカウントメニュー'),iconName:'user-profile',disableUtilityCollapse:true,
          description:authenticationRequired?t('Local browser session','ローカルブラウザーセッション'):t('Authentication disabled','認証無効'),
          items:[...(identitySelection?[{id:'identity',text:t('Choose identity','演習IDを選択'),disabled:saving}]:[]),
            {id:'signout',text:authenticationRequired?t('Sign out','サインアウト'):t('Sign out (authentication disabled)','サインアウト（認証無効）'),disabled:saving||!authenticationRequired}],
          onItemClick:({detail})=>{if(saving)return;if(detail.id==='identity'&&identitySelection)onIdentity();else if(detail.id==='signout'&&authenticationRequired)onSignOut();}},
        {type:'button',text:t('API reference','APIリファレンス'),href:'/_simulator/docs/'}
      ]} i18nStrings={{searchIconAriaLabel:searchLabel,searchDismissIconAriaLabel:t('Close search','検索を閉じる'),overflowMenuTriggerText:t('More','その他'),overflowMenuTitleText:t('Utilities','ユーティリティ'),overflowMenuBackIconAriaLabel:t('Back','戻る'),overflowMenuDismissIconAriaLabel:t('Close','閉じる')}}/></div>}
    {aws&&compact&&<div className="aws-mobile-search-row">{awsSearch}</div>}
    <div className={aws?'aws-secondary-bar':undefined}>
      {aws&&<nav className="aws-service-shortcuts" aria-label={t('Service shortcuts','サービスのショートカット')}>{allServices.map(item=><button type="button" key={item.id} disabled={saving} onClick={()=>choose(item.id)} aria-current={service===item.id?'page':undefined}><ServiceIcon id={item.id}/>{item.label}</button>)}</nav>}
      <div className={'cloud-global-nav'+(aws?' cloud-global-nav--aws':'')}>
      <nav className="cloud-provider-choices" aria-label={t('Cloud provider','クラウドプロバイダー')}>
        {PROVIDERS.map(item => <button type="button" key={item.id} aria-pressed={provider===item.id}
          disabled={saving || !servicesForProvider(item.id, capabilities).length}
          onClick={() => { if (provider!==item.id || item.id==='aws') choose(item.defaultService, true); }}>{item.label}</button>)}
      </nav>
      {!aws&&<div className="cloud-catalog-control">{serviceButton}</div>}
      <label className="cloud-language">{t('Language','言語')}<select aria-label={t('Language','言語')} value={locale}
        onChange={event=>setLocale(event.target.value)}><option value="en">English</option><option value="ja">日本語</option></select></label>
      {!aws&&<details className="cloud-session"><summary aria-label={t('Account menu','アカウントメニュー')}>{principal}</summary>
        <div><p>{authenticationRequired?t('Local browser session','ローカルブラウザーセッション'):t('Authentication disabled','認証無効')}</p>
          {identitySelection&&<button type="button" disabled={saving} onClick={event=>{event.currentTarget.closest('details').open=false;onIdentity();}}>{t('Choose identity','演習IDを選択')}</button>}
          <button type="button" disabled={saving||!authenticationRequired} onClick={event=>{event.currentTarget.closest('details').open=false;onSignOut();}}>{t('Sign out','サインアウト')}</button>
        </div></details>}
      {!aws&&<a className="cloud-api-reference" href="/_simulator/docs/">{t('API reference','APIリファレンス')}</a>}
      </div>
    </div>
    {sessionError&&<div className="cloud-session-error" role="alert"><strong>{t('Could not sign out','サインアウトできませんでした')}</strong><p>{sessionError}</p></div>}
    {open&&<div className={'cloud-service-panel'+(aws?' cloud-service-panel--aws':'')} id="cloud-service-catalog" ref={panel} onKeyDown={menuKeys} style={{top:position.top,left:position.left,maxHeight:`calc(100dvh - ${position.top+12}px)`}}>
      {aws?<>
        <nav className="aws-service-categories" aria-label={t('Service categories','サービスカテゴリ')}><button type="button" aria-current={category==='all'?'true':undefined} onClick={()=>{setCategory('all');setQuery('');}}>{t('All services','すべてのサービス')}</button><div className="aws-service-category-divider"/>{categories.filter(([id])=>allServices.some(item=>SERVICE_PRESENTATION[item.id]?.category===id)).map(([id,en,ja])=><button type="button" key={id} aria-current={category===id?'true':undefined} onClick={()=>{setCategory(id);setQuery('');}}>{t(en,ja)}</button>)}</nav>
        <div className="aws-service-results"><div className="aws-service-panel-heading"><h2>{query?t('Search results','検索結果'):category==='all'?t('All services','すべてのサービス'):t(...categories.find(item=>item[0]===category).slice(1))}</h2><button className="aws-service-close" type="button" onClick={close} aria-label={t('Close','閉じる')}>×</button></div>
          <div role="menu" aria-label={t('Services','サービス')}>
            {!query&&category==='all'&&<button type="button" role="menuitem" className="aws-service-entry" onClick={()=>choose('home')} disabled={saving}><strong>{t('Console Home','コンソールのホーム')}</strong><small>{t('Service shortcuts and local environment information','サービスへのショートカットとローカル環境の情報')}</small></button>}
            {results.map(item=><button type="button" role="menuitem" className="aws-service-entry" aria-label={item.label} key={item.id} disabled={saving} onClick={()=>choose(item.id)}><strong>{item.label}</strong><small>{t(SERVICE_PRESENTATION[item.id]?.en??item.label,SERVICE_PRESENTATION[item.id]?.ja??item.label)}</small>{service===item.id&&<span className="cloud-current-service">{t('Current','選択中')}</span>}</button>)}
          </div>{!results.length&&<p role="status">{t('No matching implemented services.','一致する実装済みサービスはありません。')}</p>}
        </div>
      </>:<><label htmlFor="cloud-service-query">{searchLabel}</label>{searchInput}<div role="menu" aria-label={t('Services','サービス')}>{results.map(item=><button type="button" role="menuitem" aria-label={item.label} key={item.id} disabled={saving} onClick={()=>choose(item.id)}>{item.label}{service===item.id&&<span className="cloud-current-service">{t('Current','選択中')}</span>}</button>)}</div>{!results.length&&<p role="status">{t('No matching implemented services.','一致する実装済みサービスはありません。')}</p>}<button type="button" onClick={close}>{t('Close','閉じる')}</button></>}
    </div>}
  </div>;
}
