"""Extract documentation-free subsets of official Azure Resource Manager specs.

Development-only. The simulator binary never imports Python. Contracts come
from a pinned commit of github.com/Azure/azure-rest-api-specs (MIT). Treat the
checkout as untrusted data: run this script with `python -I`, pass the
checkout path as an argument and never run tools from inside the checkout.

For every selected resource provider namespace and api-version the output keeps:

* the HTTP route of every operation in every swagger file of that api-version,
  so unreviewed operations are refused explicitly (HTTP 501);
* the reviewed operations with resolved parameters, response schemas and the
  x-ms-long-running-operation / x-ms-pageable traits;
* the definitions reachable from reviewed operations (including discriminator
  subtypes) with only the validation traits the engine uses;
* the source commit, every file path and its SHA-256.

Usage:
  python -I tools/azuremodels/extract.py /path/to/azure-rest-api-specs
(selection: internal/azure/arm/selection.json)
"""
import hashlib
import json
import posixpath
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SELECTION = ROOT / 'internal/azure/arm/selection.json'
OUTPUT = ROOT / 'internal/azure/arm/models'
METHODS = ('get', 'put', 'post', 'patch', 'delete', 'head')
SCHEMA_KEYS = ('type', 'format', 'pattern', 'minLength', 'maxLength', 'minimum', 'maximum',
               'exclusiveMinimum', 'exclusiveMaximum', 'minItems', 'maxItems', 'uniqueItems',
               'readOnly', 'discriminator', 'x-ms-discriminator-value', 'x-ms-secret',
               'x-ms-mutability', 'x-nullable', 'x-ms-azure-resource')
PARAMETER_KEYS = ('name', 'in', 'required', 'type', 'format', 'enum', 'pattern', 'minLength',
                  'maxLength', 'minimum', 'maximum', 'x-ms-skip-url-encoding')


def head_commit(checkout):
    """Read HEAD without executing git inside an untrusted checkout."""
    git = checkout / '.git'
    head = (git / 'HEAD').read_text().strip()
    if not head.startswith('ref: '):
        return head
    ref = head[5:]
    loose = git / ref
    if loose.exists():
        return loose.read_text().strip()
    for line in (git / 'packed-refs').read_text().splitlines():
        if line.endswith(' ' + ref):
            return line.split(' ', 1)[0]
    raise SystemExit('cannot resolve the checkout HEAD commit')


class Specs:
    def __init__(self, checkout):
        self.checkout = checkout
        self.documents = {}
        self.hashes = {}

    def load(self, relative):
        relative = posixpath.normpath(relative)
        if relative.startswith('..') or relative.startswith('/'):
            raise SystemExit(f'reference escapes the specification checkout: {relative}')
        if relative not in self.documents:
            raw = (self.checkout / relative).read_bytes()
            self.hashes[relative] = hashlib.sha256(raw).hexdigest()
            self.documents[relative] = json.loads(raw)
        return self.documents[relative]

    def pointer(self, base, ref):
        """Resolve a JSON reference relative to the file `base`."""
        target, _, fragment = ref.partition('#')
        file = posixpath.normpath(posixpath.join(posixpath.dirname(base), target)) if target else base
        node = self.load(file)
        for part in [p for p in fragment.split('/') if p]:
            node = node[part.replace('~1', '/').replace('~0', '~')]
        return file, fragment, node


