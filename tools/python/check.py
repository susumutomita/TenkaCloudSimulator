"""Verify immutable embedded runtime, stdlib and original notice inventory."""
import gzip, hashlib, json, zipfile
from pathlib import Path
root=Path(__file__).resolve().parents[2];metadata=json.loads((root/'third_party/python.json').read_text());assets=root/'internal/workload/python/assets'
assert metadata['python']=='3.12.16' and metadata['sqlite']=='3.54.1'
for name,digest in metadata['artifacts'].items():assert hashlib.sha256((assets/name).read_bytes()).hexdigest()==digest,name
wasm=gzip.decompress((assets/'python.wasm.gz').read_bytes());assert wasm[:8]==b'\0asm\x01\0\0\0';assert hashlib.sha256(wasm).hexdigest()==metadata['rawWasmSha256']
with zipfile.ZipFile(assets/'stdlib.zip') as z:
 assert set(z.namelist())==set(metadata['stdlib'])|{'lib-dynload/'}
 for name,digest in metadata['stdlib'].items():assert hashlib.sha256(z.read(name)).hexdigest()==digest,name
notices=(root/'third_party/PYTHON-NOTICES.txt').read_bytes();assert notices==(root/'internal/legal/assets/PYTHON-NOTICES.txt').read_bytes();assert hashlib.sha256(notices).hexdigest()==metadata['noticeSha256']
for item in metadata['notices']:
 marker=b'\n===== '+item['origin'].encode()+b'/'+item['path'].encode()+b' =====\n';assert notices.count(marker)==1,item
 data=notices.split(marker,1)[1].split(b'\n===== ',1)[0];assert data.endswith(b'\n');assert hashlib.sha256(data[:-1]).hexdigest()==item['sha256'],item
assert {'cpython','wasi-libc','llvm','wasi-sdk','sqlite'}=={item['origin'] for item in metadata['notices']}
print('Pinned CPython/WASI,',len(metadata['stdlib']),'stdlib sources and original runtime notices verified.')
