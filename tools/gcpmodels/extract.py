"""Extract documentation-free subsets of official Google API discovery documents.

Development-only. The simulator binary never imports Python. Discovery
documents come from the pinned google-api-python-client package (Apache-2.0).
For every selected API the output keeps the reviewed methods, the HTTP
binding of every method (so unreviewed methods are refused explicitly) and the
schemas reachable from the reviewed methods. Field descriptions are reduced to
`required` (official "Required." marker) and `readOnly` flags.

Usage: python tools/gcpmodels/extract.py  (selection: internal/gcp/catalog/selection.json)
"""
import hashlib
import json
import sys
from pathlib import Path

import importlib.metadata

import googleapiclient

ROOT = Path(__file__).resolve().parents[2]
SELECTION = ROOT / 'internal/gcp/catalog/selection.json'
OUTPUT = ROOT / 'internal/gcp/catalog/models'
PINNED = '2.199.0'
DOCUMENTS = Path(googleapiclient.__file__).parent / 'discovery_cache' / 'documents'


def flags(prop):
    text = (prop.get('description') or '').lstrip()
    out = {k: prop[k] for k in ('type', 'format', 'enum', '$ref', 'pattern') if k in prop}
    if prop.get('readOnly') or text.startswith('Output only'):
        out['readOnly'] = True
    if text.startswith('Required') or '(Required)' in text[:40]:
        out['required'] = True
    if 'items' in prop:
        out['items'] = flags(prop['items'])
    if 'additionalProperties' in prop:
        out['additionalProperties'] = flags(prop['additionalProperties'])
    if 'properties' in prop:
        out['properties'] = {k: flags(v) for k, v in prop['properties'].items()}
    return out


def methods(resources, prefix=''):
    for name, resource in resources.items():
        for mname, method in resource.get('methods', {}).items():
            yield f'{prefix}{name}.{mname}', method
        yield from methods(resource.get('resources', {}), f'{prefix}{name}.')


def refs(schema, out):
    if isinstance(schema, dict):
        if '$ref' in schema:
            out.add(schema['$ref'])
        for value in schema.values():
            refs(value, out)


def extract(key, wanted):
    path = DOCUMENTS / f'{key}.json'
    raw = path.read_bytes()
    doc = json.loads(raw)
    every = dict(methods(doc['resources']))
    missing = sorted(set(wanted) - set(every))
    if missing:
        raise SystemExit(f'{key}: unknown official methods {missing}')
    selected, roots = {}, set()
    for name in sorted(wanted):
        method = every[name]
        entry = {k: method[k] for k in ('httpMethod', 'path', 'flatPath', 'parameterOrder') if k in method}
        entry['parameters'] = {p: {k: v for k, v in spec.items() if k in ('type', 'location', 'required', 'repeated', 'format', 'enum', 'pattern')}
                               for p, spec in method.get('parameters', {}).items()}
        for part in ('request', 'response'):
            if part in method:
                entry[part] = method[part]['$ref']
                roots.add(method[part]['$ref'])
        selected[name] = entry
    schemas, stack = {}, list(roots)
    while stack:
        name = stack.pop()
        if name in schemas:
            continue
        schemas[name] = flags(doc['schemas'][name])
        found = set()
        refs(schemas[name], found)
        stack.extend(found - set(schemas))
    routes = {name: {'httpMethod': m['httpMethod'], 'flatPath': m.get('flatPath', m['path'])} for name, m in sorted(every.items())}
    return {'source': {'package': 'google-api-python-client', 'version': importlib.metadata.version('google-api-python-client'),
                       'file': f'googleapiclient/discovery_cache/documents/{key}.json',
                       'sha256': hashlib.sha256(raw).hexdigest(), 'license': 'Apache-2.0', 'revision': doc.get('revision', '')},
            'name': doc['name'], 'version': doc['version'], 'title': doc.get('title', ''), 'rootUrl': doc['rootUrl'],
            'servicePath': doc.get('servicePath', ''), 'routes': routes, 'methods': selected, 'schemas': dict(sorted(schemas.items()))}


def main():
    version = importlib.metadata.version('google-api-python-client')
    if version != PINNED:
        raise SystemExit(f'google-api-python-client {PINNED} is required; found {version}')
    selection = json.loads(SELECTION.read_text())
    OUTPUT.mkdir(parents=True, exist_ok=True)
    wanted = set()
    for key, names in sorted(selection.items()):
        target = OUTPUT / f'{key}.json'
        target.write_text(json.dumps(extract(key, names), sort_keys=True, separators=(',', ':')) + '\n')
        wanted.add(target.name)
    for stale in OUTPUT.glob('*.json'):
        if stale.name not in wanted:
            stale.unlink()
    print(f'Extracted {len(selection)} official discovery subsets into {OUTPUT.relative_to(ROOT)}')


if __name__ == '__main__':
    sys.exit(main())
