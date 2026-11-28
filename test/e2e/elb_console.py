"""Exercise real ELBv2 Cloudscape, Scalar and SDK operations in two local team databases."""

from contextlib import ExitStack, contextmanager
from hashlib import file_digest
from http.client import HTTPConnection
import json
import os
from pathlib import Path
import tempfile
from urllib.parse import parse_qsl, urlsplit

from playwright.sync_api import expect, sync_playwright

from autoscaling_plans_console import wire
from elb_scalar import NS, SERVICE, VERSION, reference, sdk, send, setup_listener
from gcp_dns_sdk import LocalTeam, started


ROUTE = '/_simulator/console/#/elasticloadbalancing'
NAME = 'scalar-practice-alb'
GROUP = 'scalar-linked'
READS = ('DescribeLoadBalancers', 'DescribeListeners', 'DescribeTargetGroups', 'DescribeTargetHealth')


def listener_response(team, suffix):
    connection = HTTPConnection('127.0.0.1', team.port, timeout=40)
    try:
        connection.request('GET', '/_elb/us-east-1/' + NAME + suffix, headers={'X-Simulator-Token':team.token})
        result = connection.getresponse()
        return result.status, result.read()
    finally:
        connection.close()


@contextmanager
def console(browser, team, token, principal, *, japanese=False):
    context = browser.new_context(
        viewport={'width': 390 if japanese else 1365, 'height': 900},
        service_workers='block', reduced_motion='reduce',
    )
    blocked, faults, sent = [], [], []
    expected_origin = urlsplit(team.endpoint).netloc
    try:
        def contain(route):
            url = urlsplit(route.request.url)
            if (url.scheme, url.netloc) == ('http', expected_origin):
                route.continue_()
            else:
                blocked.append(url.scheme)
                route.abort()

        context.route('**/*', contain)
        page = context.new_page()
        page.on('pageerror', lambda error: faults.append(str(error)))
        page.goto(team.endpoint + '/_simulator/console/')
        page.get_by_label('Access token', exact=True).fill(token)
        page.get_by_role('button', name='Open sandbox', exact=True).click()
        expect(page.get_by_role('button', name='Services', exact=True)).to_be_visible()
        if japanese:
            page.evaluate("localStorage.setItem('tenkacloud-simulator-locale','ja')")
            page.reload()
            expect(page.locator('html')).to_have_attribute('lang', 'ja')

        def observe(request):
            if request.headers.get('x-simulator-service') != SERVICE:
                return
            fields = parse_qsl(request.post_data or '', keep_blank_values=True, strict_parsing=True)
            data = dict(fields)
            headers = request.all_headers()
            assert request.url == team.endpoint + '/' and request.method == 'POST'
            assert request.redirected_from is None and len(data) == len(fields)
            assert data['Version'] == VERSION and data['Action'] in {*READS, 'ModifyTargetGroup'}
            assert headers['content-type'].split(';')[0] == 'application/x-www-form-urlencoded'
            assert headers.get('cookie') and headers['x-simulator-principal'] == principal
            assert {'authorization', 'x-simulator-token', 'x-amz-target', 'x-amz-security-token'}.isdisjoint(headers)
            sent.append((headers['x-simulator-region'], data))

        page.on('request', observe)
        page.goto(team.endpoint + ROUTE)
        expect(page.get_by_role('heading', name='Elastic Load Balancing', exact=True).last).to_be_visible()
        yield page, sent
        assert not blocked and not faults, (blocked, faults)
    finally:
        context.close()


def audit(page):
    page.evaluate(Path(os.environ['AXE_SCRIPT']).read_text())
    serious = page.evaluate("""async () => (await axe.run(document, {runOnly: {type: 'tag',
      values: ['wcag2a','wcag2aa','wcag21a','wcag21aa']}})).violations
      .filter(v=>['serious','critical'].includes(v.impact)).map(v=>[v.id,v.nodes.map(n=>n.target)])""")
    assert not serious, serious
    assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth + 1')


def click_name(page, name):
    button = page.get_by_role('button', name=name, exact=True)
    button.focus()
    button.press('Enter')


