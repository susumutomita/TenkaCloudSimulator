// First-party browser UX classifier, not authorization or proof of human presence.
// The server's cookie/idle/hard/parent checks are the authority. No timer or storage.
export class SessionRenewalError extends Error {
  constructor(code) { super('Browser session renewal failed'); this.name='SessionRenewalError'; this.code=code; }
}
export function sessionRenewalMessage(code, locale='en') {
  const ja=locale==='ja';
  if(code==='session_required') return ja?'セッションが終了しました。入力内容を確認して再ログインしてください。':'Your session ended. Review your unsaved work and sign in again.';
  return ja?'セッションを更新できませんでした。操作は再送信していません。':'Could not renew your session. Your action was not replayed.';
}
export function createSessionActivity({document:doc=globalThis.document, fetch:send=globalThis.fetch, now=()=>globalThis.performance.now()}={}) {
  let intent=null, pending=null, disposed=false;
  const eligible=()=>!disposed&&doc?.visibilityState==='visible'&&doc.hasFocus();
  function note(event) {
    if(event?.isTrusted!==true||!['pointerdown','keydown','input'].includes(event.type)||!eligible()) return;
    const at=now(); if(Number.isFinite(at)) intent=at;
  }
  function clear() { intent=null; }
  function visibility() { if(!eligible()) clear(); }
  const view=doc?.defaultView;
  for(const type of ['pointerdown','keydown','input']) doc?.addEventListener(type,note,true);
  doc?.addEventListener('visibilitychange',visibility,true);
  view?.addEventListener('blur',clear,true);
  async function renew() {
    let response;
    try {
      response=await send('/_simulator/session/renew',{method:'POST',credentials:'same-origin',redirect:'error',cache:'no-store',headers:{'X-Simulator-Session-Action':'renew'}});
    } catch { throw new SessionRenewalError('renewal_unavailable'); }
    let body;
    try { body=await response.json(); } catch { throw new SessionRenewalError('renewal_unavailable'); }
    if(response.status===200) {
      const idle=typeof body?.expiresAt==='string'?Date.parse(body.expiresAt):NaN;
      const hard=typeof body?.hardExpiresAt==='string'?Date.parse(body.hardExpiresAt):NaN;
      if(!Number.isFinite(idle)||!Number.isFinite(hard)||idle>hard) throw new SessionRenewalError('renewal_unavailable');
      return {status:'renewed'};
    }
    if(response.status===403&&body?.code==='participant_fixed_lifetime') return {status:'participant'};
    throw new SessionRenewalError(response.status===401?'session_required':'renewal_unavailable');
  }
  // No API callback is accepted; callers send their original request at most once
  // only after this succeeds. Client activity remains a UX hint, not authority.
  async function beforeExplicitAction(explicit=false) {
      if(explicit!==true||!eligible()) return {status:'not-requested'};
      if(pending) return pending;
      const at=now(), previous=intent; intent=null;
      if(previous===null||!Number.isFinite(at)||at<previous||at-previous>10000) return {status:'not-requested'};
      pending=renew();
      try { return await pending; } finally { pending=null; }
  }
  return {
    beforeExplicitAction,
    // Common authenticated API callers may consume one recent genuine gesture,
    // even if that API request is a refresh. Passive polling alone never renews.
    // Do not call on login/logout/renew or credential-control requests.
    beforeUserActiveRequest:()=>beforeExplicitAction(true),
    dispose() {
      disposed=true; clear();
      for(const type of ['pointerdown','keydown','input']) doc?.removeEventListener(type,note,true);
      doc?.removeEventListener('visibilitychange',visibility,true);
      view?.removeEventListener('blur',clear,true);
    }
  };
}
