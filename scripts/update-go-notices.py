"""Regenerate notices for actual runtime imports, without adding npm tooling."""
import os,subprocess
from pathlib import Path
go=os.environ.get('GO','go')
modules=subprocess.check_output([go,'list','-deps','-f','{{with .Module}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}','./cmd/simulator'],text=True)
texts=['Go runtime dependency license notices. Only modules reachable from cmd/simulator are included.\n']
for line in sorted(set(modules.splitlines())):
 if not line:continue
 name,version,directory=line.split('|')
 if not version:continue
 files=sorted(f for f in Path(directory).iterdir() if f.is_file() and f.name.lower().startswith(('license','notice','copying')))
 assert files,f'Missing runtime dependency notice: {name}'
 texts.append('\n'+'='*70+'\n'+name+' '+version+'\n')
 for path in files:texts.append('\n--- '+path.name+' ---\n'+path.read_text()+'\n')
goroot=Path(subprocess.check_output([go,'env','GOROOT'],text=True).strip())
texts+=['\nGo standard library, BSD-3-Clause:\n',(goroot/'LICENSE').read_text(),'\nSQLite is in the public domain. https://sqlite.org/copyright.html\nSQLite 3.50.4 is embedded by github.com/ncruces/go-sqlite3/embed v0.30.0.\n']
content='\n'.join(line.rstrip() for line in ''.join(texts).splitlines()).rstrip()+'\n'
for path in ['third_party/GO-NOTICES.txt','internal/legal/assets/GO-NOTICES.txt']:Path(path).write_text(content)
print('Runtime Go notices regenerated.')
