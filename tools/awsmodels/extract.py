"""Extract a minimal, documentation-free subset of official botocore models.

Development-only. The simulator binary never imports Python or botocore. The
output under internal/aws/catalog/models keeps only the shapes reachable from
the implemented operations, plus routing metadata for every official operation
so the runtime can refuse real-but-unimplemented operations explicitly.

Usage: python tools/awsmodels/extract.py  (requires the pinned botocore from
scripts/requirements-test.txt). The selection is internal/aws/catalog/selection.json.
"""
import gzip
import hashlib
import json
import os
import sys
from pathlib import Path

import botocore

ROOT = Path(__file__).resolve().parents[2]
SELECTION = ROOT / 'internal/aws/catalog/selection.json'
OUTPUT = ROOT / 'internal/aws/catalog/models'
PINNED = '1.41.6'
KEEP = {'type', 'members', 'member', 'key', 'value', 'required', 'enum', 'min', 'max',
        'pattern', 'location', 'locationName', 'idempotencyToken', 'timestampFormat',
        'jsonvalue', 'payload', 'streaming', 'sensitive', 'flattened', 'xmlName',
        'queryName', 'error', 'exception', 'document', 'union', 'hostLabel', 'box',
        'xmlNamespace', 'xmlAttribute', 'eventstream', 'requiresLength', 'default'}
MEMBER_KEEP = {'shape', 'location', 'locationName', 'idempotencyToken', 'jsonvalue',
               'timestampFormat', 'flattened', 'xmlName', 'queryName', 'streaming',
               'hostLabel', 'xmlNamespace', 'xmlAttribute', 'eventpayload', 'box', 'default'}


def load(path):
    if path.exists():
        data = path.read_bytes()
    else:
        data = gzip.decompress((path.parent / (path.name + '.gz')).read_bytes())
    return data, json.loads(data)


def model_dir(service):
    root = Path(botocore.__file__).parent / 'data' / service
    versions = sorted(v for v in os.listdir(root) if (root / v).is_dir())
    return root / versions[-1]


def clean_shape(shape):
    out = {k: v for k, v in shape.items() if k in KEEP}
    if 'members' in out:
        out['members'] = {name: {k: v for k, v in member.items() if k in MEMBER_KEEP}
                          for name, member in out['members'].items()}
    for key in ('member', 'key', 'value'):
        if key in out:
            out[key] = {k: v for k, v in out[key].items() if k in MEMBER_KEEP}
    return out


def closure(shapes, roots):
    seen, stack = set(), [r for r in roots if r]
    while stack:
        name = stack.pop()
        if name in seen:
            continue
        seen.add(name)
        shape = shapes[name]
        for member in shape.get('members', {}).values():
            stack.append(member['shape'])
        for key in ('member', 'key', 'value'):
            if key in shape:
                stack.append(shape[key]['shape'])
    return seen


def extract(service, operations):
    directory = model_dir(service)
    raw, model = load(directory / 'service-2.json')
    paginators = {}
    page_path = directory / 'paginators-1.json'
    if page_path.exists() or (directory / 'paginators-1.json.gz').exists():
        paginators = load(page_path)[1].get('pagination', {})
    missing = sorted(set(operations) - set(model['operations']))
    if missing:
        raise SystemExit(f'{service}: unknown official operations {missing}')
    routes = {}
    for name, op in sorted(model['operations'].items()):
        routes[name] = {'http': op.get('http', {})}
    selected = {}
    roots = []
    for name in sorted(operations):
        op = model['operations'][name]
        entry = {'http': op.get('http', {})}
        for key in ('input', 'output'):
            if key in op:
                entry[key] = op[key]['shape']
                roots.append(op[key]['shape'])
        entry['errors'] = [e['shape'] for e in op.get('errors', [])]
        roots.extend(entry['errors'])
        if name in paginators:
            entry['paginator'] = paginators[name]
        if op.get('endpoint'):
            entry['endpoint'] = op['endpoint']
        selected[name] = entry
    shapes = {name: clean_shape(model['shapes'][name])
              for name in sorted(closure(model['shapes'], roots))}
    metadata = {k: v for k, v in model['metadata'].items()
                if k in ('apiVersion', 'endpointPrefix', 'jsonVersion', 'protocol', 'serviceFullName',
                         'serviceId', 'signingName', 'targetPrefix', 'globalEndpoint', 'xmlNamespace')}
    return {'source': {'package': 'botocore', 'version': botocore.__version__,
                       'file': f'botocore/data/{service}/{directory.name}/service-2.json',
                       'sha256': hashlib.sha256(raw).hexdigest(), 'license': 'Apache-2.0'},
            'metadata': metadata, 'routes': routes, 'operations': selected, 'shapes': shapes}


def main():
    if botocore.__version__ != PINNED:
        raise SystemExit(f'botocore {PINNED} is required; found {botocore.__version__}')
    selection = json.loads(SELECTION.read_text())
    OUTPUT.mkdir(parents=True, exist_ok=True)
    wanted = set()
    for service, operations in sorted(selection.items()):
        data = extract(service, operations)
        target = OUTPUT / f'{service}.json'
        target.write_text(json.dumps(data, sort_keys=True, separators=(',', ':')) + '\n')
        wanted.add(target.name)
    for stale in OUTPUT.glob('*.json'):
        if stale.name not in wanted:
            stale.unlink()
    print(f'Extracted {len(selection)} official service models into {OUTPUT.relative_to(ROOT)}')


if __name__ == '__main__':
    sys.exit(main())
