"""Fixture-driven boto3 lifecycles for catalog services added after the first four.

Each test/e2e/catalog_fixtures/<model>.json lists native steps. The driver runs
them against two real binaries (separate databases and token authorities),
requires every bound operation of that service to succeed at least once, checks
that a participant without permissions is refused before any change, restarts
the owning process, re-reads state and cleans up. No AWS account or network.
"""
import json
import os
from pathlib import Path
import re
import sys
import tempfile

from botocore.exceptions import ClientError
from aws_catalog import Team

FIXTURES = Path(__file__).with_name('catalog_fixtures')
TOKEN = re.compile(r'\$\{([A-Za-z0-9_]+)\}')


def substitute(value, saved):
    if isinstance(value, str):
        whole = TOKEN.fullmatch(value)
        if whole:
            return saved[whole.group(1)]
        return TOKEN.sub(lambda m: str(saved[m.group(1)]), value)
    if isinstance(value, list):
        return [substitute(v, saved) for v in value]
    if isinstance(value, dict):
        return {k: substitute(v, saved) for k, v in value.items()}
    return value


def lookup(value, path):
    for part in re.findall(r'[^.\[\]]+|\[\d+\]', path):
        value = value[int(part[1:-1])] if part.startswith('[') else value[part]
    return value


def run_steps(team, fixture, steps, saved, called, token=None):
    client = team.client(fixture['client'], token)
    for step in steps:
        args = substitute(step.get('args', {}), saved)
        operation = client.meta.method_to_api_mapping[step['op']]
        try:
            response = getattr(client, step['op'])(**args)
        except ClientError as error:
            if 'error' not in step:
                raise AssertionError(f"{fixture['client']}.{step['op']} failed: {error.response['Error']}") from error
            assert error.response['Error']['Code'] == step['error'], (step, error.response['Error'])
            if 'status' in step:
                assert error.response['ResponseMetadata']['HTTPStatusCode'] == step['status'], (step, error.response['ResponseMetadata'])
            continue
        assert 'error' not in step, f"{fixture['client']}.{step['op']} unexpectedly succeeded"
        called.add(operation)
        for name, path in step.get('save', {}).items():
            saved[name] = lookup(response, path)
        for path, expected in step.get('expect', {}).items():
            actual = len(lookup(response, path[:-6])) if path.endswith('#count') else lookup(response, path)
            assert actual == substitute(expected, saved), (step['op'], path, actual, expected)


def main(selected=None):
    fixtures = sorted(FIXTURES.glob('*.json'))
    if selected:
        fixtures = [f for f in fixtures if f.stem in selected]
    assert fixtures, 'no catalog fixtures'
    with tempfile.TemporaryDirectory(prefix='tcsim-fixture-a-') as a_dir, tempfile.TemporaryDirectory(prefix='tcsim-fixture-b-') as b_dir:
        a, b = Team(a_dir), Team(b_dir)
        a.start()
        b.start()
        try:
            services = {s['model']: s for s in a.control('GET', '/_simulator/v1/capabilities')['services']['catalog']['services']}
            a.control('PUT', '/_simulator/v1/iam/principals/catalog-nobody/policy',
                      {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': 'sts:GetCallerIdentity', 'Resource': '*'}]})
            nobody = a.control('POST', '/_simulator/v1/access', {'Principal': 'catalog-nobody'})['AccessToken']
            states = {}
            for path in fixtures:
                fixture = json.loads(path.read_text())
                model = path.stem
                assert model in services, f'{model} fixture has no catalog service'
                saved, called = {}, set()
                first = fixture['steps'][0]
                denied = {**first, 'error': 'AccessDeniedException', 'status': 403}
                denied.pop('save', None)
                denied.pop('expect', None)
                run_steps(a, fixture, [denied], {}, set(), token=nobody)
                run_steps(a, fixture, fixture['steps'], saved, called)
                run_steps(b, fixture, fixture.get('isolation', []), {}, set())
                states[model] = (fixture, saved, called)
            a.stop()
            a.start()
            for model, (fixture, saved, called) in states.items():
                run_steps(a, fixture, fixture.get('after_restart', []), saved, called)
                run_steps(a, fixture, fixture.get('cleanup', []), saved, called)
                missing = set(services[model]['operations']) - called
                assert not missing, f'{model} bound operations were not exercised: {sorted(missing)}'
        finally:
            for team in (a, b):
                if team.process is not None:
                    team.stop()
    print('Catalog fixture lifecycles passed for', ', '.join(f.stem for f in fixtures))


if __name__ == '__main__':
    main(set(sys.argv[1:]) or None)
