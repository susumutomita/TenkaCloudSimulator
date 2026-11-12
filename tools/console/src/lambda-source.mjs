// Source is kept only for functions created by this mounted console. Never
// infer a downloaded package or overwrite files from an unknown ZIP.
export const blueprint = 'import json\n\ndef lambda_handler(event, context):\n    return {"statusCode": 200, "body": json.dumps({"message": "Hello from this local function", "event": event})}\n';
export const base64 = bytes => {let text='';for(let i=0;i<bytes.length;i+=32768)text+=String.fromCharCode(...bytes.subarray(i,i+32768));return btoa(text)};
export function sourceZip(source) {
 if(typeof source!=='string'||!source.trim())throw Error('Enter Python source.');
 const name=new TextEncoder().encode('lambda_function.py'),data=new TextEncoder().encode(source);
 if(data.length>262144-(30+46+22+2*name.length))throw Error('The function ZIP exceeds 256 KiB.');
 let crc=0xffffffff;
 for(const byte of data){crc^=byte;for(let bit=0;bit<8;bit++)crc=(crc>>>1)^((crc&1)?0xedb88320:0)}crc=(crc^0xffffffff)>>>0;
 const offset=30+name.length+data.length,length=46+name.length,bytes=new Uint8Array(offset+length+22),view=new DataView(bytes.buffer);
 const u16=(at,value)=>view.setUint16(at,value,true),u32=(at,value)=>view.setUint32(at,value,true);
 u32(0,0x04034b50);u16(4,20);u32(14,crc);u32(18,data.length);u32(22,data.length);u16(26,name.length);bytes.set(name,30);bytes.set(data,30+name.length);
 u32(offset,0x02014b50);u16(offset+4,20);u16(offset+6,20);u32(offset+16,crc);u32(offset+20,data.length);u32(offset+24,data.length);u16(offset+28,name.length);bytes.set(name,offset+46);
 u32(offset+length,0x06054b50);u16(offset+length+8,1);u16(offset+length+10,1);u32(offset+length+12,length);u32(offset+length+16,offset);return base64(bytes);
}
export class SourceDrafts {
 #entries=new Map();
 remember(configuration,source){
  if(configuration?.Handler!=='lambda_function.lambda_handler'||!configuration.RevisionId||!configuration.FunctionName)throw Error('The function response did not identify the deployed source.');
  this.#entries.set(configuration.FunctionName,{source,deployed:source,revision:configuration.RevisionId});
 }
 get(configuration){
  const entry=this.#entries.get(configuration?.FunctionName);
  if(!entry||entry.revision!==configuration.RevisionId||configuration.Handler!=='lambda_function.lambda_handler')return null;
  return {...entry};
 }
 edit(configuration,source){const entry=this.get(configuration);if(!entry)throw Error('Reloaded or externally changed code cannot be edited inline.');this.#entries.set(configuration.FunctionName,{...entry,source});}
 recover(name){const entry=this.#entries.get(name);return entry?{...entry}:null;}
 forget(name){this.#entries.delete(name);}
 dirty(){return [...this.#entries.values()].some(entry=>entry.source!==entry.deployed);}
}
