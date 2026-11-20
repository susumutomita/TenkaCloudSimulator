"""Fixture-driven boto3 lifecycles for catalog services added after the first four.

Each test/e2e/catalog_fixtures/<model>.json lists native steps. The driver runs
them against two real binaries (separate databases and token authorities),
requires every bound operation of that service to succeed at least once, checks
that a participant without permissions is refused before any change, restarts
the owning process, re-reads state and cleans up. No AWS account or network.

A fixture may name the catalog model it runs against with "model" when another
SDK client speaks the same wire API (the neptune and docdb clients send RDS
Actions signed as rds). Such a fixture must call only operations that model
binds, but is not required to call all of them: only a model's own fixture
(no "model" key) carries the every-operation rule. Borrowing fixtures run in a
separate pair of binaries so they cannot disturb the model's own fixture.
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
    if fixture.get('inject_host_prefix') is False:
        # Official host prefixes (for example MWAA "api.") cannot address loopback.
        client.meta.config.inject_host_prefix = False
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
    borrowed = [f for f in fixtures if 'model' in json.loads(f.read_text())]
    own = [f for f in fixtures if f not in borrowed]
    for group in (own, borrowed):
        if group:
            run_group(group)
    print('Catalog fixture lifecycles passed for', ', '.join(f.stem for f in fixtures))


def run_group(fixtures):
    with tempfile.TemporaryDirectory(prefix='tcsim-fixture-a-') as a_dir, tempfile.TemporaryDirectory(prefix='tcsim-fixture-b-') as b_dir:
        a, b = Team(a_dir), Team(b_dir)
        a.start()
        b.start()
        try:
            descriptors = a.control('GET', '/_simulator/v1/capabilities')['services']['catalog']['descriptors']
            services = {s['model']: s for s in a.control('GET', descriptors)['services']}
            a.control('PUT', '/_simulator/v1/iam/principals/catalog-nobody/policy',
                      {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': 'sts:GetCallerIdentity', 'Resource': '*'}]})
            nobody = a.control('POST', '/_simulator/v1/access', {'Principal': 'catalog-nobody'})['AccessToken']
            states = {}
            for path in fixtures:
                fixture = json.loads(path.read_text())
                model = fixture.get('model', path.stem)
                assert model in services, f'{path.stem} fixture has no catalog service {model}'
                saved, called = {}, set()
                first = fixture['steps'][0]
                # Query services answer AccessDenied; JSON and REST services
                # AccessDeniedException. A fixture names its code in "denied".
                denied = {**first, 'error': fixture.get('denied', 'AccessDeniedException'), 'status': fixture.get('denied_status', 403)}
                denied.pop('save', None)
                denied.pop('expect', None)
                run_steps(a, fixture, [denied], {}, set(), token=nobody)
                run_steps(a, fixture, fixture['steps'], saved, called)
                run_steps(b, fixture, fixture.get('isolation', []), {}, set())
                states[path.stem] = (fixture, saved, called)
            a.stop()
            a.start()
            for name, (fixture, saved, called) in states.items():
                run_steps(a, fixture, fixture.get('after_restart', []), saved, called)
                run_steps(a, fixture, fixture.get('cleanup', []), saved, called)
                bound = set(services[fixture.get('model', name)]['operations'])
                assert called <= bound, f'{name} called operations the model does not bind: {sorted(called - bound)}'
                missing = bound - called
                assert 'model' in fixture or not missing, f'{name} bound operations were not exercised: {sorted(missing)}'
        finally:
            for team in (a, b):
                if team.process is not None:
                    team.stop()


if __name__ == '__main__':
    main(set(sys.argv[1:]) or None)
