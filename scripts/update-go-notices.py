"""Regenerate notices for runtime and optional guest TCP imports without npm."""
import os, subprocess
from pathlib import Path

go = os.environ.get('GO', 'go')

def notices(target, title):
    modules = subprocess.check_output([go, 'list', '-buildvcs=false', '-deps', '-f', '{{with .Module}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}', target], text=True)
    texts = [title + '\n']
    for line in sorted(set(modules.splitlines())):
        if not line:
            continue
        name, version, directory = line.split('|')
        if not version:
            continue
        files = sorted(f for f in Path(directory).iterdir() if f.is_file() and f.name.lower().startswith(('license', 'notice', 'copying')))
        assert files, f'Missing dependency notice: {name}'
        texts.append('\n' + '=' * 70 + '\n' + name + ' ' + version + '\n')
        for path in files:
            texts.append('\n--- ' + path.name + ' ---\n' + path.read_text() + '\n')
    goroot = Path(subprocess.check_output([go, 'env', 'GOROOT'], text=True).strip())
    texts += ['\nGo standard library, BSD-3-Clause:\n', (goroot / 'LICENSE').read_text()]
    if target == './cmd/simulator':
        texts += ['\nSQLite is in the public domain. https://sqlite.org/copyright.html\nSQLite 3.50.4 is embedded by github.com/ncruces/go-sqlite3/embed v0.30.0.\n']
    return '\n'.join(line.rstrip() for line in ''.join(texts).splitlines()).rstrip() + '\n'

for filename, target, title in [
    ('GO-NOTICES.txt', './cmd/simulator', 'Go runtime dependency license notices. Only modules reachable from cmd/simulator are included.'),
    ('GUESTTCP-NOTICES.txt', './internal/workload/guesttcp', 'Optional private TCP adapter dependency notices. This adapter is not enabled in the default runtime.'),
]:
    content = notices(target, title)
    for directory in ['third_party', 'internal/legal/assets']:
        Path(directory, filename).write_text(content)
print('Runtime and optional private TCP dependency notices regenerated.')
