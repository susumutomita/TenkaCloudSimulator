"""Export the official golang/vulndb machine-readable OSV snapshot to the public
Go database v1 API. This is a clearly identified fallback, not a claim that the
canonical HTTP service was scanned. No internal YAML format is parsed.
Usage: python scripts/build-vulndb-snapshot.py REPOSITORY OUTPUT_DIRECTORY
"""
import hashlib,json,shutil,subprocess,sys
from datetime import datetime,timezone
from pathlib import Path
source=Path(sys.argv[1]).resolve();output=Path(sys.argv[2]).resolve()
assert not output.exists(),'Choose a fresh output directory'
(output/'index').mkdir(parents=True);(output/'ID').mkdir()
commit=subprocess.check_output(['git','-C',str(source),'rev-parse','HEAD'],text=True).strip()
snapshot_time=datetime.fromisoformat(subprocess.check_output(['git','-C',str(source),'show','-s','--format=%cI','HEAD'],text=True).strip()).astimezone(timezone.utc).isoformat().replace('+00:00','Z')
remote=subprocess.check_output(['git','-C',str(source),'remote','get-url','origin'],text=True).strip()
assert remote=='https://github.com/golang/vulndb.git','Expected the official Go team mirror'
modules={};vulns=[];files=[];modified=''
for path in sorted((source/'data/osv').glob('GO-*.json')):
 data=path.read_bytes();entry=json.loads(data);id=entry['id'];assert id==path.stem
 modified=max(modified,entry['modified'])
 shutil.copyfile(path,output/'ID'/path.name)
 files.append({'id':id,'sha256':hashlib.sha256(data).hexdigest()})
 vulns.append({'id':id,'modified':entry['modified'],'aliases':entry.get('aliases',[])})
 for module in sorted({a['package']['name'] for a in entry.get('affected',[]) if a.get('package',{}).get('ecosystem')=='Go'}):
  # Fixed is optional in the official v1 index API. Omit it conservatively:
  # govulncheck reads the complete unchanged OSV ranges instead of prefiltering.
  modules.setdefault(module,[]).append({'id':id,'modified':entry['modified']})
assert len(files)>1000,'Unexpectedly incomplete official snapshot'
for name,value in [('db',{'modified':snapshot_time}),('modules',[{'path':p,'vulns':v} for p,v in sorted(modules.items())]),('vulns',vulns)]:
 (output/'index'/(name+'.json')).write_text(json.dumps(value,separators=(',',':'))+'\n')
manifest={'source':remote,'commit':commit,'snapshotTime':snapshot_time,'sourceMaximumModified':modified,'sourceDateCaveat':'Repository OSV files contain placeholder dates. They are preserved unchanged; index/db.json uses checkout commit time. Range and symbol scanning uses the raw OSV data; canonical publication metadata is not claimed.','entries':len(files),'modules':len(modules),'sourceFormat':'unchanged data/osv/*.json; no internal YAML parsing','canonicalService':'https://vuln.go.dev (HTTP access blocked; this is a Git snapshot fallback)','idFiles':files}
(output/'snapshot.json').write_text(json.dumps(manifest,indent=2)+'\n')
print(json.dumps({k:v for k,v in manifest.items() if k!='idFiles'},indent=2))
