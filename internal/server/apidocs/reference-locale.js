/* First-party presentation preference only. Never store request or auth data. */
(() => {
  'use strict';
  const KEY='tenkacloud-simulator-locale',EVENT='tenkacloud-simulator-locale-change';
  const valid=value=>value==='en'||value==='ja';
  let current='en',store=null;try{store=localStorage;current=store.getItem(KEY)==='ja'?'ja':'en';}catch{}
  const listeners=new Set();
  function apply(locale){if(!valid(locale)||locale===current)return;current=locale;for(const listener of listeners)listener(current);}
  window.addEventListener(EVENT,event=>{try{const detail=event.detail;if(!detail||typeof detail!=='object'||Object.getPrototypeOf(detail)!==Object.prototype||Reflect.ownKeys(detail).length!==1||!Object.hasOwn(detail,'locale'))return;const locale=detail.locale;if(valid(locale))apply(locale);}catch{/* Malformed presentation events are ignored. */}});
  window.addEventListener('storage',event=>{if(event.key!==KEY&&event.key!==null)return;if(event.key===null&&event.newValue!==null)return;if(event.newValue!==null&&!valid(event.newValue))return;try{const actual=localStorage;if(event.storageArea!==actual)return;const value=actual.getItem(KEY);if(value===null)apply('en');else if(valid(value))apply(value);}catch{/* Blocked storage cannot grant a new presentation value. */}});
  globalThis.SimulatorReferenceLocale=Object.freeze({key:KEY,event:EVENT,get:()=>current,
    set(locale){if(!valid(locale))return false;try{store?.setItem(KEY,locale);}catch{}apply(locale);window.dispatchEvent(new CustomEvent(EVENT,{detail:{locale}}));return true;},
    subscribe(listener){listeners.add(listener);listener(current);return()=>listeners.delete(listener);}});
})();
