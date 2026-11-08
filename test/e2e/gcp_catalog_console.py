"""GCP catalog console and served reference against the real binary.

The browser signs in with a real local token and uses only same-origin
requests to the actual Google API paths. The official discovery client sees
the same SQLite state. Desktop and 390 px layouts are checked with axe; no
Google Cloud console screen is claimed.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
from urllib.parse import urlsplit

from googleapiclient.errors import HttpError
from playwright.sync_api import sync_playwright, expect
from reflow import assert_reflow
from aws_catalog import Team
from gcp_catalog_fixtures import client

REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
PROJECT = 'projects/tenkacloud-local/locations/us-central1'
NETWORK = 'projects/tenkacloud-local/global/networks/default'
SINK_DESTINATION = 'logging.googleapis.com/projects/tenkacloud-local/locations/global/buckets/app'


def descriptors_of(team):
    return team.control('GET', team.control('GET', '/_simulator/v1/capabilities')['gcpCatalog']['descriptors'])['apis']


def run():
    REPORTS.mkdir(parents=True, exist_ok=True)
    axe_path = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((axe_path.parent / 'provenance.json').read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance['bundleSha256']
    outside, errors, methods = [], [], set()
    with tempfile.TemporaryDirectory(prefix='tcsim-gcp-ui-') as directory:
        team = Team(directory)
        team.start()
        try:
            alloydb = client(team, {'api': 'alloydb.v1'}).projects().locations()
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
                    if urlsplit(request.url).path.startswith('/_simulator/providers/gcp/alloydb/'):
                        methods.add(request.method)

                page.on('request', observe)

                def audit(label):
                    for width in (1440, 390):
                        page.set_viewport_size({'width': width, 'height': 1000})
                        assert_reflow(page)
                        page.evaluate(axe_path.read_text())
                        result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                        assert not result['violations'], [item['id'] for item in result['violations']]
                        page.screenshot(path=str(REPORTS / f'gcp-catalog-{label}-{width}.png'), animations='disabled')
                    page.set_viewport_size({'width': 1440, 'height': 1000})

                page.goto(team.endpoint + '/_simulator/console/')
                page.get_by_label('Access token', exact=True).fill(team.token)
                page.get_by_role('button', name='Open sandbox', exact=True).click()
                page.get_by_role('button', name='Google Cloud', exact=True).click()
                page.get_by_role('button', name='Services', exact=True).click()
                page.get_by_role('menuitem', name='Service catalog', exact=True).click()
                expect(page.get_by_role('heading', level=1, name='Google Cloud service catalog')).to_be_visible()
                page.get_by_label('clusterId', exact=True).fill('ui-cluster')
                page.get_by_label(re.compile('^network')).fill(NETWORK)
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Resource created.')
                expect(page.get_by_role('button', name='ui-cluster', exact=True)).to_be_visible()
                clusters = alloydb.clusters().list(parent=PROJECT).execute(num_retries=0)['clusters']
                assert [c['name'].rsplit('/', 1)[1] for c in clusters] == ['ui-cluster'], clusters
                audit('alloydb-clusters')
                # A duplicate keeps the google.rpc.Status message visible.
                page.get_by_label('clusterId', exact=True).fill('ui-cluster')
                page.get_by_label(re.compile('^network')).fill(NETWORK)
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('alert')).to_contain_text('already exists')
                # SDK writes appear after a refresh of the same native list.
                alloydb.clusters().create(parent=PROJECT, clusterId='sdk-cluster', body={'network': NETWORK}).execute(num_retries=0)
                page.get_by_role('button', name='Refresh', exact=True).click()
                expect(page.get_by_role('button', name='sdk-cluster', exact=True)).to_be_visible()
                page.get_by_role('button', name='ui-cluster', exact=True).click()
                expect(page.locator('pre')).to_contain_text('READY')
                page.get_by_role('button', name='Back to list').click()
                # A child collection resolves its parent from the native list.
                page.get_by_label('Resource type').select_option('instances')
                # Choosing another parent keeps the choice and lists its children.
                page.get_by_label('clusters', exact=True).select_option(label='sdk-cluster')
                expect(page.get_by_label('clusters', exact=True)).to_have_value(PROJECT + '/clusters/sdk-cluster')
                expect(page.get_by_text('No resources')).to_be_visible()
                page.get_by_label('clusters', exact=True).select_option(label='ui-cluster')
                expect(page.get_by_label('clusters', exact=True)).to_have_value(PROJECT + '/clusters/ui-cluster')
                page.get_by_label('instanceId', exact=True).fill('primary')
                page.get_by_label(re.compile('^instanceType')).select_option('PRIMARY')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('button', name='primary', exact=True)).to_be_visible()
                instances = alloydb.clusters().instances().list(parent=PROJECT + '/clusters/ui-cluster').execute(num_retries=0)['instances']
                assert [i['instanceType'] for i in instances] == ['PRIMARY'], instances
                audit('alloydb-instances')
                # A parent with children refuses deletion without force.
                page.get_by_label('Resource type').select_option('clusters')
                page.get_by_role('button', name='Delete ui-cluster').click()
                page.get_by_role('button', name='Cancel', exact=True).click()
                page.get_by_role('button', name='Delete ui-cluster').click()
                page.get_by_role('button', name='Confirm delete ui-cluster').click()
                expect(page.get_by_role('alert')).to_be_visible()
                assert len(alloydb.clusters().list(parent=PROJECT).execute(num_retries=0)['clusters']) == 2
                page.get_by_label('Resource type').select_option('instances')
                page.get_by_label('clusters', exact=True).select_option(label='ui-cluster')
                page.get_by_role('button', name='Delete primary').click()
                page.get_by_role('button', name='Confirm delete primary').click()
                expect(page.get_by_role('status')).to_have_text('Resource deleted.')
                page.get_by_label('Resource type').select_option('clusters')
                page.get_by_role('button', name='Delete ui-cluster').click()
                page.get_by_role('button', name='Confirm delete ui-cluster').click()
                expect(page.get_by_role('status')).to_have_text('Resource deleted.')
                assert [c['name'].rsplit('/', 1)[1] for c in alloydb.clusters().list(parent=PROJECT).execute(num_retries=0)['clusters']] == ['sdk-cluster']
                # A body-ID, short-name collection: the ID travels in the
                # create body and listed names are bare IDs under the project.
                logging = client(team, {'api': 'logging.v2'}).projects()
                page.get_by_label('API', exact=True).select_option(label='Cloud Logging')
                expect(page.get_by_label('Resource type')).to_have_value('sinks')
                expect(page.get_by_label('Location', exact=True)).to_have_count(0)
                page.get_by_label('name', exact=True).fill('ui-sink')
                page.get_by_label(re.compile('^destination')).fill(SINK_DESTINATION)
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Resource created.')
                sinks = logging.sinks().list(parent='projects/tenkacloud-local').execute(num_retries=0)['sinks']
                assert [(s['name'], s['destination']) for s in sinks] == [('ui-sink', SINK_DESTINATION)], sinks
                page.get_by_role('button', name='ui-sink', exact=True).click()
                expect(page.locator('pre')).to_contain_text(SINK_DESTINATION)
                page.get_by_role('button', name='Back to list').click()
                audit('logging-sinks')
                page.get_by_role('button', name='Delete ui-sink').click()
                page.get_by_role('button', name='Confirm delete ui-sink').click()
                expect(page.get_by_role('status')).to_have_text('Resource deleted.')
                assert not logging.sinks().list(parent='projects/tenkacloud-local').execute(num_retries=0).get('sinks')
                # A project-scope collection whose official path names a location.
                page.get_by_label('Resource type').select_option('buckets')
                page.get_by_label('Location', exact=True).select_option('global')
                page.get_by_label('bucketId', exact=True).fill('ui-logs')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('button', name='ui-logs', exact=True)).to_be_visible()
                buckets = logging.locations().buckets().list(parent='projects/tenkacloud-local/locations/global').execute(num_retries=0)['buckets']
                assert [b['name'] for b in buckets] == ['projects/tenkacloud-local/locations/global/buckets/ui-logs'], buckets
                # A zonal collection lists and creates in the chosen zone.
                ids = client(team, {'api': 'ids.v1'}).projects().locations()
                page.get_by_label('API', exact=True).select_option(label='Cloud IDS')
                expect(page.get_by_label('Zone', exact=True)).to_have_value('us-central1-a')
                page.get_by_label('Zone', exact=True).select_option('us-central1-b')
                page.get_by_label('endpointId', exact=True).fill('ui-ids')
                page.get_by_label(re.compile('^network')).fill(NETWORK)
                page.get_by_label(re.compile('^severity')).select_option('HIGH')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('button', name='ui-ids', exact=True)).to_be_visible()
                endpoints = ids.endpoints().list(parent='projects/tenkacloud-local/locations/us-central1-b').execute(num_retries=0)['endpoints']
                assert [e['name'] for e in endpoints] == ['projects/tenkacloud-local/locations/us-central1-b/endpoints/ui-ids'], endpoints
                assert not ids.endpoints().list(parent='projects/tenkacloud-local/locations/us-central1-a').execute(num_retries=0).get('endpoints')
                audit('ids-endpoints')
                # A flat API: global paths without a location, a body ID and an
                # explicit false boolean.
                compute_api = next(a for a in descriptors_of(team) if a['name'] == 'compute')
                compute = client(team, {'api': 'compute.v1', 'endpoint': compute_api['endpoint']})
                page.get_by_label('API', exact=True).select_option(label='Compute Engine')
                expect(page.get_by_label('Resource type')).to_have_value('networks')
                page.get_by_label('name', exact=True).fill('ui-vpc')
                page.get_by_label(re.compile('^autoCreateSubnetworks')).select_option('false')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('button', name='ui-vpc', exact=True)).to_be_visible()
                assert [n['name'] for n in compute.networks().list(project='tenkacloud-local').execute(num_retries=0)['items']] == ['ui-vpc']
                page.get_by_label('Resource type').select_option('disks')
                expect(page.get_by_label('Zone', exact=True)).to_be_visible()
                # Implicit parents and wrapped creates are listed, with a note
                # where the form cannot create them.
                page.get_by_label('API', exact=True).select_option(label='Firestore')
                page.get_by_label('Resource type').select_option('indexes')
                expect(page.get_by_text(re.compile('cannot create them'))).to_be_visible()
                expect(page.get_by_label('collectionGroups ID', exact=True)).to_be_visible()
                page.get_by_label('API', exact=True).select_option(label='Spanner')
                expect(page.get_by_text(re.compile('needs instance, which the form cannot collect'))).to_be_visible()
                expect(page.get_by_role('button', name='Create', exact=True)).to_have_count(0)
                audit('spanner-read-only')
                page.get_by_label('API', exact=True).select_option(label='AlloyDB for PostgreSQL')
                expect(page.get_by_role('button', name='sdk-cluster', exact=True)).to_be_visible()
                # Japanese presentation keeps identifiers and native state.
                page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                expect(page.get_by_role('heading', level=1, name='Google Cloud サービスカタログ')).to_be_visible()
                expect(page.get_by_role('button', name='sdk-cluster', exact=True)).to_be_visible()
                page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                # The served reference lists every reviewed Google API document.
                docs = context.new_page()
                docs.goto(team.endpoint + '/_simulator/docs/gcp-catalog.html')
                expect(docs.get_by_text('AlloyDB for PostgreSQL (simulator catalog)').first).to_be_visible(timeout=20000)
                index = docs.evaluate("fetch('/_simulator/docs/gcp-catalog/index.json').then(r=>r.json())")
                capabilities = team.control('GET', '/_simulator/v1/capabilities')['gcpCatalog']
                descriptors = team.control('GET', capabilities['descriptors'])['apis']
                assert {s['model'] for s in index} == {f"{a['name']}.{a['version']}" for a in capabilities['apis']} == {f"{a['name']}.{a['version']}" for a in descriptors}
                assert capabilities['reference'] == '/_simulator/docs/gcp-catalog.html'
                document = docs.evaluate("fetch('/_simulator/docs/gcp-catalog/alloydb.v1.json').then(r=>r.json())")
                assert document['servers'][0]['url'] == '/_simulator/providers/gcp/alloydb'
                assert any(path.endswith('/clusters') for path in document['paths'])
                browser.close()
            alloydb.clusters().delete(name=PROJECT + '/clusters/sdk-cluster').execute(num_retries=0)
            try:
                alloydb.clusters().get(name=PROJECT + '/clusters/sdk-cluster').execute(num_retries=0)
                raise AssertionError('deleted cluster still readable')
            except HttpError as error:
                assert error.resp.status == 404
        finally:
            team.stop()
    assert not outside and not errors, (outside, errors)
    assert {'GET', 'POST', 'DELETE'} <= methods, methods
    print(json.dumps({'gcpCatalogConsole': True, 'requests': sorted(methods), 'externalRequests': 0, 'axeWidths': [1440, 390]}))


if __name__ == '__main__':
    run()
