"""Fixture-driven Azure Resource Manager lifecycles through official azure-mgmt SDKs.

Each test/e2e/azure_catalog_fixtures/<Namespace>.json lists SDK calls. The
driver runs the pinned azure-mgmt-* clients against two real binaries with
separate databases and token authorities. A custom azure-core policy adds the
local X-Simulator-Token and refuses any non-loopback origin; the credential
is a dummy that is never asked for a token. Every bound operation must be
reached at least once (proved by the X-Simulator-Azure-Operation header),
another subject is refused, a second team stays empty, a wrong api-version
and an unbound official operation are refused, state survives restart and
cleanup removes everything. No Azure account, sign-in or network.
"""
import importlib
import json
from pathlib import Path
import re
import sys
import tempfile
from urllib.parse import urlsplit

from azure.core.exceptions import HttpResponseError
from azure.core.pipeline.policies import SansIOHTTPPolicy
from aws_catalog import Team

FIXTURES = Path(__file__).with_name('azure_catalog_fixtures')
TOKEN = re.compile(r'\$\{([A-Za-z0-9_]+)\}')
SUBSCRIPTION = '00000000-0000-4000-8000-000000000001'
TENANT = '00000000-0000-4000-8000-000000000000'
GROUP = 'catalog-fixtures'
CLIENTS = {
    'resources': 'azure.mgmt.resource.resources:ResourceManagementClient',
    'keyvault': 'azure.mgmt.keyvault:KeyVaultManagementClient',
    'network': 'azure.mgmt.network:NetworkManagementClient',
    'msi': 'azure.mgmt.msi:ManagedServiceIdentityClient',
    'loganalytics': 'azure.mgmt.loganalytics:LogAnalyticsManagementClient',
    'appconfiguration': 'azure.mgmt.appconfiguration:AppConfigurationManagementClient',
    'containerregistry': 'azure.mgmt.containerregistry:ContainerRegistryManagementClient',
    'eventhub': 'azure.mgmt.eventhub:EventHubManagementClient',
    'servicebus': 'azure.mgmt.servicebus:ServiceBusManagementClient',
    'cosmosdb': 'azure.mgmt.cosmosdb:CosmosDBManagementClient',
    'web': 'azure.mgmt.web:WebSiteManagementClient',
}


class LocalToken(SansIOHTTPPolicy):
    """Adds the local access token and refuses anything but the owned origin."""

    def __init__(self, team, token, reached):
        super().__init__()
        self.team, self.token, self.reached = team, token, reached

    def on_request(self, request):
        target = urlsplit(request.http_request.url)
        assert target.scheme == 'http' and target.hostname == '127.0.0.1' and target.port == self.team.port, \
            'client left the owned loopback origin'
        request.http_request.headers['X-Simulator-Token'] = self.token

    def on_response(self, request, response):
        operation = response.http_response.headers.get('X-Simulator-Azure-Operation')
        if operation:
            self.reached.add(operation)


class DummyCredential:
    """Never issues a token: the local policy replaces bearer authentication."""

    def get_token(self, *scopes, **kwargs):
        raise AssertionError('the dummy credential must not be asked for a token')


def client(team, name, token=None, reached=None, **options):
    module, cls = CLIENTS[name].split(':')
    factory = getattr(importlib.import_module(module), cls)
    return factory(DummyCredential(), SUBSCRIPTION, base_url=team.endpoint + '/_simulator/providers/azure-arm',
                   authentication_policy=LocalToken(team, token or team.token, reached if reached is not None else set()),
                   retry_total=0, connection_timeout=5, read_timeout=10, **options)


def substitute(value, saved):
    if isinstance(value, str):
        whole = TOKEN.fullmatch(value)
        if whole:
            return saved[whole.group(1)]
        return TOKEN.sub(lambda m: str(saved[m.group(1)]), value)
    if isinstance(value, list):
        return [substitute(v, saved) for v in value]
    if isinstance(value, dict):
        return {substitute(k, saved): substitute(v, saved) for k, v in value.items()}
    return value


def lookup(value, path):
    for part in re.findall(r'[^.\[\]]+|\[\d+\]', path):
        if part.startswith('['):
            value = value[int(part[1:-1])]
        elif isinstance(value, dict):
            value = value[part]
        else:
            value = getattr(value, part)
    return value


def settle(result):
    """Finish pollers and drain pagers so every page request is sent."""
    if hasattr(result, 'result') and hasattr(result, 'done'):
        return result.result()
    if hasattr(result, 'by_page'):
        return list(result)
    return result


