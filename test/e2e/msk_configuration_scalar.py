"""MSK configurations and attached clusters through stock boto3, Cloudscape and Scalar."""
from contextlib import ExitStack
import os
from pathlib import Path
import re
from tempfile import TemporaryDirectory
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from aws_catalog import Team

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
