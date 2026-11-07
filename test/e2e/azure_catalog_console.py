"""Azure Resource Manager console and served reference against the real binary.

The browser signs in with a real local token and uses only same-origin
requests to the official ARM paths. The official azure-mgmt clients see the
same SQLite state. Desktop and 390 px layouts are checked with axe; no Azure
portal screen is claimed.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
from urllib.parse import urlsplit

from playwright.sync_api import sync_playwright, expect
from reflow import assert_reflow
from aws_catalog import Team
from azure_catalog_fixtures import client

REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()


def run():
    REPORTS.mkdir(parents=True, exist_ok=True)
    axe_path = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((axe_path.parent / 'provenance.json').read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance['bundleSha256']
    outside, errors, methods = [], [], set()
    with tempfile.TemporaryDirectory(prefix='tcsim-azure-ui-') as directory:
        team = Team(directory)
        team.start()
        try:
            resources = client(team, 'resources')
            vaults = client(team, 'keyvault')
            network = client(team, 'network')
            with sync_playwright() as p:
                browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
                context = browser.new_context(viewport={'width': 1440, 'height': 1000})

                def local_only(route):
                    target = urlsplit(route.request.url)
                    if target.netloc != urlsplit(team.endpoint).netloc:
                        outside.append(target.netloc)
                        route.abort()
                    else:
                        route.continue_()

                context.route('**/*', local_only)
                page = context.new_page()
                page.on('pageerror', lambda error: errors.append(str(error)))

                def observe(request):
                    if urlsplit(request.url).path.startswith('/_simulator/providers/azure-arm/'):
                        methods.add(request.method)

                page.on('request', observe)

                def audit(label):
                    for width in (1440, 390):
                        page.set_viewport_size({'width': width, 'height': 1000})
                        assert_reflow(page)
                        page.evaluate(axe_path.read_text())
                        result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                        assert not result['violations'], [(item['id'], [n['target'] for n in item['nodes']]) for item in result['violations']]
                        page.screenshot(path=str(REPORTS / f'azure-catalog-{label}-{width}.png'), animations='disabled')
                    page.set_viewport_size({'width': 1440, 'height': 1000})

                def create(name, fields=()):
                    page.get_by_role('button', name='+ Create', exact=True).click()
                    pane = page.get_by_role('dialog')
                    expect(pane).to_be_visible()
                    pane.get_by_label(re.compile('^Name')).fill(name)
                    for label, value in fields:
                        pane.get_by_label(label).fill(value)
                    pane.get_by_role('button', name='Create', exact=True).click()

                def menu(title):
                    # The 1440 px layout shows the type menu without its narrow-screen disclosure.
                    expect(page.get_by_role('button', name=re.compile('^Resource types:'))).to_be_hidden()
                    page.get_by_role('complementary', name='Resource types').get_by_role('button', name=title, exact=True).click()
                    expect(page.get_by_role('heading', level=1, name=title)).to_be_visible()

                page.goto(team.endpoint + '/_simulator/console/')
                page.get_by_label('Access token', exact=True).fill(team.token)
                page.get_by_role('button', name='Open sandbox', exact=True).click()
                page.get_by_role('button', name='Azure', exact=True).click()
                page.get_by_role('button', name='Services', exact=True).click()
                page.get_by_label('Search services in Azure').fill('key vault')
                page.get_by_role('menuitem', name='Resource Manager', exact=True).click()
                expect(page.get_by_role('heading', level=1, name='Resource groups')).to_be_visible()
                expect(page.get_by_text('No resources on this page. Select Create to add one.')).to_be_visible()
                # A console-made resource group is visible to the official SDK.
                create('ui-group')
                expect(page.get_by_role('status').filter(has_text='Resource created.')).to_be_visible()
                expect(page.get_by_role('link', name='ui-group', exact=True)).to_be_visible()
                assert resources.resource_groups.get('ui-group').properties.provisioning_state == 'Succeeded'
                audit('resource-groups')
                # Native validation errors stay visible in the pane.
                create('bad.')
                expect(page.get_by_role('dialog').get_by_role('alert')).to_contain_text('is invalid')
                page.get_by_role('dialog').get_by_role('button', name='Cancel', exact=True).click()
                # SDK writes appear after a refresh of the same native list.
                resources.resource_groups.create_or_update('sdk-group', {'location': 'westeurope'})
                page.get_by_role('button', name='↻ Refresh', exact=True).click()
                expect(page.get_by_role('link', name='sdk-group', exact=True)).to_be_visible()
                # A Key Vault in the chosen resource group, with its tenant prefilled.
                menu('Key vaults')
                expect(page.get_by_role('button', name='+ Create', exact=True)).to_be_disabled()
                page.get_by_label('Resource group', exact=True).select_option('ui-group')
                expect(page.get_by_text('No resources on this page. Select Create to add one.')).to_be_visible()
                create('ui-vault')
                expect(page.get_by_role('status').filter(has_text='Resource created.')).to_be_visible()
                expect(page.get_by_role('button', name='ui-vault', exact=True)).to_be_visible()
                vault = vaults.vaults.get('ui-group', 'ui-vault')
                assert vault.properties.sku.name == 'standard' and vault.properties.tenant_id == '00000000-0000-4000-8000-000000000000'
                audit('key-vaults')
                page.get_by_role('button', name='ui-vault', exact=True).click()
                expect(page.get_by_role('dialog').locator('pre')).to_contain_text('"provisioningState": "Succeeded"')
                audit('key-vault-detail')
                page.get_by_role('dialog').get_by_role('button', name='Close', exact=True).click()
                # Inline delete confirmation can be cancelled, then confirmed.
                page.get_by_label('Select ui-vault').check()
                page.get_by_role('button', name='Delete', exact=True).click()
                page.get_by_role('button', name='Cancel', exact=True).click()
                assert len(list(vaults.vaults.list_by_resource_group('ui-group'))) == 1
                page.get_by_label('Select ui-vault').check()
                page.get_by_role('button', name='Delete', exact=True).click()
                page.get_by_role('button', name='Confirm delete', exact=True).click()
                expect(page.get_by_role('status').filter(has_text='Resource deleted.')).to_be_visible()
                assert list(vaults.vaults.list_by_resource_group('ui-group')) == []
                # A child type resolves its parent from the native list.
                menu('Virtual networks')
                page.get_by_label('Resource group', exact=True).select_option('ui-group')
                create('ui-vnet')
                expect(page.get_by_role('button', name='ui-vnet', exact=True)).to_be_visible()
                menu('Subnets')
                expect(page.get_by_label('Virtual networks', exact=True)).to_have_value('ui-vnet')
                create('frontend')
                expect(page.get_by_role('button', name='frontend', exact=True)).to_be_visible()
                assert network.subnets.get('ui-group', 'ui-vnet', 'frontend').address_prefix == '10.0.0.0/24'
                create('overlap')
                expect(page.get_by_role('dialog').get_by_role('alert')).to_contain_text('overlaps')
                page.get_by_role('dialog').get_by_role('button', name='Cancel', exact=True).click()
                audit('subnets')
                # At 390 px the type menu collapses behind a keyboard-operable disclosure.
                page.set_viewport_size({'width': 390, 'height': 1000})
                toggle = page.get_by_role('button', name='Resource types: Subnets', exact=True)
                expect(toggle).to_have_attribute('aria-expanded', 'false')
                expect(page.get_by_role('complementary', name='Resource types').get_by_role('button', name='Key vaults', exact=True)).to_be_hidden()
                toggle.focus()
                page.keyboard.press('Enter')
                expect(toggle).to_have_attribute('aria-expanded', 'true')
                audit('subnets-menu')
                page.set_viewport_size({'width': 390, 'height': 1000})
                page.get_by_role('complementary', name='Resource types').get_by_role('button', name='Subnets', exact=True).click()
                expect(toggle).to_have_attribute('aria-expanded', 'false')
                page.set_viewport_size({'width': 1440, 'height': 1000})
                # Deleting the resource group cascades to its network resources.
                menu('Resource groups')
                page.get_by_label('Select ui-group').check()
                page.get_by_role('button', name='Delete', exact=True).click()
                expect(page.get_by_text('This also permanently deletes every resource it contains.')).to_be_visible()
                page.get_by_role('button', name='Confirm delete', exact=True).click()
                expect(page.get_by_role('status').filter(has_text='Resource deleted.')).to_be_visible()
                assert not resources.resource_groups.check_existence('ui-group')
                assert list(network.virtual_networks.list_all()) == []
                # Japanese presentation keeps identifiers and native state.
                page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                expect(page.get_by_role('heading', level=1, name='リソース グループ')).to_be_visible()
                expect(page.get_by_role('link', name='sdk-group', exact=True)).to_be_visible()
                audit('japanese')
                page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                # The served reference lists every reviewed provider document.
                docs = context.new_page()
                docs.goto(team.endpoint + '/_simulator/docs/azure-catalog.html')
                expect(docs.get_by_role('heading', level=1, name=re.compile(r'\(Microsoft\.\w+, simulator catalog\)$'))).to_be_visible(timeout=20000)
                index = docs.evaluate("fetch('/_simulator/docs/azure-catalog/index.json').then(r=>r.json())")
                capabilities = team.control('GET', '/_simulator/v1/capabilities')['azureCatalog']
                assert {s['model'] for s in index} == {p['namespace'] for p in capabilities['providers']}
                assert capabilities['reference'] == '/_simulator/docs/azure-catalog.html'
                document = docs.evaluate("fetch('/_simulator/docs/azure-catalog/Microsoft.KeyVault.json').then(r=>r.json())")
                assert document['servers'][0]['url'] == '/_simulator/providers/azure-arm'
                assert any(path.endswith('/vaults/{vaultName}') for path in document['paths'])
                browser.close()
            resources.resource_groups.begin_delete('sdk-group').result()
            assert list(resources.resource_groups.list()) == []
        finally:
            team.stop()
    assert not outside and not errors, (outside, errors)
    assert {'GET', 'PUT', 'DELETE'} <= methods, methods
    print(json.dumps({'azureCatalogConsole': True, 'requests': sorted(methods), 'externalRequests': 0, 'axeWidths': [1440, 390]}))


if __name__ == '__main__':
    run()
