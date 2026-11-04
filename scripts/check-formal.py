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
# Drift must not release ownership: otherwise a later owner's stack can be
# deleted by the original stack's cleanup. The mutant reclaims absent resources.
with tempfile.TemporaryDirectory(prefix='tcsim-ownership-') as directory:
 root=Path(directory)
 model=Path('formal/StackOwnership.tla').read_text()
 cfg=Path('formal/StackOwnership.cfg').read_text()
 for name,source,success in [('StackOwnership',model,True),('StackOwnershipMutant',model.replace('MODULE StackOwnership ', 'MODULE StackOwnershipMutant ').replace('/\\ Unclaimed(p)', '/\\ p \\notin present'),False)]:
  (root/(name+'.tla')).write_text(source);(root/(name+'.cfg')).write_text(cfg)
  result=subprocess.run(['java','-cp',str(jar),'tlc2.TLC','-workers','1','-metadir',str(root/(name+'-states')),'-config',str(root/(name+'.cfg')),str(root/(name+'.tla'))],capture_output=True,text=True,timeout=60)
  text=result.stdout+result.stderr
  Path('reports/'+name+'.txt').write_text(text)
  if success:assert result.returncode==0 and 'No error has been found' in text,text
  else:assert result.returncode!=0 and 'Invariant OwnershipExclusive is violated' in text,text
  print(name+': '+('invariants hold' if success else 'drift ownership mutation detected'))
# Versioned deletion must guard retained history, not merely the current view.
# The bounded model abstracts payload/version IDs and enabled versioning only;
# native tests cover suspension, quotas, SQL rollback and concurrent writers.
with tempfile.TemporaryDirectory(prefix='tcsim-s3-versions-') as directory:
 root=Path(directory)
 model=Path('formal/S3Versions.tla').read_text()
 cfg=Path('formal/S3Versions.cfg').read_text()
 for name,source,success in [('S3Versions',model,True),('S3VersionsMutant',model.replace('MODULE S3Versions ', 'MODULE S3VersionsMutant ').replace('bucket /\\ EmptyHistory', 'bucket /\\ current = {}'),False)]:
  (root/(name+'.tla')).write_text(source);(root/(name+'.cfg')).write_text(cfg)
  result=subprocess.run(['java','-cp',str(jar),'tlc2.TLC','-workers','1','-metadir',str(root/(name+'-states')),'-config',str(root/(name+'.cfg')),str(root/(name+'.tla'))],capture_output=True,text=True,timeout=60)
  text=result.stdout+result.stderr
  Path('reports/'+name+'.txt').write_text(text)
  if success:assert result.returncode==0 and 'No error has been found' in text,text
  else:assert result.returncode!=0 and 'Invariant NoHistoryOrphans is violated' in text,text
  print(name+': '+('invariants hold' if success else 'current-only deletion mutation detected'))
# Native/legacy writes must atomically reserve a shared name, including retired
# native roles with no aggregate identity row. Abstracts two case-folded names;
# native race/fault/HTTP tests separately exercise the implementation.
with tempfile.TemporaryDirectory(prefix='tcsim-native-iam-') as directory:
 root=Path(directory)
 model=Path('formal/NativeIAMOwnership.tla').read_text()
 cfg=Path('formal/NativeIAMOwnership.cfg').read_text()
 mutant=model.replace('MODULE NativeIAMOwnership ', 'MODULE NativeIAMOwnershipMutant ').replace('LegacyPut(n) == /\\ n \\notin claimed','LegacyPut(n) == /\\ TRUE')
 assert mutant!=model and 'LegacyPut(n) == /\\ TRUE' in mutant
 for name,source,success in [('NativeIAMOwnership',model,True),('NativeIAMOwnershipMutant',mutant,False)]:
  (root/(name+'.tla')).write_text(source);(root/(name+'.cfg')).write_text(cfg)
  result=subprocess.run(['java','-cp',str(jar),'tlc2.TLC','-workers','1','-metadir',str(root/(name+'-states')),'-config',str(root/(name+'.cfg')),str(root/(name+'.tla'))],capture_output=True,text=True,timeout=60)
  text=result.stdout+result.stderr
  Path('reports/'+name+'.txt').write_text(text)
  if success:assert result.returncode==0 and 'No error has been found' in text,text
  else:assert result.returncode!=0 and 'Invariant OwnershipExclusive is violated' in text,text
  print(name+': '+('invariants hold' if success else 'native role adoption mutation detected'))
# One API, two deployment IDs, two handler configurations and one stage. A
# mutable route edit/deletion must not change an existing deployment snapshot.
# Concrete SQLite, native HTTP, Python and two-team checks remain independent.
with tempfile.TemporaryDirectory(prefix='tcsim-rest-snapshots-') as directory:
 root=Path(directory)
 model=Path('formal/RESTDeploymentSnapshot.tla').read_text()
 cfg=Path('formal/RESTDeploymentSnapshot.cfg').read_text()
 original='/\\ UNCHANGED <<exists, snapshots, baseline, stage>>'
 replacement="/\\ snapshots' = [d \\in Deployments |-> IF snapshots[d] = None THEN None ELSE v] /\\ UNCHANGED <<exists, baseline, stage>>"
 assert model.count(original)==1
 mutant=model.replace('MODULE RESTDeploymentSnapshot ', 'MODULE RESTDeploymentSnapshotMutant ').replace(original,replacement)
 for name,source,success in [('RESTDeploymentSnapshot',model,True),('RESTDeploymentSnapshotMutant',mutant,False)]:
  (root/(name+'.tla')).write_text(source);(root/(name+'.cfg')).write_text(cfg)
  result=subprocess.run(['java','-cp',str(jar),'tlc2.TLC','-workers','1','-metadir',str(root/(name+'-states')),'-config',str(root/(name+'.cfg')),str(root/(name+'.tla'))],capture_output=True,text=True,timeout=60)
  text=result.stdout+result.stderr
  Path('reports/'+name+'.txt').write_text(text)
  if success:assert result.returncode==0 and 'No error has been found' in text,text
  else:assert result.returncode!=0 and 'Invariant SnapshotsFrozen is violated' in text,text
  print(name+': '+('invariants hold' if success else 'mutable deployment snapshot mutation detected'))
