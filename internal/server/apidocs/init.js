"use strict";
// Keep one Scalar instance. Every locale update supplies the full safe config;
// the pinned updateConfiguration API replaces the entire configuration object.
(() => {
  const locale=SimulatorReferenceLocale;
  const message=document.getElementById('reference-locale-status');
  const customFetch=SimulatorS3ResponseGuide.wrapFetch(async(input,init)=>{
    const url=new URL(typeof input==='string'?input:input instanceof URL?input.href:input.url,location.href);
    if(url.origin!==location.origin)return Promise.reject(new Error('Only this simulator origin is allowed.'));
    // Document/schema/capability and credential-control reads never consume
    // explicit user activity. Native Scalar executions share the runner hook.
    if(!url.pathname.startsWith('/_simulator/')||url.pathname.startsWith('/_simulator/providers/')){
      const activity=globalThis.SimulatorDocsSessionActivity;
      if(!activity)throw Error('Session activity is not ready; request was not sent.');
      try{await activity.beforeExplicitSend();}catch(error){message.textContent=activity.message(error.code,locale.get());throw error;}
    }
    return fetch(input,init);
  });
  let activeLocale=locale.get();
  const reference=Scalar.createApiReference('#reference',SimulatorReferenceConfig.create(null,customFetch,activeLocale));
  locale.subscribe(next=>{
    const japanese=next==='ja';
    document.documentElement.lang=next;
    document.title=japanese?'TenkaCloud Simulator · API リファレンス':'TenkaCloud Simulator · API reference';
    document.querySelector('meta[name="description"]').content=japanese?'TenkaCloud Simulator の対応操作向けの対話型 API リファレンス。':'Interactive API reference for supported TenkaCloud Simulator operations.';
    message.textContent=japanese?'操作 UI には独自の日本語訳を使用します。OpenAPI の説明文、API 識別子、リクエスト・応答データは原文のままです。':'First-party Japanese overrides translate UI controls. OpenAPI descriptions, API identifiers and request/response data retain their original language.';
    if(next===activeLocale)return;
    try{
      const previous=reference.getConfiguration();
      reference.updateConfiguration(SimulatorReferenceConfig.create(previous,customFetch,next));
      activeLocale=next;
    }catch{
      message.textContent=japanese?'リファレンスの言語更新に失敗しました。ネイティブリクエスト実行の経路は変更していません。':'Reference language update failed. Native request execution routing is unchanged.';
    }
  });
})();