class Extractor:
    def __init__(self, specs, files):
        self.specs = specs
        self.files = files
        self.reachable = {}  # (file, name) -> raw schema
        self.queue = []

    def definition(self, file, ref):
        """Return the qualified key for a definition reference and queue it."""
        target, fragment, node = self.specs.pointer(file, ref)
        if not fragment.startswith('/definitions/') or fragment.count('/') != 2:
            raise SystemExit(f'{file}: unsupported reference {ref}')
        key = (target, fragment.split('/')[2])
        if key not in self.reachable:
            self.reachable[key] = node
            self.queue.append(key)
        return key

    def parameter(self, file, raw):
        if '$ref' in raw:
            file, _, raw = self.specs.pointer(file, raw['$ref'])
        out = {k: raw[k] for k in PARAMETER_KEYS if k in raw}
        if 'items' in raw:
            out['items'] = {k: raw['items'][k] for k in ('type', 'format', 'enum') if k in raw['items']}
        if raw.get('in') == 'body':
            schema = raw['schema']
            if '$ref' not in schema:
                raise SystemExit(f'{file}: inline body schemas are not supported')
            out['schema'] = self.definition(file, schema['$ref'])
        return out

    def operations(self):
        """Yield (file, path, method, operation, path-level parameters) of every operation."""
        for file in self.files:
            document = self.specs.load(file)
            for section in ('paths', 'x-ms-paths'):
                for path, item in sorted(document.get(section, {}).items()):
                    for method in METHODS:
                        if method in item:
                            yield file, path.split('?', 1)[0], method, item[method], item.get('parameters', [])

    def extract(self, wanted):
        routes, selected = {}, {}
        for file, path, method, op, shared in self.operations():
            identifier = op['operationId']
            if identifier in routes:
                raise SystemExit(f'duplicate operationId {identifier}')
            parameters = [self.parameter(file, p) if identifier in wanted else self.route_parameter(file, p)
                          for p in shared + op.get('parameters', [])]
            multi = sorted(p['name'] for p in parameters if p.get('in') == 'path' and p.get('x-ms-skip-url-encoding'))
            routes[identifier] = {'method': method.upper(), 'path': path, **({'multi': multi} if multi else {})}
            if identifier not in wanted:
                continue
            entry = {'method': method.upper(), 'path': path, 'parameters': parameters, 'responses': {}}
            for code, response in sorted(op.get('responses', {}).items()):
                schema = response.get('schema')
                if schema is None:
                    entry['responses'][code] = ''
                elif '$ref' in schema:
                    entry['responses'][code] = self.definition(file, schema['$ref'])
                else:
                    name = (file, identifier + '.' + code)
                    self.reachable[name] = schema
                    self.queue.append(name)
                    entry['responses'][code] = name
            if op.get('x-ms-long-running-operation'):
                entry['longRunning'] = True
            pageable = op.get('x-ms-pageable')
            if pageable is not None:
                entry['pageable'] = {'nextLinkName': pageable.get('nextLinkName') or '',
                                     'itemName': pageable.get('itemName') or 'value'}
            selected[identifier] = entry
        missing = sorted(set(wanted) - set(selected))
        if missing:
            raise SystemExit(f'unknown official operations {missing}')
        return routes, selected

    def route_parameter(self, file, raw):
        if '$ref' in raw:
            file, _, raw = self.specs.pointer(file, raw['$ref'])
        return {k: raw[k] for k in ('name', 'in', 'x-ms-skip-url-encoding') if k in raw}

    def close(self):
        """Visit every reachable definition, including discriminator subtypes."""
        while self.queue:
            file, name = self.queue.pop()
            node = self.reachable[(file, name)]
            self.refs(file, node)
            if 'discriminator' in node:
                for subtype in self.subtypes(file, name):
                    if subtype not in self.reachable:
                        self.reachable[subtype] = self.specs.load(subtype[0])['definitions'][subtype[1]]
                        self.queue.append(subtype)

    def refs(self, file, node):
        if isinstance(node, dict):
            if '$ref' in node:
                self.definition(file, node['$ref'])
            for key, value in node.items():
                if key not in ('x-ms-examples', 'example', 'examples'):
                    self.refs(file, value)
        elif isinstance(node, list):
            for value in node:
                self.refs(file, value)

    def subtypes(self, base_file, base):
        found = []
        for file in sorted({base_file, *self.files}):
            for name, node in sorted(self.specs.load(file).get('definitions', {}).items()):
                for parent in node.get('allOf', []):
                    if '$ref' in parent and self.specs.pointer(file, parent['$ref'])[:2] == (base_file, '/definitions/' + base):
                        found.append((file, name))
        return found


def local_names(keys):
    """Plain names unless two files define the same name; then qualify all."""
    by_name = {}
    for file, name in keys:
        by_name.setdefault(name, set()).add(file)
    names = {}
    for file, name in keys:
        if len(by_name[name]) == 1:
            names[(file, name)] = name
        else:
            parts = file.split('/')
            prefix = '.'.join(parts[-3:-1]) if 'common-types' in parts else parts[-1].removesuffix('.json')
            names[(file, name)] = prefix + '.' + name
    return names


