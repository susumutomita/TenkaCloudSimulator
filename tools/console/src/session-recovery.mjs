// One-way RAM-only barrier. A new login/context can never unlock this page.
export function sessionRecoveryMessage(locale='en'){
  return locale==='ja'?'このページはロックされています。残っている入力を確認してください。操作は再送信していません。':'This page is locked. Review remaining input. Your action was not replayed.';
}
export class SessionLockedError extends Error {
  constructor(locale='en'){super(sessionRecoveryMessage(locale));this.name='SessionLockedError';}
}
export function createSessionRecovery({locale=()=> 'en'}={}){
  let locked=false, generation=0;
  const error=()=>new SessionLockedError(locale());
  const check=ticket=>{if(locked||ticket!==generation)throw error();};
  return {
    isLocked:()=>locked,
    error,
    lock(){if(!locked){locked=true;generation++;}return generation;},
    async run(action){const ticket=generation;check(ticket);const assertCurrent=()=>check(ticket);const result=await action(assertCurrent);assertCurrent();return result;},
    guardResponse(response,assertCurrent){
      const guard=next=>this.guardResponse(next,assertCurrent);
      const stream=body=>body&&new Proxy(body,{get(target,key){
        if(key==='getReader')return (...args)=>{assertCurrent();const reader=target.getReader(...args);return new Proxy(reader,{get(owner,method){
          if(method==='read')return async(...values)=>{assertCurrent();const result=await owner.read(...values);assertCurrent();return result;};
          const value=Reflect.get(owner,method,owner);return typeof value==='function'?value.bind(owner):value;
        }});};
        const value=Reflect.get(target,key,target);return typeof value==='function'?value.bind(target):value;
      }});
      // A response may arrive before lock while its body finishes afterwards.
      return new Proxy(response,{get(target,key){
        if(key==='body'){assertCurrent();return stream(target.body);}
        if(['json','text','blob','arrayBuffer','formData','bytes'].includes(key)&&typeof target[key]==='function')return async(...args)=>{assertCurrent();const value=await target[key](...args);assertCurrent();return value;};
        if(key==='clone')return ()=>{assertCurrent();return guard(target.clone());};
        const value=Reflect.get(target,key,target);return typeof value==='function'?value.bind(target):value;
      }});
    }
  };
}