def details(page, lb, group, function, *, japanese=False):
    click_name(page, lb['LoadBalancerName'])
    for value in (lb['VpcId'], lb['LoadBalancerArn'], *lb['SecurityGroups']):
        expect(page.get_by_text(value, exact=True)).to_be_visible()
    for zone in lb['AvailabilityZones']:
        expect(page.get_by_text(zone['SubnetId'], exact=True)).to_be_visible()
        expect(page.get_by_text(zone['ZoneName'], exact=True)).to_be_visible()
    table = page.get_by_role('table', name='リスナー' if japanese else 'Listeners', exact=True)
    expect(table).to_contain_text('HTTP')
    expect(table).to_contain_text('80')
    expect(table).to_contain_text('forward')
    expect(table).to_contain_text(group)
    click_name(page, GROUP)
    health = page.get_by_role('table', name='登録済みターゲット' if japanese else 'Registered targets', exact=True)
    expect(health).to_contain_text(function)
    expect(health).to_contain_text('unavailable')
    expect(health).to_contain_text('Target.HealthCheckDisabled')


def edits(sent):
    return [params for region, params in sent if params['Action'] == 'ModifyTargetGroup']


def save_path(page, path):
    page.get_by_label('Health check path', exact=True).fill(path)
    click_name(page, 'Save health check path')


def restrict(team, principal, actions):
    wire(team, 'PUT', '/_simulator/v1/iam/principals/' + principal + '/policy', {
        'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': list(actions), 'Resource': '*'}],
    })


def check_stale_read(page, sent, team, group):
    held = []
    def delay(route):
        fields = dict(parse_qsl(route.request.post_data or ''))
        if fields.get('Action') == 'DescribeTargetHealth' and fields.get('TargetGroupArn') == group and not held:
            held.append((route, route.fetch(max_redirects=0)))
        else:
            route.fallback()
    page.route(team.endpoint + '/', delay)
    try:
        click_name(page, 'Refresh')
        for _ in range(100):
            if held:
                break
            page.wait_for_timeout(30)
        assert len(held) == 1 and held[0][1].status == 200
        # Same-document hash navigation exercises the shipped region/identity keyed component.
        page.goto(team.endpoint + ROUTE + '?region=us-west-2')
        expect(page.get_by_text('No load balancers found.', exact=True)).to_be_visible()
        assert any(region == 'us-west-2' and q['Action'] == 'DescribeLoadBalancers' for region, q in sent)
        route, response = held[0]
        route.fulfill(response=response)
        expect(page.get_by_text('No load balancers found.', exact=True)).to_be_visible()
        expect(page.get_by_text(group, exact=True)).to_have_count(0)
        expect(page.get_by_role('button', name='Save health check path')).to_have_count(0)
    finally:
        page.unroute(team.endpoint + '/', delay)