def flags(file, node, extractor, names):
    """Keep only the schema traits the simulator engine reads."""
    if '$ref' in node:
        out = {'$ref': names[extractor.definition(file, node['$ref'])]}
        for key in ('readOnly', 'x-ms-mutability', 'x-ms-secret', 'x-nullable'):
            if key in node:
                out[key] = node[key]
        return out
    out = {k: node[k] for k in SCHEMA_KEYS if k in node}
    if 'enum' in node:
        out['enum'] = node['enum']
        if node.get('x-ms-enum', {}).get('modelAsString'):
            out['x-ms-enum-model-as-string'] = True
    if 'required' in node and isinstance(node['required'], list):
        out['required'] = sorted(node['required'])
    if 'items' in node:
        out['items'] = flags(file, node['items'], extractor, names)
    if 'additionalProperties' in node:
        extra = node['additionalProperties']
        out['additionalProperties'] = extra if isinstance(extra, bool) else flags(file, extra, extractor, names)
    if 'properties' in node:
        out['properties'] = {k: flags(file, v, extractor, names) for k, v in sorted(node['properties'].items())}
    if 'allOf' in node:
        out['allOf'] = [flags(file, v, extractor, names) for v in node['allOf']]
    return out


def extract(specs, namespace, entry):
    extractor = Extractor(specs, entry['files'])
    routes, selected = extractor.extract(set(entry['operations']))
    extractor.close()
    names = local_names(sorted(extractor.reachable))
    definitions = {}
    for key, node in sorted(extractor.reachable.items()):
        definitions[names[key]] = flags(key[0], node, extractor, names)
        if 'discriminator' in node:
            subtypes = {}
            for sub in extractor.subtypes(*key):
                value = specs.load(sub[0])['definitions'][sub[1]].get('x-ms-discriminator-value', sub[1])
                subtypes[value] = names[sub]
            definitions[names[key]]['x-subtypes'] = dict(sorted(subtypes.items()))
    for op in selected.values():
        for p in op['parameters']:
            if 'schema' in p:
                p['schema'] = names[p['schema']]
        op['responses'] = {code: names[ref] if ref else '' for code, ref in op['responses'].items()}
    files = sorted({*entry['files'], *(file for file, _ in extractor.reachable)})
    return {
        'namespace': namespace,
        'apiVersion': entry['apiVersion'],
        'source': {'repository': 'github.com/Azure/azure-rest-api-specs', 'commit': SELECTION_DATA['commit'], 'license': 'MIT',
                   'files': [{'path': f, 'sha256': specs.hashes[f]} for f in files]},
        'routes': dict(sorted(routes.items())),
        'operations': dict(sorted(selected.items())),
        'definitions': dict(sorted(definitions.items())),
    }


SELECTION_DATA = {}


def main(argv):
    if len(argv) != 2:
        raise SystemExit(__doc__)
    checkout = Path(argv[1]).resolve()
    SELECTION_DATA.update(json.loads(SELECTION.read_text()))
    commit = head_commit(checkout)
    if commit != SELECTION_DATA['commit']:
        raise SystemExit(f'azure-rest-api-specs {SELECTION_DATA["commit"]} is required; found {commit}')
    specs = Specs(checkout)
    OUTPUT.mkdir(parents=True, exist_ok=True)
    wanted = set()
    for namespace, entry in sorted(SELECTION_DATA['providers'].items()):
        for file in entry['files']:
            if f'/stable/{entry["apiVersion"]}/' not in file:
                raise SystemExit(f'{namespace}: {file} is not a stable {entry["apiVersion"]} file')
        target = OUTPUT / f'{namespace}.json'
        target.write_text(json.dumps(extract(specs, namespace, entry), sort_keys=True, separators=(',', ':')) + '\n')
        wanted.add(target.name)
    for stale in OUTPUT.glob('*.json'):
        if stale.name not in wanted:
            stale.unlink()
    print(f'Extracted {len(wanted)} official ARM subsets into {OUTPUT.relative_to(ROOT)}')


if __name__ == '__main__':
    sys.exit(main(sys.argv))
