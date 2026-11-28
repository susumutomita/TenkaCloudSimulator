"""MSK configurations and attached clusters through stock boto3, Cloudscape and Scalar."""
from contextlib import ExitStack
import os
from pathlib import Path
import re
from tempfile import TemporaryDirectory
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from aws_catalog import Team, refused

CONFIG = 'reference-configuration'
CLUSTER = 'reference-cluster'
TITLE = 'Amazon Managed Streaming for Apache Kafka'
PROPERTIES = b'auto.create.topics.enable=false\n'


def check_shared_views(browser, team, configuration, cluster):
    with browser.new_context(service_workers='block') as context:
        outside = []
        own = urlsplit(team.endpoint)

        def same_origin(route):
            url = urlsplit(route.request.url)
            if (url.scheme, url.netloc) == (own.scheme, own.netloc):
                route.continue_()
            else:
                outside.append(url.hostname)
                route.abort()

        context.route('**/*', same_origin)
        assert context.request.post(team.endpoint + '/_simulator/login', form={'token': team.token},
                                    max_redirects=0).status == 303
        console = context.new_page()
        console.goto(team.endpoint + '/_simulator/console/')
        console.get_by_role('button', name='Services', exact=True).click()
        console.get_by_role('menuitem', name='Service catalog', exact=True).click()
        console.get_by_role('button', name=re.compile('^Catalog service')).click()
        console.get_by_role('option', name=re.compile('^' + TITLE)).click()
        expect(console.get_by_role('link', name=CLUSTER, exact=True)).to_be_visible()
        console.get_by_role('tab', name=re.compile('Configuration', re.I)).click()
        expect(console.get_by_role('link', name=CONFIG, exact=True)).to_be_visible()

        page = context.new_page()
        page.goto(team.endpoint + '/_simulator/docs/catalog.html?api=kafka')
        expect(page.get_by_text(TITLE + ' (simulator catalog)', exact=True).first).to_be_visible()
        sent = []
        page.on('request', lambda request: sent.append((request.method, request.url)))
        for operation, path in [('ListConfigurations', '/v1/configurations'), ('ListClusters', '/v1/clusters')]:
            page.get_by_text(operation, exact=True).first.click()
            page.get_by_role('button', name=f'Test Request (get {path})', exact=True).click()
            url = team.endpoint + path
            with page.expect_response(lambda r: r.url == url and r.request.method == 'GET') as pending:
                page.get_by_role('button', name=re.compile('^Send get request to ')).click()
            response = pending.value
            request = response.request
            assert response.status == 200 and request.redirected_from is None
            assert request.header_value('x-simulator-service') == 'kafka'
            assert request.header_value('cookie') and not request.header_value('x-simulator-token')
            assert not request.header_value('authorization')
            if operation == 'ListConfigurations':
                rows = response.json()['configurations']
                assert len(rows) == 1 and rows[0]['arn'] == configuration and 'serverProperties' not in rows[0]
            else:
                rows = response.json()['clusterInfoList']
                assert len(rows) == 1 and rows[0]['clusterArn'] == cluster
                assert rows[0]['currentBrokerSoftwareInfo'] == {
                    'configurationArn': configuration, 'configurationRevision': 1, 'kafkaVersion': '3.6.0'}
            body = page.locator('.response-section-content-body').filter(
                has=page.get_by_role('link', name='Download Response Body', exact=True))
            expect(body).to_have_count(1)
            expect(body).to_contain_text(configuration)
            if operation == 'ListClusters':
                expect(body).to_contain_text(cluster)
            page.get_by_role('button', name='Close Client', exact=True).click()
        assert [entry for entry in sent if urlsplit(entry[1]).path.startswith('/v1/')] == [
            ('GET', team.endpoint + '/v1/configurations'), ('GET', team.endpoint + '/v1/clusters')]
        assert not outside, 'MSK browser attempted external access'


def clean_team(team, record):
    try:
        if 'client' in record:
            client = record['client']
            if 'cluster' in record:
                client.delete_cluster(ClusterArn=record['cluster'])
            if 'configuration' in record:
                client.delete_configuration(Arn=record['configuration'])
            assert client.list_clusters()['ClusterInfoList'] == []
            assert client.list_configurations()['Configurations'] == []
    finally:
        if team.process is not None:
            team.stop()


