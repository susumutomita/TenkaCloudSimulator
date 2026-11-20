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
from urllib.parse import parse_qs, urlsplit

from playwright.sync_api import sync_playwright, expect
from reflow import assert_reflow
from aws_catalog import Team, refused

REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
# Optional directory for the two review screenshots (RDS list, reference Send).
SHOTS = os.environ.get('CATALOG_SHOTS')


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
                    elif request.headers.get('content-type', '').startswith('application/x-www-form-urlencoded') and request.post_data:
                        # Query protocol: the Action field names the operation.
                        operations.update(parse_qs(request.post_data).get('Action', []))
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
                # Fixed list inputs: Bedrock lists application profiles only.
                models = team.client('bedrock')
                profile = models.create_inference_profile(inferenceProfileName='sdk-profile', modelSource={'copyFrom': 'arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-v2'})['inferenceProfileArn']
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon Bedrock(?! AgentCore)')).click()
                page.get_by_role('tab', name='inferenceProfile').click()
                expect(page.get_by_role('link', name='sdk-profile')).to_be_visible()
                page.get_by_role('link', name='sdk-profile').click()
                expect(page.get_by_text(profile, exact=True).first).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                models.delete_inference_profile(inferenceProfileIdentifier=profile)
                # Account inputs: Glacier sends the caller account "-", and
                # Budgets sends the simulator account as AccountId.
                vaults = team.client('glacier')
                vaults.create_vault(accountId='-', vaultName='sdk-vault')
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon S3 Glacier')).click()
                page.get_by_role('tab', name='vault').click()
                expect(page.get_by_role('link', name='sdk-vault')).to_be_visible()
                page.get_by_role('link', name='sdk-vault').click()
                expect(page.get_by_text('arn:aws:glacier:us-east-1:000000000000:vaults/sdk-vault', exact=True).first).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                page.get_by_role('button', name='Delete sdk-vault').click()
                page.get_by_role('dialog').get_by_role('button', name='Delete').click()
                expect(page.get_by_text('Resource deleted.')).to_be_visible()
                assert vaults.list_vaults(accountId='-')['VaultList'] == []
                spending = team.client('budgets')
                spending.create_budget(AccountId='000000000000', Budget={'BudgetName': 'sdk-budget', 'BudgetType': 'COST', 'TimeUnit': 'MONTHLY', 'BudgetLimit': {'Amount': '10', 'Unit': 'USD'}})
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^AWS Budgets')).click()
                page.get_by_role('tab', name='budget').click()
                expect(page.get_by_role('link', name='sdk-budget')).to_be_visible()
                page.get_by_role('link', name='sdk-budget').click()
                expect(page.get_by_text('MONTHLY', exact=True).first).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                spending.delete_budget(AccountId='000000000000', BudgetName='sdk-budget')
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
                # Query protocol: RDS sends botocore's form bodies and reads XML.
                databases = team.client('rds')
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon Relational Database Service')).click()
                expect(page.get_by_role('tab', name='dbSubnetGroup')).to_be_visible()
                page.get_by_label('DBSubnetGroupName', exact=True).fill('Console-Subnets')
                page.get_by_label('DBSubnetGroupDescription', exact=True).fill('console subnets')
                page.get_by_label('SubnetIds', exact=True).fill('subnet-0a1b2c3d, subnet-4e5f6a7b')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_text('Resource created.')).to_be_visible()
                expect(page.get_by_role('link', name='console-subnets')).to_be_visible()
                groups = databases.describe_db_subnet_groups()['DBSubnetGroups']
                assert [(g['DBSubnetGroupName'], [s['SubnetIdentifier'] for s in g['Subnets']]) for g in groups] == [('console-subnets', ['subnet-0a1b2c3d', 'subnet-4e5f6a7b'])], groups
                audit('rds-list')
                if SHOTS:
                    Path(SHOTS).mkdir(parents=True, exist_ok=True)
                    page.screenshot(path=str(Path(SHOTS) / 'console-rds-list.png'), animations='disabled')
                databases.add_tags_to_resource(ResourceName=groups[0]['DBSubnetGroupArn'], Tags=[{'Key': 'stage', 'Value': 'console-dev'}])
                page.get_by_role('link', name='console-subnets').click()
                expect(page.get_by_text(groups[0]['DBSubnetGroupArn'], exact=True).first).to_be_visible()
                expect(page.get_by_text('subnet-4e5f6a7b', exact=False).first).to_be_visible()
                expect(page.get_by_text('console-dev', exact=False).first).to_be_visible()
                page.get_by_role('button', name='Back to list').click()
                # Service faults keep their official code and message.
                page.get_by_label('DBSubnetGroupName', exact=True).fill('console-subnets')
                page.get_by_label('DBSubnetGroupDescription', exact=True).fill('again')
                page.get_by_label('SubnetIds', exact=True).fill('subnet-0a1b2c3d, subnet-4e5f6a7b')
                page.get_by_role('button', name='Create', exact=True).click()
                expect(page.get_by_text('DBSubnetGroupAlreadyExists: ', exact=False)).to_be_visible()
                page.get_by_role('button', name='Delete console-subnets').click()
                page.get_by_role('dialog').get_by_role('button', name='Delete').click()
                expect(page.get_by_text('Resource deleted.')).to_be_visible()
                assert databases.describe_db_subnet_groups()['DBSubnetGroups'] == []
                # Every other RDS kind: create, list, open with tags, delete.
                for kind, values, name, arn_key, describe in [
                        ('dbParameterGroup', {'DBParameterGroupName': 'Console-Params', 'DBParameterGroupFamily': 'mysql8.0', 'Description': 'console'}, 'console-params', 'DBParameterGroupArn',
                         lambda: databases.describe_db_parameter_groups()['DBParameterGroups']),
                        ('dbClusterParameterGroup', {'DBClusterParameterGroupName': 'console-cluster', 'DBParameterGroupFamily': 'aurora-postgresql16', 'Description': 'console'}, 'console-cluster', 'DBClusterParameterGroupArn',
                         lambda: databases.describe_db_cluster_parameter_groups()['DBClusterParameterGroups']),
                        ('optionGroup', {'OptionGroupName': 'console-options', 'EngineName': 'mysql', 'MajorEngineVersion': '8.0', 'OptionGroupDescription': 'console'}, 'console-options', 'OptionGroupArn',
                         lambda: databases.describe_option_groups()['OptionGroupsList'])]:
                    page.get_by_role('tab', name=kind, exact=True).click()
                    for label, value in values.items():
                        page.get_by_label(label, exact=True).fill(value)
                    page.get_by_role('button', name='Create', exact=True).click()
                    expect(page.get_by_role('link', name=name)).to_be_visible()
                    created = describe()
                    assert len(created) == 1 and created[0][arn_key].endswith(':' + name), created
                    databases.add_tags_to_resource(ResourceName=created[0][arn_key], Tags=[{'Key': 'kind', 'Value': kind}])
                    page.get_by_role('link', name=name).click()
                    expect(page.get_by_text(created[0][arn_key], exact=True).first).to_be_visible()
                    expect(page.get_by_role('cell', name=kind, exact=True)).to_be_visible()
                    page.get_by_role('button', name='Back to list').click()
                    page.get_by_role('button', name='Delete ' + name).click()
                    page.get_by_role('dialog').get_by_role('button', name='Delete').click()
                    expect(page.get_by_text('Resource deleted.')).to_be_visible()
                    assert describe() == [], kind
                # Generic edit, tag and complex-create controls. Each write is
                # the service's native request; boto3 reads the result back.
                create_form = page.locator('form').filter(has=page.get_by_role('button', name='Create', exact=True))
                edit_form = page.locator('form').filter(has=page.get_by_role('button', name='Save changes', exact=True))

                def create(values):
                    for label, value in values.items():
                        create_form.get_by_label(label, exact=True).fill(value)
                    create_form.get_by_role('button', name='Create', exact=True).click()

                def edit(values):
                    page.get_by_role('button', name='Edit', exact=True).click()
                    for label, value in values.items():
                        edit_form.get_by_label(label, exact=True).fill(value)

                def save():
                    edit_form.get_by_role('button', name='Save changes', exact=True).click()
                    expect(page.get_by_text('Resource updated.')).to_be_visible()

                def add_tag(key, value):
                    page.get_by_label('Tag key', exact=True).fill(key)
                    page.get_by_label('Tag value (optional)', exact=True).fill(value)
                    page.get_by_role('button', name='Add tag', exact=True).click()
                    expect(page.get_by_text('Tag added.')).to_be_visible()

                def remove_tag(key):
                    page.get_by_role('button', name='Remove tag ' + key, exact=True).click()
                    expect(page.get_by_text('Tag removed.')).to_be_visible()

                def delete(name):
                    page.get_by_role('button', name='Back to list').click()
                    page.get_by_role('button', name='Delete ' + name).click()
                    page.get_by_role('dialog').get_by_role('button', name='Delete').click()
                    expect(page.get_by_text('Resource deleted.')).to_be_visible()

                shots = Path(SHOTS) if SHOTS else None
                # RDS (Query): Tags at creation, ModifyDBSubnetGroup, AddTagsToResource and RemoveTagsFromResource.
                page.get_by_role('tab', name='dbSubnetGroup', exact=True).click()
                create({'DBSubnetGroupName': 'edit-subnets', 'DBSubnetGroupDescription': 'before', 'SubnetIds': 'subnet-0a1b2c3d, subnet-4e5f6a7b',
                        'Tags (optional)': 'team=console\nstage=dev'})
                expect(page.get_by_role('link', name='edit-subnets')).to_be_visible()
                arn = databases.describe_db_subnet_groups(DBSubnetGroupName='edit-subnets')['DBSubnetGroups'][0]['DBSubnetGroupArn']
                assert databases.list_tags_for_resource(ResourceName=arn)['TagList'] == [{'Key': 'stage', 'Value': 'dev'}, {'Key': 'team', 'Value': 'console'}]
                page.get_by_role('link', name='edit-subnets').click()
                expect(page.get_by_role('button', name='Remove tag team')).to_be_visible()
                edit({})
                expect(edit_form.get_by_label('DBSubnetGroupDescription (optional)', exact=True)).to_have_value('before')
                # A required member left empty is refused before any request.
                edit_form.get_by_role('button', name='Save changes', exact=True).click()
                expect(page.get_by_text('SubnetIds is required.')).to_be_visible()
                edit_form.get_by_label('DBSubnetGroupDescription (optional)', exact=True).fill('after')
                edit_form.get_by_label('SubnetIds', exact=True).fill('subnet-0a1b2c3d, subnet-4e5f6a7b, subnet-0123456789abcdef0')
                audit('rds-edit')
                if shots:
                    page.screenshot(path=str(shots / 'console-update.png'), animations='disabled')
                save()
                group = databases.describe_db_subnet_groups(DBSubnetGroupName='edit-subnets')['DBSubnetGroups'][0]
                assert group['DBSubnetGroupDescription'] == 'after' and len(group['Subnets']) == 3, group
                add_tag('owner', 'ui')
                remove_tag('stage')
                assert databases.list_tags_for_resource(ResourceName=arn)['TagList'] == [{'Key': 'owner', 'Value': 'ui'}, {'Key': 'team', 'Value': 'console'}]
                expect(page.get_by_role('button', name='Remove tag owner')).to_be_visible()
                expect(page.get_by_role('button', name='Remove tag stage')).to_have_count(0)
                audit('rds-tags')
                if shots:
                    page.get_by_role('button', name='Add tag', exact=True).scroll_into_view_if_needed()
                    page.screenshot(path=str(shots / 'console-tags.png'), animations='disabled')
                delete('edit-subnets')
                assert databases.describe_db_subnet_groups()['DBSubnetGroups'] == []
                # ElastiCache (Query): a user needs the AuthenticationMode structure.
                caches = team.client('elasticache')
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon ElastiCache')).click()
                page.get_by_role('tab', name='user', exact=True).click()
                user = {'UserId': 'console-user', 'UserName': 'console-user', 'Engine': 'valkey', 'AccessString': 'on ~app:* +@read',
                        'AuthenticationMode (optional)': '{"Type": "cert"}', 'Tags (optional)': 'team=cache'}
                create(user)
                expect(page.get_by_text('AuthenticationMode.Type: must be one of password, no-password-required, iam.')).to_be_visible()
                assert caches.describe_users()['Users'] == [], 'an invalid value was sent'
                create({'AuthenticationMode (optional)': '{"Type": "iam"}'})
                expect(page.get_by_role('link', name='console-user')).to_be_visible()
                made = caches.describe_users(UserId='console-user')['Users'][0]
                assert made['Authentication']['Type'] == 'iam' and made['AccessString'] == 'on ~app:* +@read', made
                assert caches.list_tags_for_resource(ResourceName=made['ARN'])['TagList'] == [{'Key': 'team', 'Value': 'cache'}]
                if shots:
                    page.screenshot(path=str(shots / 'console-elasticache-user.png'), animations='disabled')
                page.get_by_role('link', name='console-user').click()
                expect(page.get_by_role('button', name='Remove tag team')).to_be_visible()
                edit({'AccessString (optional)': 'on ~* +@all'})
                save()
                assert caches.describe_users(UserId='console-user')['Users'][0]['AccessString'] == 'on ~* +@all'
                delete('console-user')
                assert caches.describe_users()['Users'] == []
                page.get_by_role('tab', name='cacheSubnetGroup', exact=True).click()
                create({'CacheSubnetGroupName': 'console-cache', 'CacheSubnetGroupDescription': 'before', 'SubnetIds': 'subnet-0a1b2c3d'})
                page.get_by_role('link', name='console-cache').click()
                edit({'CacheSubnetGroupDescription (optional)': 'after', 'SubnetIds (optional)': 'subnet-0a1b2c3d, subnet-4e5f6a7b'})
                save()
                cache_group = caches.describe_cache_subnet_groups(CacheSubnetGroupName='console-cache')['CacheSubnetGroups'][0]
                assert cache_group['CacheSubnetGroupDescription'] == 'after' and len(cache_group['Subnets']) == 2, cache_group
                add_tag('owner', 'ui')
                assert caches.list_tags_for_resource(ResourceName=cache_group['ARN'])['TagList'] == [{'Key': 'owner', 'Value': 'ui'}]
                delete('console-cache')
                assert caches.describe_cache_subnet_groups()['CacheSubnetGroups'] == []
                # Redshift (Query): no ARN member and no list-tags operation; tags
                # come from the description and are written through the ARN.
                warehouse = team.client('redshift')
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon Redshift')).click()
                page.get_by_role('tab', name='clusterSubnetGroup', exact=True).click()
                create({'ClusterSubnetGroupName': 'console-dw', 'Description': 'before', 'SubnetIds': 'subnet-0a1b2c3d', 'Tags (optional)': 'team=dw'})
                page.get_by_role('link', name='console-dw').click()
                expect(page.get_by_role('button', name='Remove tag team')).to_be_visible()
                expect(page.get_by_text('Request failed')).to_have_count(0)
                add_tag('owner', 'ui')
                remove_tag('team')
                assert warehouse.describe_cluster_subnet_groups(ClusterSubnetGroupName='console-dw')['ClusterSubnetGroups'][0]['Tags'] == [{'Key': 'owner', 'Value': 'ui'}]
                edit({'Description (optional)': 'after', 'SubnetIds': 'subnet-0a1b2c3d, subnet-4e5f6a7b'})
                save()
                dw = warehouse.describe_cluster_subnet_groups(ClusterSubnetGroupName='console-dw')['ClusterSubnetGroups'][0]
                assert dw['Description'] == 'after' and len(dw['Subnets']) == 2, dw
                delete('console-dw')
                page.get_by_role('tab', name='clusterParameterGroup', exact=True).click()
                create({'ParameterGroupName': 'console-params', 'ParameterGroupFamily': 'redshift-1.0', 'Description': 'dw', 'Tags (optional)': 'team=dw'})
                page.get_by_role('link', name='console-params').click()
                expect(page.get_by_text('redshift-1.0', exact=True).first).to_be_visible()
                remove_tag('team')
                assert warehouse.describe_cluster_parameter_groups()['ParameterGroups'][0]['Tags'] == []
                delete('console-params')
                assert warehouse.describe_cluster_parameter_groups()['ParameterGroups'] == []
                assert warehouse.describe_cluster_subnet_groups()['ClusterSubnetGroups'] == []
                # JSON 1.1 (ECR): a structure update and a tag through the UI.
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^Amazon Elastic Container Registry')).click()
                create({'repositoryName': 'console/edit', 'tags (optional)': 'team=ecr'})
                page.get_by_role('link', name='console/edit').click()
                edit({'imageScanningConfiguration': '{"scanOnPush": true}'})
                save()
                repository = registry.describe_repositories(repositoryNames=['console/edit'])['repositories'][0]
                assert repository['imageScanningConfiguration'] == {'scanOnPush': True}, repository
                add_tag('owner', 'ui')
                assert sorted(t['Key'] for t in registry.list_tags_for_resource(resourceArn=repository['repositoryArn'])['tags']) == ['owner', 'team']
                delete('console/edit')
                # REST-JSON (Amplify): an update and map tags through the UI.
                page.get_by_role('button', name=re.compile('^Catalog service')).click()
                page.get_by_role('option', name=re.compile('^AWS Amplify')).click()
                page.get_by_role('link', name='console-site').click()
                edit({'description (optional)': 'edited in the console'})
                save()
                assert hosting.get_app(appId=app['appId'])['app']['description'] == 'edited in the console'
                add_tag('owner', 'ui')
                assert hosting.list_tags_for_resource(resourceArn=app['appArn'])['tags'] == {'owner': 'ui'}
                remove_tag('owner')
                assert hosting.list_tags_for_resource(resourceArn=app['appArn'])['tags'] == {}
                page.get_by_role('button', name='Back to list').click()
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
                # The RDS reference: each Action is runnable, and Send performs
                # the real form POST that SDK clients see the result of.
                rds_doc = docs.evaluate("fetch('/_simulator/docs/catalog/rds.json').then(r=>r.json())")
                assert '/#Action=DescribeDBSubnetGroups' in rds_doc['paths'], sorted(rds_doc['paths'])
                docs.goto(team.endpoint + '/_simulator/docs/catalog.html?api=rds')
                expect(docs.get_by_text('Amazon Relational Database Service (simulator catalog)').first).to_be_visible(timeout=20000)

                def send(action):
                    docs.get_by_text(action, exact=True).first.click()
                    docs.get_by_role('button', name=f'Test Request (post /#Action={action})', exact=True).click()
                    with docs.expect_response(lambda r: r.request.method == 'POST' and urlsplit(r.url).path == '/') as received:
                        docs.get_by_role('button', name=re.compile('^Send post request to ')).click()
                    response = received.value
                    form = parse_qs(response.request.post_data)
                    assert form['Action'] == [action] and form['Version'] == ['2014-10-31'], form
                    assert response.request.headers.get('x-simulator-service') == 'rds'
                    assert response.status == 200 and response.headers['content-type'] == 'text/xml', (response.status, response.text())
                    assert f'<{action}Response xmlns="http://rds.amazonaws.com/doc/2014-10-31/">' in response.text()
                    return response

                send('CreateDBParameterGroup')
                created = databases.describe_db_parameter_groups()['DBParameterGroups']
                assert [g['DBParameterGroupName'] for g in created] == ['example'], created
                expect(docs.get_by_text('CreateDBParameterGroupResponse', exact=False).first).to_be_visible()
                if SHOTS:
                    docs.screenshot(path=str(Path(SHOTS) / 'reference-rds-send.png'), animations='disabled')
                docs.get_by_role('button', name='Close Client', exact=True).click()
                assert '<DBParameterGroupName>example</DBParameterGroupName>' in send('DescribeDBParameterGroups').text()
                docs.get_by_role('button', name='Close Client', exact=True).click()
                databases.delete_db_parameter_group(DBParameterGroupName='example')
                browser.close()
            refused('BadRequestException', lambda: hosting.delete_app(appId=app['appId']))
            hosting.delete_branch(appId=app['appId'], branchName='main')
            hosting.delete_app(appId=app['appId'])
            registry.delete_repository(repositoryName='sdk/made')
        finally:
            team.stop()
    assert not outside and not errors, (outside, errors)
    assert {'CreateRepository', 'DescribeRepositories', 'DeleteRepository'} <= operations, operations
    assert {'ModifyDBSubnetGroup', 'AddTagsToResource', 'RemoveTagsFromResource', 'CreateUser', 'ModifyUser', 'ModifyCacheSubnetGroup',
            'ModifyClusterSubnetGroup', 'CreateTags', 'DeleteTags', 'PutImageScanningConfiguration', 'TagResource'} <= operations, operations
    assert {'CreateDBSubnetGroup', 'DescribeDBSubnetGroups', 'DeleteDBSubnetGroup', 'ListTagsForResource', 'CreateDBParameterGroup', 'DeleteDBParameterGroup',
            'CreateDBClusterParameterGroup', 'DescribeDBClusterParameterGroups', 'DeleteDBClusterParameterGroup', 'CreateOptionGroup', 'DescribeOptionGroups', 'DeleteOptionGroup'} <= operations, operations
    print(json.dumps({'catalogConsole': True, 'operations': sorted(operations), 'externalRequests': 0, 'axeWidths': [1440, 390]}))


if __name__ == '__main__':
    run()
