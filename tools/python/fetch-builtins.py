"""Fetch/verify only WASM compiler-rt builtin sources for original notices."""
import argparse, concurrent.futures, hashlib, json, time, urllib.request
from pathlib import Path
parser=argparse.ArgumentParser();parser.add_argument('inventory',type=Path);parser.add_argument('destination',type=Path);a=parser.parse_args();items=json.loads(a.inventory.read_text())
prefix='https://raw.githubusercontent.com/llvm/llvm-project/895aa2c896ada719451be2e3673c83da8ddf1141/compiler-rt/lib/builtins/'
def fetch(item):
 name=Path(item['path']).name;assert item['path']=='compiler-rt/lib/builtins/'+name;assert item['url']==prefix+name
 target=a.destination/item['path'];target.parent.mkdir(parents=True,exist_ok=True)
 if target.exists():
  cached=target.read_bytes()
  if hashlib.sha1(b'blob '+str(len(cached)).encode()+b'\0'+cached).hexdigest()==item['sha']:return
 for attempt in range(3):
  try:
   with urllib.request.urlopen(item['url'],timeout=30) as response:data=response.read(1<<20)
   break
  except OSError:
   if attempt==2:raise
   time.sleep(attempt+1)
 assert hashlib.sha1(b'blob '+str(len(data)).encode()+b'\0'+data).hexdigest()==item['sha'],name
 target.write_bytes(data)
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:list(pool.map(fetch,items))
print('Verified compiler-rt builtin original sources:',len(items))
