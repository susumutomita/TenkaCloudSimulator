"""Verify vendored bundle identity and local API-reference wiring without npm."""
import hashlib,json
from pathlib import Path
metadata=json.loads(Path('third_party/scalar.json').read_text())
assert hashlib.sha256(Path('internal/server/apidocs/scalar.js').read_bytes()).hexdigest()==metadata['bundleSha256']
for name,digest in metadata['assets'].items():
 assert hashlib.sha256(Path('internal/server/apidocs',name).read_bytes()).hexdigest()==digest
graph=json.loads(Path('tools/scalar/bundle-inputs.json').read_text())
assert not any('@replit/codemirror-css-color-picker' in name for name in graph['inputs'])
assert any(name.endswith('no-color-picker.js') for name in graph['inputs'])
inventory=json.loads(Path('third_party/scalar-bundled-packages.json').read_text())
assert not any(item['name']=='@replit/codemirror-css-color-picker' for item in inventory)
assert all(item.get('noticeEvidence') for item in inventory)
spec=json.loads(Path('internal/server/apidocs/openapi.json').read_text())
assert spec['openapi']=='3.1.0' and spec['servers']==[{'url':'/','description':'This simulator only'}]
assert len([v for p in spec['paths'].values() for k,v in p.items() if k in ['get','put','head','delete']])==14
init=Path('internal/server/apidocs/init.js').read_text()
for option in ['withDefaultFonts: false','telemetry: false','agent: { disabled: true }','proxyUrl: ""','url.origin !== location.origin']:
 assert option in init,option
assert 'jitless: true' in Path('internal/server/apidocs/boot.js').read_text()
assert 'https://' not in Path('internal/server/apidocs/index.html').read_text()
for path in ['third_party/SCALAR-LICENSE','third_party/SCALAR-BUNDLED-NOTICES.txt','third_party/GO-NOTICES.txt']:
 assert Path(path).stat().st_size>500,path
for path in ['SCALAR-LICENSE','SCALAR-BUNDLED-NOTICES.txt','GO-NOTICES.txt']:
 assert Path('third_party',path).read_bytes()==Path('internal/legal/assets',path).read_bytes()
print('Vendored Scalar identity, notices, and local OpenAPI configuration verified.')
