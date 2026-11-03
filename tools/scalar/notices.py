"""Inventory only esbuild inputs; collect upstream notices without fabricating text.
Run after build.mjs. Missing notices fail the rebuild rather than becoming a waiver.
"""
import hashlib,io,json,tarfile,urllib.request
from pathlib import Path
root=Path(__file__).resolve().parent
repo=root.parent.parent
meta=json.loads((root/'bundle-inputs.json').read_text())
lock=json.loads((root/'package-lock.json').read_text())['packages']
packages={}
for input_name in meta['inputs']:
    path=root/input_name
    if 'node_modules/' not in input_name:
        continue
    parent=path.parent
    while parent!=parent.parent:
        manifest=parent/'package.json'
        if manifest.exists():
            info=json.loads(manifest.read_text())
            if info.get('name'):
                packages[(info['name'],info['version'])]=(parent,info)
                break
        parent=parent.parent
assert not any(name=='@replit/codemirror-css-color-picker' for name,version in packages)
texts=['Notices for the locally rebuilt Scalar API reference.\nThe Replit CSS color picker is excluded from all build inputs.\nScalar workspace packages share SCALAR-LICENSE.\n']
inventory=[]
for (name,version),(directory,info) in sorted(packages.items()):
    files=sorted(p for p in directory.iterdir() if p.is_file() and p.name.lower().startswith(('license','licence','notice','copying')))
    evidence=[]
    for path in files:
        evidence.append((path.name,path.read_text(errors='strict')))
    if not evidence and name.startswith('@scalar/'):
        evidence=[('Scalar monorepo LICENSE',(repo/'third_party/SCALAR-LICENSE').read_text())]
    if not evidence:
        # Both exact published versions have a public upstream license at gitHead.
        origins={'@headlessui/vue':('tailwindlabs/headlessui','LICENSE'),'@ai-sdk/provider-utils':('vercel/ai','LICENSE')}
        assert name in origins,f'No upstream notice located for {name}@{version}'
        registry=json.loads(urllib.request.urlopen('https://registry.npmjs.org/'+name+'/'+version).read())
        pinned_refs={'@ai-sdk/provider-utils':'6e14ffaa7e66594b926891583a4dab61910743d0'}
        commit=registry.get('gitHead') or pinned_refs.get(name);assert commit,f'No immutable upstream ref for {name}'
        if name=='@ai-sdk/provider-utils':
            source=json.loads(urllib.request.urlopen('https://raw.githubusercontent.com/vercel/ai/'+commit+'/packages/provider-utils/package.json').read())
            assert source['version']==version,'Upstream license reference is not the exact version'
        project,filename=origins[name]
        url='https://raw.githubusercontent.com/'+project+'/'+commit+'/'+filename
        evidence=[(url,urllib.request.urlopen(url).read().decode())]
    license_name=info.get('license')
    assert license_name in ['MIT','Apache-2.0','ISC','BSD-2-Clause','BSD-3-Clause'],(name,license_name)
    texts.append('\n'+'='*70+'\n'+name+'@'+version+'\nLicense: '+license_name+'\n')
    notices=[]
    for label,content in evidence:
        texts.append('\n--- '+label+' ---\n'+content+'\n')
        notices.append({'source':label,'sha256':hashlib.sha256(content.encode()).hexdigest()})
    relative=str(directory.relative_to(root))
    package_lock=lock.get(relative,{})
    inventory.append({'name':name,'version':version,'license':license_name,'integrity':package_lock.get('integrity'),'noticeEvidence':notices})
# Precompiled Tailwind CSS is distributed with Scalar, but is not a JS module input.
version='4.3.3'
metadata=json.loads(urllib.request.urlopen('https://registry.npmjs.org/tailwindcss/'+version).read())
archive=urllib.request.urlopen(metadata['dist']['tarball']).read()
import base64
algorithm,expected=metadata['dist']['integrity'].split('-',1)
assert base64.b64encode(hashlib.new(algorithm,archive).digest()).decode()==expected
with tarfile.open(fileobj=io.BytesIO(archive),mode='r:gz') as tar:
    tailwind=tar.extractfile('package/LICENSE').read().decode()
texts.append('\nTailwind CSS '+version+' (precompiled stylesheet)\n'+tailwind+'\n')
inventory.append({'name':'tailwindcss','version':version,'license':metadata['license'],'integrity':metadata['dist']['integrity'],'noticeEvidence':[{'source':'npm tarball package/LICENSE','sha256':hashlib.sha256(tailwind.encode()).hexdigest()}],'usage':'precompiled stylesheet in @scalar/api-reference'})
content='\n'.join(line.rstrip() for line in ''.join(texts).splitlines()).rstrip()+'\n'
for path in ['third_party/SCALAR-BUNDLED-NOTICES.txt','internal/legal/assets/SCALAR-BUNDLED-NOTICES.txt']:
    (repo/path).write_text(content)
(repo/'third_party/scalar-bundled-packages.json').write_text(json.dumps(inventory,indent=2)+'\n')
print(f'Collected original notices for {len(inventory)} included packages; no Replit color-picker code is shipped.')
