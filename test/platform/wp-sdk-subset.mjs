// Test-only projection of the pinned existing problem. Production catalog stays
// disabled until its complete template, HTTP/WAF and original grading work.
import {createRequire} from 'node:module';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import path from 'node:path';
const require=createRequire(path.resolve('tools/platform/package.json'));
const {parseDocument}=require('yaml');
const document=parseDocument(readFileSync(process.argv[2],'utf8'));
if(document.errors.length)throw document.errors[0];
const location=['Resources','AppFunction','Properties','Code','ZipFile'];
const source=document.getIn(location);
if(typeof source!=='string')throw Error('Missing original inline handler');
const resources=document.get('Resources',true);
resources.items=resources.items.filter(pair=>['AWS::S3::Bucket','AWS::SSM::Parameter','AWS::Lambda::Function'].includes(pair.value.get('Type'))||pair.key.value==='AppFunctionRole');
document.setIn(['Resources','AppFunction','Properties','Environment','Variables','API_BASE_URL'],'http://api-unavailable.invalid/prod');
document.delete('Outputs');
const template=document.toString();
if(parseDocument(template).getIn(location)!==source)throw Error('Serialized original handler changed');
process.stdout.write(JSON.stringify({template,handlerSha256:createHash('sha256').update(source).digest('hex')}));
