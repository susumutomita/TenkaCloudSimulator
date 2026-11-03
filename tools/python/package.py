"""Package locally built, pinned CPython/WASI and retain original rights bytes."""
import argparse, gzip, hashlib, io, json, re, subprocess, zipfile
from pathlib import Path

parser=argparse.ArgumentParser()
for name in ('source','wasm','sqlite','libc','llvm','sdk'):parser.add_argument('--'+name,required=True,type=Path)
a=parser.parse_args();root=Path(__file__).resolve().parents[2]
pins={'source':'2c2462afc5d978d31f3d073001fc202d269917ff','sqlite':'57520227c95396ce39ba6ecaac8d475b78dd3964','libc':'2e6fb9d8ee0cdf9e431fbcabe8af3115de000a13','sdk':'5a0bf653a1a06e1c18867567c5937006d3394a69'}
for key,commit in pins.items():
 assert subprocess.check_output(['git','-C',str(getattr(a,key)),'rev-parse','HEAD'],text=True).strip()==commit,key
 subprocess.run(['git','-C',str(getattr(a,key)),'diff','--exit-code','HEAD','--'],check=True,stdout=subprocess.DEVNULL)
configuration=(a.source/'builddir/wasi/pyconfig.h').read_text()
assert '#define HAVE_CLOCK_NANOSLEEP 1' not in configuration
assert '#define HAVE_NANOSLEEP 1' in configuration

assets=root/'internal/workload/python/assets';assets.mkdir(parents=True,exist_ok=True)
wasm=a.wasm.read_bytes();assert wasm[:8]==b'\0asm\x01\0\0\0' and len(wasm)<32<<20
(assets/'python.wasm.gz').write_bytes(gzip.compress(wasm,compresslevel=9,mtime=0))
stdlib={}
with zipfile.ZipFile(assets/'stdlib.zip','w',compression=zipfile.ZIP_DEFLATED,compresslevel=9) as z:
 for file in sorted((a.source/'Lib').rglob('*.py')):
  path=file.relative_to(a.source/'Lib')
  if any(p in {'test','tests','__pycache__','idlelib','tkinter','turtledemo','ensurepip','venv','lib2to3'} for p in path.parts):continue
  assert not file.is_symlink();data=file.read_bytes();info=zipfile.ZipInfo(path.as_posix(),(1980,1,1,0,0,0));info.compress_type=zipfile.ZIP_DEFLATED;info.external_attr=0o100444<<16;z.writestr(info,data);stdlib[path.as_posix()]=hashlib.sha256(data).hexdigest()
 info=zipfile.ZipInfo('lib-dynload/',(1980,1,1,0,0,0));info.external_attr=(0o40555<<16)|0x10;z.writestr(info,b'')

rights=[];inventory=[]
def retain(origin,path,data):
 assert data;rights.append(b'\n===== '+origin.encode()+b'/'+path.encode()+b' =====\n'+data+b'\n');inventory.append({'origin':origin,'path':path,'sha256':hashlib.sha256(data).hexdigest()})
for path in ('LICENSE','Doc/license.rst'):retain('cpython',path,(a.source/path).read_bytes())
for origin,directory in [('wasi-sdk',a.sdk),('wasi-libc',a.libc),('llvm',a.llvm)]:
 for file in sorted(directory.rglob('*')):
  if '.git' in file.parts or not file.is_file() or not re.search(r'license|copying|copyright',file.name,re.I):continue
  retain(origin,file.relative_to(directory).as_posix(),file.read_bytes())
# Retain original comment blocks, including HACL/Expat/mpdecimal/CC0 notices.
# This source acknowledgement inventory is deliberately broader than linked code.
for origin,directory in [('cpython',a.source),('wasi-libc',a.libc),('llvm',a.llvm)]:
 names=[f.relative_to(directory).as_posix() for f in directory.rglob('*') if f.is_file()] if origin=='llvm' else subprocess.check_output(['git','-C',str(directory),'ls-files','-z']).decode().split('\0')
 for name in sorted(n for n in names if n.endswith(('.c','.h','.s','.S','.cpp','.inc','.inl','.py'))):
  data=(directory/name).read_bytes()
  pattern=rb'/\*.*?\*/|(?:^[ \t]*//[^\n]*(?:\n|$))+' if not name.endswith('.py') else rb'(?:^[ \t]*\#[^\n]*(?:\n|$))+'
  blocks=[m.group() for m in re.finditer(pattern,data,re.S|re.M) if re.search(rb'copyright|permission is hereby granted|public domain|MIT License|Redistribution and use|SPDX-License-Identifier|Licensed under',m.group(),re.I)]
  if blocks:retain(origin,name,b'\n\n'.join(blocks))
data=(a.sqlite/'sqlite3.c').read_bytes();retain('sqlite','sqlite3.c original header',re.search(rb'/\*.*?\*/',data,re.S).group())
notice=b'CPython 3.12.16 / SQLite 3.54.1 / WASI runtime original notices.\nSimulator build: static WASI, selected stdlib, relative nanosleep fallback, debug information stripped; separate invocation glue. Upstream interpreter sources are unmodified.\nSource comments are overinclusive acknowledgements, not a claim that every source module is linked.\n'+b''.join(rights)
for path in ('third_party/PYTHON-NOTICES.txt','internal/legal/assets/PYTHON-NOTICES.txt'):(root/path).write_bytes(notice)
metadata={'python':'3.12.16','sqlite':'3.54.1','wasiSdk':'34.0','llvm':'895aa2c896ada719451be2e3673c83da8ddf1141','sourceCommits':pins,'rawWasmSha256':hashlib.sha256(wasm).hexdigest(),'sourceDateEpoch':1792952677,'configureOverrides':{'ac_cv_func_clock_nanosleep':'no','ac_cv_lib_rt_clock_nanosleep':'no'},'artifacts':{p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(assets.iterdir())},'stdlib':stdlib,'notices':inventory,'noticeSha256':hashlib.sha256(notice).hexdigest()}
(root/'third_party/python.json').write_text(json.dumps(metadata,indent=2,sort_keys=True)+'\n')
print('Packaged actual CPython/WASI,',len(stdlib),'stdlib files,',len(inventory),'original notice blocks')
