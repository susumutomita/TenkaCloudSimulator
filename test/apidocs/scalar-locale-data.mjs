// First-party lexical data reader, never evaluates/imports the vendor bundle.
import {createHash} from 'node:crypto';
export const ASSET='09949908a1eaed291abd64c8f490c2a7c5d218a458a77d394f065b644301cb5f';
export const digest=data=>createHash('sha256').update(data).digest('hex');
export function literalObject(source,aliases={}){
 let i=0;
 const space=()=>{while(/\s/.test(source[i]||'')&&i<source.length)i++;};
 function quoted(){const quote=source[i++];let out='';while(i<source.length){const c=source[i++];if(c===quote)return out;if(c!=='\\'){out+=c;continue;}const e=source[i++],simple={n:'\n',r:'\r',t:'\t',b:'\b',f:'\f',v:'\v','0':'\0','"':'"',"'":"'",'\\':'\\','/':'/'};if(Object.hasOwn(simple,e))out+=simple[e];else if(e==='u'||e==='x'){const size=e==='u'?4:2,hex=source.slice(i,i+size);if(!new RegExp('^[0-9a-fA-F]{'+size+'}$').test(hex))throw Error('escape');out+=String.fromCharCode(parseInt(hex,16));i+=size;}else throw Error('unsupported escape');}throw Error('unclosed string');}
 function word(){const m=/^[A-Za-z_$][A-Za-z0-9_$]*/.exec(source.slice(i));if(!m)throw Error('identifier');i+=m[0].length;return m[0];}
 function object(){space();if(source[i++]!=='{')throw Error('object');const out={};space();while(source[i]!=='}'){const key=['"',"'"].includes(source[i])?quoted():word();space();if(source[i++]!==':'||Object.hasOwn(out,key))throw Error('key');space();let value;if(source[i]==='{')value=object();else if(['"',"'"].includes(source[i]))value=quoted();else{const alias=Object.keys(aliases).find(a=>source.startsWith(a,i));if(!alias)throw Error('expression forbidden');i+=alias.length;value=aliases[alias];}out[key]=value;space();if(source[i]!==',')break;i++;space();}if(source[i++]!=='}')throw Error('object end');return out;}
 const result=object();space();if(i!==source.length)throw Error('trailing expression');return result;
}
function flatten(value,prefix='',out={}){for(const [key,entry]of Object.entries(value)){const path=prefix?prefix+'.'+key:key;if(typeof entry==='string')out[path]=entry;else flatten(entry,path,out);}return out;}
export function pinnedEnglish(bytes){
 if(digest(bytes)!==ASSET)throw Error('unreviewed Scalar asset');
 const ranges={blocks:[269422,271279,'ae8edb75976700c99279acd551c882db2f474578a96607c53195b26ae375ef38'],reference:[1851208,1859686,'9d9ed5db5da36a0aa78fb400c63d0010bb2e304677ebfa9e5e66df0d22624d57'],client:[2684177,2694088,'0ac2cc6aa0cf809259873ab7b9c21347e91cf71fe17a70086abc1cc06d699970']};
 const text=name=>{const [a,b,hash]=ranges[name],data=bytes.subarray(a,b);if(digest(data)!==hash)throw Error('locale literal changed');return data.toString('utf8');};
 const blocks=literalObject(text('blocks')),reference=literalObject(text('reference'),{'Bs.en.schema':blocks.schema}),client=literalObject(text('client'));
 const result=flatten(reference);for(const [key,value]of Object.entries(flatten(blocks)))if(result[key]!==value)throw Error('schema alias mismatch');
 return {...result,...flatten(client,'apiClient')};
}
