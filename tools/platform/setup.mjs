// Development-only resolution for immutable external platform source. Never overwrite modules.
import {symlink,lstat,mkdir,realpath} from 'node:fs/promises';
import {resolve,dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
const root=dirname(fileURLToPath(import.meta.url)),platform=resolve(process.argv[2]);
async function link(source,target){try{await lstat(target);if(await realpath(target)!==await realpath(source))throw Error('Refusing to replace '+target)}catch(error){if(error.code==='ENOENT')await symlink(source,target,'dir');else throw error}}
await mkdir(resolve(root,'node_modules/@tenkacloud'),{recursive:true});
for(const name of ['problem-sdk','problem-runtime','portal-contracts','coordination-plugin-sdk'])await link(resolve(platform,'packages',name),resolve(root,'node_modules/@tenkacloud',name));
await link(resolve(root,'node_modules'),resolve(platform,'node_modules'));
console.log('Platform source resolves locked development tools; no application source changed.');
