#!/usr/bin/env python3
"""Deterministic, offline Smithy shape extraction, not a server generator."""
import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DATA = ROOT / 'internal/aws/testdata/smithy'
OPERATIONS = ('CreateQueue', 'SendMessage', 'ReceiveMessage', 'DeleteMessage', 'DeleteQueue')

def extract(model):
    if model.get('smithy') != '2.0':
        raise ValueError('expected Smithy 2.0')
    shapes = model['shapes']
    service = shapes['com.amazonaws.sqs#AmazonSQS']
    if 'aws.protocols#awsJson1_0' not in service['traits']:
        raise ValueError('expected SQS AWS JSON 1.0')
    allowed = {op['target'] for op in service['operations']}
    operations = {}
    pending = []
    for name in OPERATIONS:
        key = 'com.amazonaws.sqs#' + name
        if key not in allowed or shapes[key]['type'] != 'operation':
            raise ValueError('missing operation ' + key)
        operations[name] = {direction: shapes[key][direction]['target'] for direction in ('input', 'output')}
        pending.extend(operations[name].values())
    selected = {}
    def clean(value):
        if isinstance(value, dict):
            return {k: clean(v) for k, v in value.items() if k != 'smithy.api#documentation'}
        if isinstance(value, list):
            return [clean(v) for v in value]
        return value
    def targets(value):
        if isinstance(value, dict):
            for k, v in value.items():
                if k == 'target':
                    yield v
                else:
                    yield from targets(v)
        elif isinstance(value, list):
            for v in value:
                yield from targets(v)
    while pending:
        key = pending.pop()
        if key in selected:
            continue
        shape = {'type': 'structure', 'members': {}} if key == 'smithy.api#Unit' else shapes[key]
        selected[key] = clean(shape)
        pending.extend(targets(shape))
    return {'serviceVersion': service['version'], 'operations': operations, 'shapes': selected}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    manifest = json.loads((DATA / 'source.json').read_text())
    raw = (DATA / 'sqs.json').read_bytes()
    digest = hashlib.sha256(raw).hexdigest()
    if digest != manifest['sha256']:
        raise SystemExit('source checksum mismatch: review upstream update before changing the pin')
    output = extract(json.loads(raw))
    output['sourceSHA256'] = digest
    output['notice'] = ('Modified derivative of AWS SDK for Go v2 SQS Smithy model: selected input/output shapes; '
                        'documentation removed; Unit synthesized. Apache-2.0; see LICENSE.txt and NOTICE.txt.')
    text = json.dumps(output, sort_keys=True, indent=2) + '\n'
    destination = DATA / 'sqs-contracts.json'
    if args.check:
        if not destination.exists() or destination.read_text() != text:
            raise SystemExit('stale Smithy contracts; run python3 scripts/smithy-contracts.py')
        print('Smithy SQS contracts verified (five operations, offline)')
    else:
        destination.write_text(text)

if __name__ == '__main__':
    main()