def check_participant(team, configuration, cluster):
    principal = 'msk-scope'
    policy_path = f'/_simulator/v1/iam/principals/{principal}/policy'

    def policy(statements):
        team.control('PUT', policy_path, {'Version': '2012-10-17', 'Statement': statements})

    def allow(action, resource):
        return {'Effect': 'Allow', 'Action': action, 'Resource': resource}

    policy([allow('sts:GetCallerIdentity', '*')])
    token = team.control('POST', '/_simulator/v1/access', {'Principal': principal})['AccessToken']
    client = team.client('kafka', token=token)
    cluster_input = {'ClusterName': 'msk-scope-test', 'KafkaVersion': '3.6.0', 'NumberOfBrokerNodes': 2,
                     'BrokerNodeGroupInfo': {'InstanceType': 'kafka.m5.large', 'ClientSubnets': ['subnet-a', 'subnet-b']},
                     'ConfigurationInfo': {'Arn': configuration, 'Revision': 1}, 'Tags': {'scope': 'fixture'}}
    try:
        for call in (lambda: client.create_cluster(**cluster_input), lambda: client.describe_cluster(ClusterArn=cluster),
                     lambda: client.delete_cluster(ClusterArn=cluster), client.list_clusters, client.list_configurations,
                     lambda: client.create_configuration(Name='msk-scope-test', ServerProperties=PROPERTIES),
                     lambda: client.describe_configuration(Arn=configuration),
                     lambda: client.describe_configuration_revision(Arn=configuration, Revision=1),
                     lambda: client.delete_configuration(Arn=configuration)):
            refused('ForbiddenException', call, 403)
        cluster_pattern = cluster.rsplit(':cluster/', 1)[0] + ':cluster/msk-scope-test/*'
        policy([allow('kafka:ListClusters', cluster_pattern), allow('kafka:ListConfigurations', configuration)])
        refused('ForbiddenException', client.list_clusters, 403)
        refused('ForbiddenException', client.list_configurations, 403)
        policy([allow(['kafka:ListClusters', 'kafka:ListConfigurations'], '*')])
        assert [row['ClusterArn'] for row in client.list_clusters(ClusterNameFilter=CLUSTER)['ClusterInfoList']] == [cluster]
        assert [row['Arn'] for row in client.list_configurations()['Configurations']] == [configuration]
        create_grants = [allow('kafka:CreateCluster', [cluster_pattern, configuration])]
        policy(create_grants)
        refused('ForbiddenException', lambda: client.create_cluster(**cluster_input), 403)
        policy(create_grants + [allow('kafka:TagResource', cluster_pattern)])
        created = client.create_cluster(**cluster_input)['ClusterArn']
        admin = team.client('kafka')
        try:
            detail = admin.describe_cluster(ClusterArn=created)['ClusterInfo']
            assert detail['CurrentBrokerSoftwareInfo']['ConfigurationArn'] == configuration
            assert detail['Tags'] == {'scope': 'fixture'}
            assert detail['EncryptionInfo']['EncryptionInTransit'] == {'ClientBroker': 'TLS', 'InCluster': True}
        finally:
            admin.delete_cluster(ClusterArn=created)
    finally:
        team.control('DELETE', policy_path)


def check_msk_reference(browser):
    with TemporaryDirectory() as owner_dir, TemporaryDirectory() as peer_dir, ExitStack() as cleanup:
        owner, peer = Team(owner_dir), Team(peer_dir)
        resources = {}
        for team in (owner, peer):
            record = resources[team] = {}
            cleanup.callback(clean_team, team, record)
            team.start()
            client = record['client'] = team.client('kafka')
            arn = client.create_configuration(Name=CONFIG, ServerProperties=PROPERTIES)['Arn']
            record['configuration'] = arn
            record['cluster'] = client.create_cluster(
                ClusterName=CLUSTER, KafkaVersion='3.6.0', NumberOfBrokerNodes=2,
                BrokerNodeGroupInfo={'InstanceType': 'kafka.m5.large', 'ClientSubnets': ['subnet-a', 'subnet-b']},
                ConfigurationInfo={'Arn': arn, 'Revision': 1})['ClusterArn']
        mine, other = resources[owner], resources[peer]
        assert mine['configuration'] != other['configuration'] and mine['cluster'] != other['cluster']
        for team in (owner, peer):
            check_participant(team, resources[team]['configuration'], resources[team]['cluster'])
        owner.stop()
        owner.start()
        mine['client'] = owner.client('kafka')
        check_shared_views(browser, owner, mine['configuration'], mine['cluster'])
        for record in (mine, other):
            client = record['client']
            software = client.describe_cluster(ClusterArn=record['cluster'])['ClusterInfo']['CurrentBrokerSoftwareInfo']
            assert software['ConfigurationArn'] == record['configuration'] and software['ConfigurationRevision'] == 1
            assert client.describe_configuration_revision(Arn=record['configuration'], Revision=1)['ServerProperties'] == PROPERTIES
    print('MSK stock SDK, Cloudscape, Scalar Send, restart, peer isolation and cleanup passed')


if __name__ == '__main__':
    executable = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    with sync_playwright() as playwright:
        with playwright.chromium.launch(executable_path=str(executable), chromium_sandbox=True,
                                        args=['--proxy-server=http://127.0.0.1:9', '--proxy-bypass-list=127.0.0.1',
                                              '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1']) as browser:
            check_msk_reference(browser)
