"""Download the pinned development-only axe-core tool with npm integrity checks.
No npm/Node dependency is installed in the application and no tool is embedded.
"""
import base64,hashlib,io,json,sys,tarfile,urllib.request
from pathlib import Path
version='4.11.0'
metadata=json.loads(urllib.request.urlopen('https://registry.npmjs.org/axe-core/'+version,timeout=30).read())
data=urllib.request.urlopen(metadata['dist']['tarball'],timeout=30).read()
pinned_integrity='sha512-ilYanEU8vxxBexpJd8cWM4ElSQq4QctCLKih0TSfjIfCQTeyH/6zVrmIJfLPrKTKJRbiG+cfnZbQIjAlJmF1jQ=='
assert metadata['dist']['integrity']==pinned_integrity,'Unexpected upstream axe-core integrity'
algorithm,expected=pinned_integrity.split('-',1)
assert base64.b64encode(hashlib.new(algorithm,data).digest()).decode()==expected,'axe-core integrity mismatch'
output=Path(sys.argv[1]);output.mkdir(parents=True,exist_ok=True)
with tarfile.open(fileobj=io.BytesIO(data),mode='r:gz') as tar:
 for name in ['axe.min.js','axe.js','LICENSE','LICENSE-3RD-PARTY.txt']:
  (output/name).write_bytes(tar.extractfile('package/'+name).read())
(output/'provenance.json').write_text(json.dumps({'name':'axe-core','version':version,'license':metadata['license'],'source':metadata['dist']['tarball'],'integrity':metadata['dist']['integrity'],'bundleSha256':hashlib.sha256((output/'axe.min.js').read_bytes()).hexdigest(),'upstreamSource':'https://github.com/dequelabs/axe-core/tree/v4.11.0'},indent=2)+'\n')
print('Development-only axe-core '+version+' downloaded with original MPL-2.0 and third-party notices to '+str(output))