def check_moved_group(browser, owner, clean, elb, lb, group, function):
    # Native operations can reassign this group while its detail view is open.
    # Each new listener is registered for cleanup immediately after creation.
    parent = lb['LoadBalancerArn']
    alternate = elb.create_load_balancer(
        Name='console-alternate', Subnets=[z['SubnetId'] for z in lb['AvailabilityZones']],
        SecurityGroups=lb['SecurityGroups'],
    )['LoadBalancers'][0]['LoadBalancerArn']
    clean.callback(elb.delete_load_balancer, LoadBalancerArn=alternate)
    old = elb.describe_listeners(LoadBalancerArn=parent)['Listeners']
    assert len(old) == 1 and old[0]['DefaultActions'][0]['TargetGroupArn'] == group

    def attach(arn):
        listener = elb.create_listener(LoadBalancerArn=arn, Protocol='HTTP', Port=80,
                                       DefaultActions=[{'Type':'forward','TargetGroupArn':group}])['Listeners'][0]
        clean.callback(elb.delete_listener, ListenerArn=listener['ListenerArn'])
        return listener['ListenerArn']

    def current():
        return elb.describe_target_groups(TargetGroupArns=[group])['TargetGroups'][0]

    before = current()
    with console(browser,owner,owner.token,'simulator-admin') as (page, requests):
        details(page,lb,group,function)
        elb.delete_listener(ListenerArn=old[0]['ListenerArn'])
        moved = attach(alternate)
        assert current() == before | {'LoadBalancerArns':[alternate]}
        start = len(requests)
        save_path(page,'/stale-parent')
        unavailable = page.get_by_text('This target group is no longer linked to the selected load balancer.',exact=True)
        expect(unavailable).to_be_visible()
        assert [p for _,p in requests[start:]] == [
            {'Action':'DescribeTargetGroups','Version':VERSION,'TargetGroupArns.member.1':group}]
        expect(page.get_by_role('button',name='Save health check path',exact=True)).to_have_count(0)
        assert current() == before | {'LoadBalancerArns':[alternate]}
        elb.delete_listener(ListenerArn=moved)
        assert current() == before | {'LoadBalancerArns':[]}
        click_name(page,'Refresh')
        expect(unavailable).to_be_visible()
        assert not edits(requests)
        attach(parent)
        click_name(page,'Refresh')
        expect(page.get_by_label('Health check path',exact=True)).to_have_value(before['HealthCheckPath'])
        expect(page.get_by_text('Saved health check path verified.',exact=True)).to_have_count(0)
        assert current() == before and not edits(requests)


