"""Real native UpdateSecret description UI acceptance, not a mock backend.

Hosted execution is required. This source does not imply browser acceptance.
Uses dummy data, isolated processes/DBs/token authorities and native SDK cleanup.
"""
import hashlib
import json
import os
import shutil
import tempfile
import time
from pathlib import Path
from urllib.parse import urlsplit

from check import Config, REPORTS, boto3, expect, running, sync_playwright, assert_reflow
from secrets_manager import control


def run():
    axe_path = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((axe_path.parent / 'provenance.json').read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance['bundleSha256']
    axe = axe_path.read_text()
    calls, outside, errors, audits = [], [], [], []
    clients, contexts = [], []

    def sdk(endpoint, token):
        origin = urlsplit(endpoint)
        assert origin.scheme == 'http' and origin.hostname == '127.0.0.1' and origin.port
        client = boto3.client('secretsmanager', endpoint_url=endpoint, region_name='us-east-1',
            aws_access_key_id='TCSIMDESCRIPTION', aws_secret_access_key='dummy-only',
            config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=2, read_timeout=5))
        client.meta.events.register('before-sign', lambda request, **kw: request.headers.add_header('X-Simulator-Token', token))
        def guard(request, **kw):
            target = urlsplit(request.url)
            assert (target.scheme, target.hostname, target.port) == (origin.scheme, origin.hostname, origin.port)
            assert target.path == '/' and not target.query and not target.fragment
        client.meta.events.register('before-send', guard)
        clients.append(client)
        return client

    def login(page, endpoint, token):
        page.goto(endpoint + '/_simulator/console/')
        page.get_by_label('Access token', exact=True).fill(token)
        page.get_by_role('button', name='Open sandbox', exact=True).click()
        expect(page.get_by_role('button', name='Services', exact=True)).to_be_visible()
        page.goto(endpoint + '/_simulator/console/#/secretsmanager')
        page.get_by_role('tab', name='Edit description', exact=True).click()

    def prepare(page, arn, text):
        page.get_by_label('Secret ARN for description', exact=True).fill(arn)
        page.get_by_label('New description', exact=True).fill(text)
        page.get_by_role('checkbox', name='Replace the description of this exact ARN (empty text clears it).', exact=True).check()

    def save(page, status=200):
        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'secretsmanager.UpdateSecret') as response:
            page.get_by_role('button', name='Save description', exact=True).focus()
            page.keyboard.press('Enter')
        assert response.value.status == status

    def audit(page, label):
        page.evaluate(axe)
        result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
        assert result['violations'] == [], label
        assert_reflow(page)
        assert 'DUMMY-DESCRIPTION-VALUE' not in page.locator('body').inner_text()
        assert not any(c['operation'] == 'secretsmanager.GetSecretValue' for c in calls)
        audits.append(label)
        page.screenshot(path=str(REPORTS / (label + '.png')), full_page=True)

    with tempfile.TemporaryDirectory(prefix='secret-description-ui-') as temp:
        with running(db=Path(temp)/'a.db', authenticated=True) as (endpoint, operator), \
                running(db=Path(temp)/'b.db', authenticated=True) as (peer_endpoint, peer_operator), sync_playwright() as playwright:
            first, peer = sdk(endpoint, operator), sdk(peer_endpoint, peer_operator)
            arn = first.create_secret(Name='training/description-ui', Description='original', SecretString='DUMMY-DESCRIPTION-VALUE-A')['ARN']
            peer_arn = peer.create_secret(Name='training/description-ui', Description='peer-original', SecretString='DUMMY-DESCRIPTION-VALUE-B')['ARN']
            versions = first.list_secret_version_ids(SecretId=arn, IncludeDeprecated=True)['Versions']
            policies = {
                'description-reader': [{'Effect':'Allow', 'Action':['secretsmanager:ListSecrets','secretsmanager:DescribeSecret'], 'Resource':'*'}],
                'description-writer': [{'Effect':'Allow', 'Action':'secretsmanager:UpdateSecret', 'Resource':arn}],
                'description-denied': [{'Effect':'Allow','Action':'secretsmanager:*','Resource':'*'}, {'Effect':'Deny','Action':'secretsmanager:UpdateSecret','Resource':arn}],
            }
            tokens = {}
            for role, statements in policies.items():
                control(endpoint, operator, '/_simulator/v1/iam/principals/' + role + '/policy', {'Version':'2012-10-17','Statement':statements})
                tokens[role] = control(endpoint, operator, '/_simulator/v1/access', {'Principal':role})['AccessToken']
            browser = playwright.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
            try:
                def new_page(destination):
                    context = browser.new_context(viewport={'width':1440,'height':1000})
                    contexts.append(context)
                    page = context.new_page()
                    page.set_default_timeout(5000)
                    page.on('pageerror', lambda error: errors.append(type(error).__name__))
                    def guard(route):
                        request = route.request
                        if not request.url.startswith(destination + '/'):
                            outside.append(urlsplit(request.url).hostname)
                            route.abort()
                            return
                        operation = request.headers.get('x-amz-target', '')
                        if operation.startswith('secretsmanager.'):
                            calls.append({'operation':operation,'input':request.post_data_json})
                        route.continue_()
                    page.route('**/*', guard)
                    return page

                page = new_page(endpoint)
                login(page, endpoint, operator)
                # Accessible inline validation, no mutation before confirmation.
                prepare(page, arn, 'x' * 2049)
                before = len(calls)
                page.get_by_role('button', name='Save description', exact=True).click()
                expect(page.get_by_text('Enter valid Unicode text of at most 2048 characters.', exact=True)).to_be_visible()
                assert len(calls) == before
                prepare(page, arn, 'console 日本語')
                save(page)
                expect(page.get_by_label('Current saved description', exact=True)).to_have_value('console 日本語')
                assert first.describe_secret(SecretId=arn)['Description'] == 'console 日本語'
                assert first.list_secret_version_ids(SecretId=arn, IncludeDeprecated=True)['Versions'] == versions
                first.update_secret(SecretId=arn, Description='SDK description')
                page.get_by_role('button', name='Refresh description metadata', exact=True).click()
                expect(page.get_by_label('Current saved description', exact=True)).to_have_value('SDK description')
                audit(page, 'description-admin-desktop')
                prepare(page, arn, '')
                save(page)
                expect(page.get_by_label('Current saved description', exact=True)).to_have_value('')
                assert first.describe_secret(SecretId=arn).get('Description', '') == ''
                page.set_viewport_size({'width':390,'height':844})
                audit(page, 'description-admin-mobile')
                page.set_viewport_size({'width':1440,'height':1000})
                page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                expect(page.get_by_label('説明を更新するシークレット ARN', exact=True)).to_have_value(arn)
                audit(page, 'description-japanese')
                page.get_by_role('combobox', name='言語', exact=True).select_option('en')

                for role in ('description-reader', 'description-denied', 'description-writer'):
                    limited = new_page(endpoint)
                    login(limited, endpoint, tokens[role])
                    prepare(limited, arn, role)
                    save(limited, 200 if role == 'description-writer' else 403)
                    if role == 'description-writer':
                        expect(limited.get_by_text('Description saved, but metadata refresh failed.', exact=False)).to_be_visible()
                        assert first.describe_secret(SecretId=arn)['Description'] == role
                        writes = sum(c['operation']=='secretsmanager.UpdateSecret' for c in calls)
                        limited.get_by_role('button', name='Refresh description metadata', exact=True).click()
                        expect(limited.get_by_text('Description saved, but metadata refresh failed.', exact=False)).to_be_visible()
                        assert sum(c['operation']=='secretsmanager.UpdateSecret' for c in calls) == writes
                    else:
                        expect(limited.get_by_text('The description update was not confirmed.', exact=False)).to_be_visible()
                        assert first.describe_secret(SecretId=arn).get('Description', '') == ''
                    audit(limited, role)

                # Hold an actual native successful write, then change host context.
                # Abort does not undo the stored write, and a late response must
                # not populate the next tab/region/identity/session.
                for boundary in ('tab', 'region', 'identity', 'logout'):
                    held = []
                    def hold(route):
                        if route.request.headers.get('x-amz-target') == 'secretsmanager.UpdateSecret':
                            calls.append({'operation':'secretsmanager.UpdateSecret','input':route.request.post_data_json})
                            response = route.fetch(max_redirects=0)
                            assert response.status == 200
                            assert set(response.json()) == {'ARN','Name'}
                            held.append((route,response))
                        else:
                            route.continue_()
                    page.route(endpoint+'/', hold)
                    prepare(page, arn, 'held-'+boundary)
                    page.get_by_role('button', name='Save description', exact=True).click()
                    deadline = time.monotonic()+5
                    while not held and time.monotonic()<deadline:
                        page.wait_for_timeout(20)
                    assert len(held)==1
                    assert first.describe_secret(SecretId=arn)['Description']=='held-'+boundary
                    # Repeated Enter during the pending transaction cannot resubmit.
                    page.keyboard.press('Enter')
                    assert len(held)==1
                    if boundary=='tab':
                        page.get_by_role('tab', name='Version metadata', exact=True).click()
                    elif boundary=='region':
                        page.get_by_role('button', name='us-east-1', exact=True).click()
                        page.get_by_role('menuitem', name='us-west-2', exact=True).click()
                    elif boundary=='identity':
                        page.get_by_role('button', name='Account menu', exact=True).click()
                        page.get_by_role('menuitem', name='Choose identity', exact=True).click()
                        dialog=page.get_by_role('dialog')
                        dialog.get_by_label('Principal name', exact=True).fill('description-reader')
                        dialog.get_by_role('button', name='Choose identity', exact=True).click()
                    else:
                        page.get_by_role('button', name='Account menu', exact=True).click()
                        page.get_by_role('menuitem', name='Sign out', exact=True).click()
                    route,response=held[0]
                    try:
                        route.fulfill(response=response)
                    except Exception:
                        assert 'ERR_ABORTED' in (route.request.failure or '')
                    page.unroute(endpoint+'/', hold)
                    expect(page.get_by_label('Current saved description', exact=True)).to_have_count(0)
                    # Fresh operator context for each independent boundary.
                    page=new_page(endpoint)
                    login(page,endpoint,operator)

                peer_page=new_page(peer_endpoint)
                login(peer_page,peer_endpoint,peer_operator)
                prepare(peer_page,peer_arn,'peer-new')
                save(peer_page)
                assert peer.describe_secret(SecretId=peer_arn)['Description']=='peer-new'
                assert first.describe_secret(SecretId=arn)['Description']=='held-logout'
                # Another server's authority cannot authenticate against this DB.
                result=peer_page.request.post(peer_endpoint+'/', headers={'X-Simulator-Token':operator,'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'secretsmanager.UpdateSecret','X-Simulator-Region':'us-east-1'}, data=json.dumps({'SecretId':peer_arn,'Description':'forbidden'}))
                assert result.status==401
                assert peer.describe_secret(SecretId=peer_arn)['Description']=='peer-new'
                assert first.list_secret_version_ids(SecretId=arn,IncludeDeprecated=True)['Versions']==versions
                assert not outside and not errors
                assert not any(c['operation']=='secretsmanager.GetSecretValue' for c in calls)
                assert all(set(c['input'])=={'SecretId','Description'} for c in calls if c['operation']=='secretsmanager.UpdateSecret')
            finally:
                for context in contexts:
                    context.close()
                browser.close()
                first.delete_secret(SecretId=arn,ForceDeleteWithoutRecovery=True)
                peer.delete_secret(SecretId=peer_arn,ForceDeleteWithoutRecovery=True)
                for client in clients:
                    client.close()
    (REPORTS/'secret-description-console.json').write_text(json.dumps({'audits':audits,'outsideRequests':len(outside),'pageErrors':len(errors),'postAbortJSDeliveryProved':False},indent=2)+'\n')
    print('Secret description actual console/native/IAM/context/accessibility PASS')


if __name__=='__main__':
    run()