def run_steps(team, fixture, steps, saved, reached, token=None, **options):
    clients = {}
    for step in steps:
        name = step.get('client', fixture['client'])
        if name not in clients:
            clients[name] = client(team, name, token, reached, **options)
        target = clients[name]
        parts = step['call'].split('.')
        for part in parts[:-1]:
            target = getattr(target, part)
        args = substitute(step.get('args', {}), saved)
        try:
            response = settle(getattr(target, parts[-1])(**args))
        except HttpResponseError as error:
            if 'error' not in step:
                raise AssertionError(f"{fixture['namespace']} {step['call']} failed: {error}") from error
            assert error.error is not None and error.error.code == step['error'], (step['call'], error.error and error.error.code, error.message)
            if 'status' in step:
                assert error.status_code == step['status'], (step['call'], error.status_code)
            continue
        assert 'error' not in step, f"{fixture['namespace']} {step['call']} unexpectedly succeeded"
        for key, path in step.get('save', {}).items():
            saved[key] = lookup(response, path)
        for path, expected in step.get('expect', {}).items():
            if path == '#count':
                actual = len(response)
            elif path == '#value':
                actual = response
            else:
                actual = lookup(response, path)
            if hasattr(actual, 'value') and not isinstance(actual, (str, int, bool, list, dict)):
                actual = actual.value
            assert actual == substitute(expected, saved), (step['call'], path, actual, expected)


def main(selected=None):
    fixtures = sorted(FIXTURES.glob('*.json'))
    if selected:
        fixtures = [f for f in fixtures if f.stem in selected]
    assert fixtures, 'no Azure catalog fixtures'
    with tempfile.TemporaryDirectory(prefix='tcsim-azure-a-') as a_dir, tempfile.TemporaryDirectory(prefix='tcsim-azure-b-') as b_dir:
        a, b = Team(a_dir), Team(b_dir)
        a.start()
        b.start()
        try:
            summary = a.control('GET', '/_simulator/v1/capabilities')['azureCatalog']
            assert summary['subscription'] == SUBSCRIPTION and summary['tenant'] == TENANT
            catalog = a.control('GET', summary['descriptors'])
            providers = {p['namespace']: p for p in catalog['providers']}
            assert {p['namespace']: p['operations'] for p in summary['providers']} == {k: len(p['operations']) for k, p in providers.items()}
            # AWS-style policies grant nothing: only the configured Azure owner may act.
            a.control('PUT', '/_simulator/v1/iam/principals/azure-outsider/policy',
                      {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': '*', 'Resource': '*'}]})
            outsider = a.control('POST', '/_simulator/v1/access', {'Principal': 'azure-outsider'})['AccessToken']
            setup = {'namespace': 'setup', 'client': 'resources'}
            run_steps(a, setup, [{'call': 'resource_groups.create_or_update', 'args': {'resource_group_name': GROUP, 'parameters': {'location': 'eastus'}}}], {}, set())
            states, reached = {}, set()
            for path in fixtures:
                fixture = json.loads(path.read_text())
                namespace = fixture['namespace']
                assert namespace == path.stem and namespace in providers, f'{path.stem} has no reviewed provider'
                saved = {'rg': GROUP, 'subscription': SUBSCRIPTION, 'tenant': TENANT}
                denied = {**fixture['steps'][0], 'error': 'AuthorizationFailed', 'status': 403}
                denied.pop('save', None)
                denied.pop('expect', None)
                run_steps(a, fixture, [denied], dict(saved), set(), token=outsider)
                run_steps(a, fixture, fixture['steps'], saved, reached)
                # The pinned client with another api-version reaches the same route and is refused.
                probe = {**fixture['probe'], 'error': 'InvalidApiVersionParameter', 'status': 400}
                run_steps(a, fixture, [probe], saved, set(), api_version=fixture.get('probeVersion', '2015-01-01'))
                run_steps(a, fixture, [{**fixture['unbound'], 'error': 'NotImplemented', 'status': 501}], saved, set())
                run_steps(b, fixture, fixture.get('isolation', []), dict(saved), set())
                states[namespace] = (fixture, saved)
            a.stop()
            a.start()
            for namespace, (fixture, saved) in sorted(states.items(), reverse=True):
                run_steps(a, fixture, fixture.get('after_restart', []), saved, reached)
                run_steps(a, fixture, fixture.get('cleanup', []), saved, reached)
            run_steps(a, setup, [{'call': 'resource_groups.begin_delete', 'args': {'resource_group_name': GROUP}},
                                 {'call': 'resource_groups.list', 'expect': {'#count': 0}}], {}, set())
            for namespace in states:
                missing = {f'{namespace}/{op}' for op in providers[namespace]['operations']} - reached
                assert not missing, f'{namespace} bound operations were not reached through the SDK: {sorted(missing)}'
        finally:
            for team in (a, b):
                if team.process is not None:
                    team.stop()
    print('Azure catalog SDK fixture lifecycles passed for', ', '.join(f.stem for f in fixtures))


if __name__ == '__main__':
    main(set(sys.argv[1:]) or None)
