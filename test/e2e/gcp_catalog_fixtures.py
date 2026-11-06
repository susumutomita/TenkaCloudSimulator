"""Fixture-driven Google API lifecycles through the official discovery client.

Each test/e2e/gcp_catalog_fixtures/<api>.<version>.json lists native calls.
The driver uses google-api-python-client with its pinned static discovery
documents against two real binaries (separate databases and token
authorities). Every reviewed method must succeed at least once, another
subject and another project are refused, a second team stays empty, state
survives restart and cleanup removes everything. No Google account or network.
"""
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import urllib.parse

import httplib2
from googleapiclient.discovery import build
from googleapiclient.errors import HttpError
from aws_catalog import Team

FIXTURES = Path(__file__).with_name('gcp_catalog_fixtures')
TOKEN = re.compile(r'\$\{([A-Za-z0-9_]+)\}')


class TokenHttp(httplib2.Http):
    """Adds the local access token and refuses anything but the owned origin."""

    def __init__(self, team, token):
        super().__init__()
        self.team, self.token = team, token

    def request(self, uri, method='GET', body=None, headers=None, **kwargs):
        target = urllib.parse.urlsplit(uri)
        assert target.netloc == f'127.0.0.1:{self.team.port}', 'client left the owned loopback origin'
        headers = dict(headers or {})
        headers['X-Simulator-Token'] = self.token
        return super().request(uri, method, body=body, headers=headers, **kwargs)


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


def client(team, fixture, token=None):
    name, version = fixture['api'].split('.', 1)
    return build(name, version, static_discovery=True, http=TokenHttp(team, token or team.token),
                 client_options={'api_endpoint': f'{team.endpoint}/_simulator/providers/gcp/{name}/'})


def run_steps(team, fixture, steps, saved, called, token=None):
    service = client(team, fixture, token)
    for step in steps:
        target = service
        parts = step['call'].split('.')
        for part in parts[:-1]:
            target = getattr(target, part)()
        args = substitute(step.get('args', {}), saved)
        if 'body' in step:
            args['body'] = substitute(step['body'], saved)
        try:
            response = getattr(target, parts[-1])(**args).execute(num_retries=0)
        except HttpError as error:
            if 'error' not in step:
                raise AssertionError(f"{fixture['api']} {step['call']} failed: {error.content!r}") from error
            status = json.loads(error.content)['error']['status']
            assert status == step['error'], (step, status)
            if 'status' in step:
                assert error.resp.status == step['status'], (step, error.resp.status)
            continue
        assert 'error' not in step, f"{fixture['api']} {step['call']} unexpectedly succeeded"
        called.add(step['call'])
        for name, path in step.get('save', {}).items():
            saved[name] = lookup(response, path)
        for path, expected in step.get('expect', {}).items():
            actual = len(lookup(response, path[:-6])) if path.endswith('#count') else lookup(response, path)
            assert actual == substitute(expected, saved), (step['call'], path, actual, expected)


def main(selected=None):
    fixtures = sorted(FIXTURES.glob('*.json'))
    if selected:
        fixtures = [f for f in fixtures if f.stem in selected]
    assert fixtures, 'no GCP catalog fixtures'
    with tempfile.TemporaryDirectory(prefix='tcsim-gcp-a-') as a_dir, tempfile.TemporaryDirectory(prefix='tcsim-gcp-b-') as b_dir:
        a, b = Team(a_dir), Team(b_dir)
        a.start()
        b.start()
        try:
            apis = {f"{api['name']}.{api['version']}": api for api in a.control('GET', '/_simulator/v1/capabilities')['gcpCatalog']['apis']}
            a.control('PUT', '/_simulator/v1/iam/principals/gcp-outsider/policy',
                      {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': '*', 'Resource': '*'}]})
            outsider = a.control('POST', '/_simulator/v1/access', {'Principal': 'gcp-outsider'})['AccessToken']
            states = {}
            for path in fixtures:
                fixture = json.loads(path.read_text())
                assert fixture['api'] == path.stem and path.stem in apis, f'{path.stem} has no reviewed API'
                saved, called = {}, set()
                first = {**fixture['steps'][0], 'error': 'PERMISSION_DENIED', 'status': 403}
                first.pop('save', None)
                first.pop('expect', None)
                # Only the configured project owner may act; AWS-style policies grant nothing here.
                run_steps(a, fixture, [first], {}, set(), token=outsider)
                run_steps(a, fixture, fixture['steps'], saved, called)
                run_steps(b, fixture, fixture.get('isolation', []), {}, set())
                states[path.stem] = (fixture, saved, called)
            a.stop()
            a.start()
            for key, (fixture, saved, called) in states.items():
                run_steps(a, fixture, fixture.get('after_restart', []), saved, called)
                run_steps(a, fixture, fixture.get('cleanup', []), saved, called)
                missing = set(apis[key]['methods']) - called
                assert not missing, f'{key} reviewed methods were not exercised: {sorted(missing)}'
        finally:
            for team in (a, b):
                if team.process is not None:
                    team.stop()
    print('GCP catalog fixture lifecycles passed for', ', '.join(f.stem for f in fixtures))


if __name__ == '__main__':
    main(set(sys.argv[1:]) or None)
