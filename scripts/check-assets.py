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
operations=[v['operationId'] for p in spec['paths'].values() for k,v in p.items() if k in ['get','post','put','head','delete']]
expected_legacy={'Capabilities','CreateBucket','CreateFunction','DeleteBucket','DeleteFunction','DeleteIdentityPolicy','DeleteObject','GetFunctionConfiguration','GetIdentityPolicy','GetObject','HeadBucket','HeadObject','IAMRoleQueryOperation','Invoke','ListBuckets','ListFunctions','ListObjectsV2OrGetBucketLocation','ListPrincipals','PutIdentityPolicy','PutObject','UpdateFunctionCode','awsRegionalOperation'}
expected_rest={'CreateRestApi','GetRestApis','GetRestApi','DeleteRestApi','GetResources','CreateResource','GetResource','DeleteResource','PutMethod','GetMethod','PutIntegration','CreateDeployment','GetDeployment','CreateStage','GetStage','DeleteStage','InvokeLocalRESTProxyGet','InvokeLocalRESTProxyPost','AddPermission','GetPolicy','RemovePermission'}
expected_multicloud={'GCPDownloadObject', 'AzureGetContainerProperties', 'GCPUploadObject', 'AzureDeleteContainer', 'AzureDeleteBlob', 'GCPListObjects', 'GCPDeleteBucket', 'GCPGetBucket', 'GCPGetObject', 'AzureCreateContainer', 'AzureListBlobs', 'GCPCreateBucket', 'AzureGetBlobProperties', 'GCPDeleteObject', 'AzureListContainers', 'GCPListBuckets', 'AzurePutBlob', 'AzureGetBlob'}
assert len(operations)==len(set(operations)) and set(operations)==expected_legacy|expected_rest|expected_multicloud
lambda_operations={v['operationId'] for path,p in spec['paths'].items() if path.startswith('/2015-03-31/functions') for k,v in p.items() if k in ['get','post','put','delete']}
assert lambda_operations=={'CreateFunction','ListFunctions','GetFunctionConfiguration','UpdateFunctionCode','Invoke','DeleteFunction','AddPermission','GetPolicy','RemovePermission'}
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

console=json.loads(Path('tools/console/bundle-digests.json').read_text())
for name,digest in console.items():
 assert hashlib.sha256(Path('internal/server/web',name).read_bytes()).hexdigest()==digest,name
inputs=json.loads(Path('tools/console/bundle-inputs.json').read_text())['inputs']
for component in ['app-layout','table','tabs','global-styles']:
 assert any('@cloudscape-design/' in name and component in name for name in inputs),component
packages=json.loads(Path('third_party/console-packages.json').read_text())
assert all(p.get('evidence') for p in packages)
assert Path('third_party/CONSOLE-NOTICES.txt').read_bytes()==Path('internal/legal/assets/CONSOLE-NOTICES.txt').read_bytes()
assert spec['paths']['/']['post']['operationId']=='awsRegionalOperation'
print('Cloudscape bundle, original notices and regional API description verified.')

import runpy
runpy.run_path('tools/python/check.py',run_name='__main__')

# Retain exact historical rights and require reviewed current source identities.
# Pending replacement review is a hard failure, never an adapted-comment check.
native_provenance=runpy.run_path('scripts/check-native-provenance.py')
assert native_provenance['main']()==0, 'Native provenance gate did not pass.'
