import {build} from 'esbuild';
import {readFile,writeFile,mkdir,readdir} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.dirname(fileURLToPath(import.meta.url));
const output=path.resolve(root,'../../internal/server/web');
// Cloudscape 3.0.1408 emits aria-orientation on a <nav>; ARIA navigation
// landmarks do not support it. Remove only that invalid attribute at build
// time, retaining the official component, controls and original license.
const navigationAriaFix={name:'valid-navigation-aria',setup(b){b.onLoad({filter:/app-layout\/visual-refresh\/mobile-toolbar\.js$/},async args=>{
 const source=await readFile(args.path,'utf8');const invalid='"aria-orientation": "horizontal", ';
 if(source.split(invalid).length!==2)throw Error('Review Cloudscape navigation ARIA workaround against the new version');
 return {contents:source.replace(invalid,''),loader:'js'};
})}};
const result=await build({plugins:[navigationAriaFix],absWorkingDir:root,entryPoints:['src/main.jsx'],outfile:path.join(output,'app.js'),bundle:true,minify:true,format:'iife',platform:'browser',target:['es2022'],metafile:true,legalComments:'external',define:{'process.env.NODE_ENV':'"production"'},loader:{'.woff':'dataurl','.woff2':'dataurl','.ttf':'dataurl','.svg':'dataurl','.png':'dataurl'}});
const inputs=Object.keys(result.metafile.inputs);
for(const required of ['node_modules/@cloudscape-design/components/app-layout/','node_modules/@cloudscape-design/components/table/','node_modules/@cloudscape-design/global-styles/'])if(!inputs.some(p=>p.includes(required)))throw Error('Missing actual Cloudscape input: '+required);
await writeFile(path.join(root,'bundle-inputs.json'),JSON.stringify(result.metafile,null,2)+'\n');
const packages=new Map();
for(const input of inputs){if(!input.includes('node_modules/'))continue;let dir=path.dirname(path.join(root,input));while(dir!==path.dirname(dir)){try{const pkg=JSON.parse(await readFile(path.join(dir,'package.json'),'utf8'));if(pkg.name){packages.set(pkg.name,{dir,pkg});break}}catch(e){if(e.code!=='ENOENT')throw e;}dir=path.dirname(dir);}}
const inventory=[],notices=['Cloudscape console: original package licenses and notices for actual bundle inputs.\nBuild modification: @cloudscape-design/components 3.0.1408 app-layout/visual-refresh/mobile-toolbar.js has the invalid aria-orientation attribute removed from its nav landmark. See tools/console/build.mjs. Original component and license notices are retained.\n'];
for(const [name,{dir,pkg}] of [...packages].sort()){
 const files=(await readdir(dir)).filter(n=>/license|licence|notice|copying/i.test(n));if(!files.length)throw Error('No original notice for '+name);
 const evidence=[];notices.push('\n'+'='.repeat(70)+'\n'+name+'@'+pkg.version+'\nLicense: '+pkg.license+'\n');
 for(const file of files){const data=await readFile(path.join(dir,file),'utf8');notices.push('\n--- '+file+' ---\n'+data+'\n');evidence.push({file,sha256:createHash('sha256').update(data).digest('hex')});}
 inventory.push({name,version:pkg.version,license:pkg.license,evidence});
}
const text=notices.join('');for(const relative of ['third_party/CONSOLE-NOTICES.txt','internal/legal/assets/CONSOLE-NOTICES.txt'])await writeFile(path.resolve(root,'../..',relative),text);
await writeFile(path.resolve(root,'../../third_party/console-packages.json'),JSON.stringify(inventory,null,2)+'\n');
const assets={};for(const file of ['app.js','app.css','app.js.LEGAL.txt','app.css.LEGAL.txt']){try{assets[file]=createHash('sha256').update(await readFile(path.join(output,file))).digest('hex')}catch(e){if(e.code!=='ENOENT')throw e;}}
await writeFile(path.join(root,'bundle-digests.json'),JSON.stringify(assets,null,2)+'\n');
console.log('Verified real Cloudscape component inputs and original notices:',inventory.length,'packages',assets);