def run(owner, peer, browser):
    with ExitStack() as clients:
        elb, ec2, iam, fn_client = [sdk(clients, owner, s) for s in ('elbv2','ec2','iam','lambda')]
        other = sdk(clients, peer, 'elbv2')
        def saved(client, arn):
            records = client.describe_target_groups(TargetGroupArns=[arn])['TargetGroups']
            assert len(records) == 1 and records[0]['TargetGroupArn'] == arn
            return records[0]
        with ExitStack() as clean:
            lb, group, function = setup_listener(clean, elb, ec2, iam, fn_client)
            sibling = elb.create_target_group(Name='console-sibling', TargetType='lambda')['TargetGroups'][0]
            clean.callback(elb.delete_target_group, TargetGroupArn=sibling['TargetGroupArn'])
            peer_group = other.create_target_group(Name=GROUP, TargetType='lambda', HealthCheckPath='/peer')['TargetGroups'][0]
            clean.callback(other.delete_target_group, TargetGroupArn=peer_group['TargetGroupArn'])
            original = saved(elb,group)
            assert listener_response(owner, '/80/console') == (200,b'local')
            assert listener_response(owner, '/81/missing')[0] == 404
            with console(browser,owner,owner.token,'simulator-admin') as (page, requests):
                field = page.get_by_label('Exact load balancer name', exact=True)
                field.fill('absent-console-alb')
                click_name(page, 'Apply filter')
                expect(page.get_by_text('No load balancers found.',exact=True)).to_be_visible()
                field.fill(NAME)
                click_name(page, 'Apply filter')
                details(page,lb,group,function)
                assert any(f.get('Names.member.1') == NAME for _,f in requests)
                audit(page)
                save_path(page,'/console-check')
                expect(page.get_by_text('Saved health check path verified.',exact=True)).to_be_visible()
                assert saved(elb,group) == original | {'HealthCheckPath':'/console-check'}
                assert len(edits(requests)) == 1

                delivered = []
                def drop_ack(route):
                    data = dict(parse_qsl(route.request.post_data or ''))
                    if data.get('Action') == 'ModifyTargetGroup':
                        response = route.fetch(max_redirects=0)
                        assert response.status == 200 and b'ModifyTargetGroupResult' in response.body()
                        delivered.append(data)
                        route.abort('connectionreset')
                    else:
                        route.fallback()
                page.route(owner.endpoint + '/',drop_ack)
                try:
                    save_path(page,'/after-lost-response')
                    expect(page.get_by_role('button',name='Refresh affected target group',exact=True)).to_be_visible()
                    assert len(delivered) == 1 and saved(elb,group)['HealthCheckPath'] == '/after-lost-response'
                finally:
                    page.unroute(owner.endpoint + '/',drop_ack)
                click_name(page,'Refresh affected target group')
                expect(page.get_by_label('Health check path',exact=True)).to_have_value('/after-lost-response')
                expect(page.get_by_text('Saved health check path verified.',exact=True)).to_have_count(0)
                assert edits(requests) == [
                    {'Action':'ModifyTargetGroup','Version':VERSION,'TargetGroupArn':group,'HealthCheckPath':p}
                    for p in ('/console-check','/after-lost-response')]
                check_stale_read(page,requests,owner,group)

            check_moved_group(browser, owner, clean, elb, lb, group, function)
            assert saved(elb,sibling['TargetGroupArn']) == sibling
            assert saved(other,peer_group['TargetGroupArn']) == peer_group

            with reference(browser,owner,owner.token) as (ref, requests):
                received = []
                result = send(ref,requests,'DescribeTargetHealth',{'TargetGroupArn':group},200,received)
                assert received == [['DescribeTargetHealth',200]]
                assert result.findtext('e:TargetHealthDescriptions/e:member/e:Target/e:Id',namespaces=NS) == function
                expect(ref.response_panel).to_contain_text(function)
            actor = 'elb-console-reader'
            restrict(owner,actor,['elasticloadbalancing:' + op for op in READS])
            token = wire(owner,'POST','/_simulator/v1/access',{'Principal':actor})['AccessToken']
            with console(browser,owner,token,actor) as (view, observed):
                details(view,lb,group,function)
                before = saved(elb,group)
                save_path(view,'/denied')
                expect(view.get_by_text('AccessDenied:',exact=False)).to_be_visible()
                assert saved(elb,group) == before and len(edits(observed)) == 1
                expect(view.get_by_text(function,exact=True)).to_have_count(0)
                restrict(owner,actor,['elasticloadbalancing:DescribeListeners'])
                click_name(view,'Back to load balancers')
                expect(view.get_by_text('AccessDenied:',exact=False)).to_be_visible()
                expect(view.get_by_role('button',name=NAME,exact=True)).to_have_count(0)
                expect(view.get_by_text('No load balancers found.',exact=True)).to_have_count(0)

            owner.stop()
            owner.start()
            assert saved(elb,group) == original | {'HealthCheckPath':'/after-lost-response'}
            assert saved(elb,sibling['TargetGroupArn']) == sibling
            assert saved(other,peer_group['TargetGroupArn']) == peer_group
            assert listener_response(owner, '/80/again') == (200,b'local')
            with console(browser,owner,owner.token,'simulator-admin',japanese=True) as (mobile,_):
                details(mobile,lb,group,function,japanese=True)
                expect(mobile.get_by_label('ヘルスチェックパス',exact=True)).to_have_value('/after-lost-response')
                audit(mobile)
        assert elb.describe_load_balancers()['LoadBalancers'] == []
        assert elb.describe_target_groups()['TargetGroups'] == [] and other.describe_target_groups()['TargetGroups'] == []


def main():
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    with binary.open('rb') as f:
        assert file_digest(f,'sha256').hexdigest() == os.environ['SIMULATOR_BINARY_SHA256']
    with tempfile.TemporaryDirectory(prefix='elb-console-') as tmp, sync_playwright() as pw, ExitStack() as stack:
        root = Path(tmp)
        for name in ('owner','peer'):
            (root/name).mkdir()
        owner, peer = [LocalTeam(root/name,binary) for name in ('owner','peer')]
        stack.enter_context(started(owner))
        stack.enter_context(started(peer))
        browser = stack.enter_context(pw.chromium.launch(
            executable_path=os.environ['CHROMIUM_EXECUTABLE'],chromium_sandbox=True))
        run(owner,peer,browser)
    print(json.dumps({'console':True,'scope':'owner+peer','sdk':True,'scalar_sends':1,
                      'verified_and_ambiguous_writes':2,'denied_write':1,'real_listener_statuses':[200,404],
                      'owner_restart':True,'english_desktop':True,'japanese_mobile':True,'cleanup':True}))


if __name__ == '__main__':
    main()
