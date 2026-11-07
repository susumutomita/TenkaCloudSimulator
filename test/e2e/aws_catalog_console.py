"""Catalog console and served reference against the real binary and boto3.

The browser signs in with a real local token, uses only same-origin native
requests, and the SDK observes the same SQLite state. Desktop and 390 px
layouts are checked with axe; no AWS console screen is claimed.
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
from aws_catalog import Team, refused

REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()


def run():
    REPORTS.mkdir(parents=True, exist_ok=True)
    axe_path = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((axe_path.parent / 'provenance.json').read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance['bundleSha256']
    outside, errors, operations = [], [], set()
    with tempfile.TemporaryDirectory(prefix='tcsim-catalog-ui-') as directory:
        team = Team(directory)
        team.start()
        try:
            registry, hosting = team.client('ecr'), team.client('amplify')
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
                    target = request.headers.get('x-amz-target', '')
                    if target:
                        operations.add(target.rsplit('.', 1)[1])
                    elif request.headers.get('x-simulator-service'):
                        operations.add(request.method + ' ' + urlsplit(request.url).path.split('/')[1])

                page.on('request', observe)

                def audit(label):
                    for width in (1440, 390):
                        page.set_viewport_size({'width': width, 'height': 1000})
                        assert_reflow(page)
                        page.evaluate(axe_path.read_text())
                        result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                        assert not result['violations'], [item['id'] for item in result['violations']]
                        page.screenshot(path=str(REPORTS / f'catalog-{label}-{width}.png'), animations='disabled')
                    page.set_viewport_size({'width': 1440, 'height': 1000})

                page.goto(team.endpoint + '/_simulator/console/')
                page.get_by_label('Access token', exact=True).fill(team.token)
                page.get_by_role('button', name='Open sandbox', exact=True).click()
                page.get_by_role('button', name='Services', exact=True).click()
                page.get_by_role('menuitem', name='Service catalog', exact=True).click()
                expect(page.get_by_role('heading', level=1, name='AWS service catalog')).to_be_visible()
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon Elastic Container Registry')).click()
                page.get_by_label('repositoryName', exact=True).fill('console/web')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_text('Resource created.')).to_be_visible()
                expect(page.get_by_role('link', name='console/web')).to_be_visible()
                assert [r['repositoryName'] for r in registry.describe_repositories()['repositories']] == ['console/web']
                audit('ecr-list')
                page.get_by_role('link', name='console/web').click()
                expect(page.get_by_text('000000000000.dkr.ecr.us-east-1.amazonaws.com/console/web')).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                registry.create_repository(repositoryName='sdk/made')
                page.get_by_role('button', name='Refresh').click()
                expect(page.get_by_role('link', name='sdk/made')).to_be_visible()
                page.get_by_role('button', name='Delete console/web').click()
                page.get_by_role('dialog').get_by_role('button', name='Cancel').click()
                assert len(registry.describe_repositories()['repositories']) == 2, 'cancel deleted state'
                page.get_by_role('button', name='Delete console/web').click()
                page.get_by_role('dialog').get_by_role('button', name='Delete').click()
                expect(page.get_by_text('Resource deleted.')).to_be_visible()
                assert [r['repositoryName'] for r in registry.describe_repositories()['repositories']] == ['sdk/made']
                page.get_by_label('repositoryName', exact=True).fill('sdk/made')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_text('The repository sdk/made already exists.', exact=False)).to_be_visible()
                # Wire names: Amazon MQ serializes lowerCamel keys. The console
                # must send and read them as SDKs do.
                broker = team.client('mq')
                broker.create_configuration(Name='sdk-config', EngineType='ACTIVEMQ', EngineVersion='5.18')
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon MQ')).click()
                expect(page.get_by_role('link', name='sdk-config')).to_be_visible()
                page.get_by_label('Name', exact=True).fill('console-config')
                page.get_by_role('button', name=re.compile('^EngineType')).click()
                page.get_by_role('option', name='ACTIVEMQ', exact=True).click()
                page.get_by_label(re.compile('^EngineVersion')).fill('5.18')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('link', name='console-config')).to_be_visible()
                configurations = broker.list_configurations()['Configurations']
                assert sorted(c['Name'] for c in configurations) == ['console-config', 'sdk-config'], configurations
                for configuration in configurations:
                    broker.delete_configuration(ConfigurationId=configuration['Id'])
                # Identifier-only lists: ECS ListClusters and ListTaskDefinitions
                # return ARNs; each opens through its describe operation.
                containers = team.client('ecs')
                containers.create_cluster(clusterName='sdk-cluster')
                task = containers.register_task_definition(
                    family='sdk-web', requiresCompatibilities=['FARGATE'], networkMode='awsvpc', cpu='256', memory='512',
                    containerDefinitions=[{'name': 'web', 'image': 'public.ecr.aws/nginx/nginx:latest', 'essential': True}])['taskDefinition']['taskDefinitionArn']
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon Elastic Container Service')).click()
                cluster = containers.describe_clusters(clusters=['sdk-cluster'])['clusters'][0]['clusterArn']
                expect(page.get_by_role('link', name=cluster)).to_be_visible()
                page.get_by_role('link', name=cluster).click()
                expect(page.get_by_text('sdk-cluster', exact=True).first).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                page.get_by_role('tab', name='taskDefinition').click()
                expect(page.get_by_role('link', name=task)).to_be_visible()
                page.get_by_role('link', name=task).click()
                expect(page.get_by_text('sdk-web', exact=True).first).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                containers.deregister_task_definition(taskDefinition=task)
                containers.delete_cluster(cluster='sdk-cluster')
                # Summary identity keys: Athena lists DataCatalogSummary.CatalogName.
                queries = team.client('athena')
                queries.create_data_catalog(Name='sdk_catalog', Type='GLUE', Parameters={'catalog-id': '000000000000'})
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon Athena')).click()
                page.get_by_role('tab', name='dataCatalog').click()
                expect(page.get_by_role('link', name='sdk_catalog')).to_be_visible()
                page.get_by_role('link', name='sdk_catalog').click()
                expect(page.get_by_text('GLUE', exact=True).first).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                queries.delete_data_catalog(Name='sdk_catalog')
                # ARN-only parents: Cloud Directory facets live under schema ARNs.
                directory = team.client('clouddirectory')
                schema = directory.create_schema(Name='sdk_org')['SchemaArn']
                directory.create_facet(SchemaArn=schema, Name='person', ObjectType='LEAF_NODE', FacetStyle='DYNAMIC')
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon Cloud Directory')).click()
                page.get_by_role('tab', name='facet').click()
                expect(page.get_by_role('link', name='person')).to_be_visible()
                directory.delete_facet(SchemaArn=schema, Name='person')
                directory.delete_schema(SchemaArn=schema)
                # Lookups send the identity the member accepts (AppConfig IDs), and
                # REST payload outputs open unwrapped (App Mesh meshes).
                configs = team.client('appconfig')
                application = configs.create_application(Name='sdk-app')['Id']
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^AWS AppConfig')).click()
                page.get_by_role('tab', name='application').click()
                page.get_by_role('link', name='sdk-app').click()
                expect(page.get_by_text(application, exact=True).first).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                page.get_by_role('button', name='Delete sdk-app').click()
                page.get_by_role('dialog').get_by_role('button', name='Delete').click()
                expect(page.get_by_text('Resource deleted.')).to_be_visible()
                assert configs.list_applications()['Items'] == []
                meshes = team.client('appmesh')
                meshes.create_mesh(meshName='sdk-mesh')
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^AWS App Mesh')).click()
                page.get_by_role('tab', name='mesh').click()
                page.get_by_role('link', name='sdk-mesh').click()
                expect(page.get_by_text('sdk-mesh', exact=True).first).to_be_visible()
                expect(page.get_by_text('No details', exact=False)).to_have_count(0)
                page.get_by_role('button', name='Back to list').click()
                meshes.delete_mesh(meshName='sdk-mesh')
                # A REST-JSON parent and child through the same generic view.
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^AWS Amplify')).click()
                page.get_by_label('name', exact=True).fill('console-site')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('link', name='console-site')).to_be_visible()
                page.get_by_role('tab', name='branch').click()
                page.get_by_label('branchName', exact=True).fill('main')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('link', name='main')).to_be_visible()
                app = hosting.list_apps()['apps'][0]
                assert [b['branchName'] for b in hosting.list_branches(appId=app['appId'])['branches']] == ['main']
                audit('amplify-branch')
                # Choosing another parent loads its children and keeps the choice.
                second = hosting.create_app(name='sdk-site')['app']
                page.get_by_role('button', name='Refresh').click()
                page.get_by_role('button', name=re.compile('^app ')).click()
                page.get_by_role('option', name='sdk-site').click()
                expect(page.get_by_role('link', name='main')).to_have_count(0)
                page.get_by_label('branchName', exact=True).fill('dev')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_role('link', name='dev')).to_be_visible()
                assert [b['branchName'] for b in hosting.list_branches(appId=second['appId'])['branches']] == ['dev']
                assert [b['branchName'] for b in hosting.list_branches(appId=app['appId'])['branches']] == ['main']
                hosting.delete_branch(appId=second['appId'], branchName='dev')
                hosting.delete_app(appId=second['appId'])
                page.get_by_role('tab', name='app').click()
                page.get_by_role('button', name='Delete console-site').click()
                page.get_by_role('dialog').get_by_role('button', name='Delete').click()
                expect(page.get_by_text('dependent resources', exact=False)).to_be_visible()
                assert len(hosting.list_apps()['apps']) == 1
                # Japanese presentation keeps identifiers and native state.
                page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                expect(page.get_by_role('heading', level=1, name='AWS サービスカタログ')).to_be_visible()
                page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                # The served reference lists every catalog service document.
                docs = context.new_page()
                docs.goto(team.endpoint + '/_simulator/docs/catalog.html')
                index = docs.evaluate("fetch('/_simulator/docs/catalog/index.json').then(r=>r.json())")
                # The first indexed service is the default source of the multi-source page.
                expect(docs.get_by_text(index[0]['title'] + ' (simulator catalog)').first).to_be_visible(timeout=20000)
                assert {'amplify', 'codeconnections', 'ecr', 'stepfunctions'} <= {s['model'] for s in index}
                document = docs.evaluate("fetch('/_simulator/docs/catalog/ecr.json').then(r=>r.json())")
                targets = document['paths']['/']['post']['parameters'][0]['schema']['enum']
                assert 'AmazonEC2ContainerRegistry_V20150921.CreateRepository' in targets
                browser.close()
            refused('BadRequestException', lambda: hosting.delete_app(appId=app['appId']))
            hosting.delete_branch(appId=app['appId'], branchName='main')
            hosting.delete_app(appId=app['appId'])
            registry.delete_repository(repositoryName='sdk/made')
        finally:
            team.stop()
    assert not outside and not errors, (outside, errors)
    assert {'CreateRepository', 'DescribeRepositories', 'DeleteRepository'} <= operations, operations
    print(json.dumps({'catalogConsole': True, 'operations': sorted(operations), 'externalRequests': 0, 'axeWidths': [1440, 390]}))


if __name__ == '__main__':
    run()
