// Inspect YAML syntax only. Intrinsics and template code are never evaluated.
import {parseDocument,isMap} from 'yaml';
import {readFileSync} from 'node:fs';
const result={};
for(const file of process.argv.slice(2)){
 const document=parseDocument(readFileSync(file,'utf8'));
 if(document.errors.length)throw Error(document.errors[0].message);
 const resources=document.get('Resources',true);
 if(!isMap(resources))throw Error('Expected a Resources mapping: '+file);
 const types=[];
 for(const entry of resources.items){
  if(!isMap(entry.value))throw Error('Expected a resource definition');
  const type=entry.value.get('Type');
  if(typeof type!=='string'||!/^(?:AWS::[A-Za-z0-9]+::[A-Za-z0-9]+|Custom::[A-Za-z0-9]+)$/.test(type))throw Error('Expected a literal AWS resource type');
  types.push(type);
 }
 result[file]=[...new Set(types)].sort();
}
console.log(JSON.stringify(result));
