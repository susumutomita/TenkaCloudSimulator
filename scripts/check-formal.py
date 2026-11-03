"""Finite TLA+ model check and a deletion-guard mutation to test the invariant.
Requires Java and a separately downloaded TLC 1.7.4 jar; no runtime dependency.
"""
import hashlib,os,subprocess,tempfile
from pathlib import Path
jar=Path(os.environ['TLA_JAR']).resolve()
expected=Path('formal/tla2tools.sha256').read_text().split()[0]
assert hashlib.sha256(jar.read_bytes()).hexdigest()==expected,'TLC jar checksum mismatch'
with tempfile.TemporaryDirectory(prefix='tcsim-tlc-') as directory:
 root=Path(directory)
 model=Path('formal/ObjectStore.tla').read_text()
 cfg=Path('formal/ObjectStore.cfg').read_text()
 for name,source,success in [('ObjectStore',model,True),('ObjectStoreMutant',model.replace('MODULE ObjectStore ','MODULE ObjectStoreMutant ').replace('ObjectsFor(b) = {}','TRUE'),False)]:
  (root/(name+'.tla')).write_text(source);(root/(name+'.cfg')).write_text(cfg)
  result=subprocess.run(['java','-cp',str(jar),'tlc2.TLC','-workers','1','-metadir',str(root/(name+'-states')),'-config',str(root/(name+'.cfg')),str(root/(name+'.tla'))],capture_output=True,text=True,timeout=60)
  text=result.stdout+result.stderr
  Path('reports').mkdir(exist_ok=True);Path('reports/'+('formal-model-check.txt' if success else 'formal-mutation-check.txt')).write_text(text)
  if success:assert result.returncode==0 and 'No error has been found' in text,text
  else:assert result.returncode!=0 and 'Invariant NoOrphans is violated' in text,text
  print(name+': '+('invariants hold' if success else 'missing deletion guard detected'))
