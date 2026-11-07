"""Source acceptance for the root-mounted KMS Aliases tab; no fixture UI/mock API.
Requires the canonical bundle/CLI with KMSAliasesView mounted on key details.
"""
from console_locale import select_console_locale
import collections
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import sys
import tempfile
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parent))
from check import running, REPORTS, sync_playwright, expect, boto3, Config
from reflow import assert_reflow

axe = os.environ.get('AXE_SCRIPT')
assert axe and Path(axe).is_file(), 'Canonical AXE_SCRIPT required; no skip'


def guard(url, endpoint):
    expected, actual = urlsplit(endpoint), urlsplit(url)
    assert expected.scheme == 'http' and expected.port and ipaddress.ip_address(expected.hostname).is_loopback
    assert actual.scheme == 'http' and actual.netloc == expected.netloc and not actual.username and not actual.password and not actual.fragment
    assert ipaddress.ip_address(actual.hostname).is_loopback


def client(service, endpoint, token, stats, clients):
    guard(endpoint, endpoint)
    count = {'calls': 0, 'sends': 0, 'responses': 0}
    stats.append(count)
    c = boto3.client(service, endpoint_url=endpoint, region_name='us-east-1',
        aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
        config=Config(retries={'max_attempts': 0}, proxies={}))
    c.meta.events.register('before-sign.' + service, lambda request, **kw: request.headers.add_header('X-Simulator-Token', token))
    def called(**kw): count['calls'] += 1
    def sent(request, **kw): guard(request.url, endpoint); count['sends'] += 1
    def response(**kw): count['responses'] += 1
    c.meta.events.register('before-call.' + service, called)
    c.meta.events.register('before-send.' + service, sent)
    c.meta.events.register('after-call.' + service, response)
    clients.append(c)
    return c


def saved_frame(path):
    # Complete local dummy-fixture values, including material/secret bytes, are
    # compared only in memory and never included in errors/results/screenshots.
    with sqlite3.connect(path) as db:
        return {table: db.execute('SELECT * FROM ' + table + ' ORDER BY 1,2').fetchall()
                for table in ['aws_kms_keys', 'aws_kms_aliases', 'aws_kms_alias_state']}


def alter(path, old, new):
    assert (old, new) in {('target_created', 'unavailable_target'), ('unavailable_target', 'target_created')}
    with sqlite3.connect(path, timeout=5) as db:
        db.execute('ALTER TABLE aws_kms_aliases RENAME COLUMN ' + old + ' TO ' + new)


def main():
    clients, stats = [], []
    try:
        with tempfile.TemporaryDirectory(prefix='kms-alias-console-') as directory:
            state = Path(directory) / 'team.db'
            with running(state, authenticated=True) as (endpoint, operator), sync_playwright() as p:
                kms = client('kms', endpoint, operator, stats, clients)
                iam = client('iam', endpoint, operator, stats, clients)
                first = kms.create_key(Description='Dummy alias UI first')['KeyMetadata']
                second = kms.create_key(Description='Dummy alias UI second')['KeyMetadata']
                browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
                context = browser.new_context(viewport={'width': 1440, 'height': 1000})
                try:
                    context.add_init_script(path=axe)
                    page = context.new_page(); page.set_default_timeout(10000)
                    errors, outside, held = [], [], []
                    writes = collections.Counter()
                    page.on('pageerror', lambda error: errors.append(str(error)))
                    def network(route):
                        if route.request.url.startswith(endpoint + '/'):
                            route.continue_()
                        else:
                            outside.append(route.request.url); route.abort()
                    context.route('**/*', network)
                    def count_write(request):
                        op = request.headers.get('x-amz-target', '')
                        if op in ['TrentService.CreateAlias', 'TrentService.UpdateAlias', 'TrentService.DeleteAlias']:
                            writes[op] += 1
                    page.on('request', count_write)
                    page.goto(endpoint + '/_simulator/console/')
                    page.get_by_label('Access token', exact=True).fill(operator)
                    page.get_by_role('button', name='Open sandbox', exact=True).click()
                    expect(page.get_by_role('button', name='Services', exact=True)).to_be_visible()
                    page.get_by_role('button', name='Services', exact=True).click()
                    page.get_by_role('menuitem', name='Key Management Service', exact=True).click()

                    def open_key(key_id):
                        back = page.get_by_role('button', name='Customer managed keys', exact=True)
                        if back.count(): back.click()
                        page.get_by_role('link', name=key_id, exact=True).click()
                        page.get_by_role('tab', name='Aliases', exact=True).click()
                        expect(page.locator('[data-kms-aliases-view]')).to_be_visible()
                        expect(page.get_by_role('button', name='Create alias', exact=True)).to_be_enabled()
                    def pick_target(key_id):
                        page.get_by_label('Target key', exact=True).click()
                        option = page.get_by_role('option').filter(has_text=key_id)
                        expect(option).to_have_count(1); option.click()
                        expect(page.locator('[data-kms-aliases-view] span.resource-identifier').filter(has_text=re.compile('^' + re.escape(key_id) + '$'))).to_be_visible()
                        expect(page.get_by_role('button', name='Change target', exact=True)).to_be_enabled()
                    def visible_alias(name):
                        return page.get_by_role('radio', name=name, exact=True)
                    def audit(phase, width):
                        assert_reflow(page)
                        result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
                        (REPORTS / f'kms-aliases-{phase}-{width}.axe.json').write_text(json.dumps(result, indent=2))
                        assert not [v for v in result['violations'] if v['impact'] in ['serious', 'critical']], 'Serious/critical axe violation'
                        page.screenshot(path=str(REPORTS / f'kms-aliases-{phase}-{width}.png'), full_page=True, animations='disabled')
                    open_key(first['KeyId'])
                    expect(page.get_by_text('No aliases', exact=True)).to_be_visible()
                    page.get_by_role('button', name='Create alias', exact=True).click()
                    expect(page.get_by_text(first['Arn'], exact=True)).to_be_visible()
                    page.get_by_label('Alias name', exact=True).fill('ui-draft')
                    page.screenshot(path=str(REPORTS / 'kms-aliases-create-1440.png'), full_page=True, animations='disabled')
                    select_console_locale(page, 'ja')
                    expect(page.get_by_label('エイリアス名', exact=True)).to_have_value('ui-draft')
                    page.screenshot(path=str(REPORTS / 'kms-aliases-create-ja-1440.png'), full_page=True, animations='disabled')
                    expect(page.get_by_text(first['Arn'], exact=True)).to_be_visible()
                    select_console_locale(page, 'en')
                    expect(page.get_by_label('Alias name', exact=True)).to_have_value('ui-draft')
                    page.get_by_role('button', name='Cancel', exact=True).click()
                    assert kms.list_aliases()['Aliases'] == []

                    # A real native ACK followed by a real native storage500:
                    # the form must already be closed and Retry must be GET-like
                    # ListAliases only. No response body or server state is mocked.
                    page.get_by_role('button', name='Create alias', exact=True).click()
                    page.get_by_label('Alias name', exact=True).fill('ui-ack')
                    expect(page.get_by_text(first['Arn'], exact=True)).to_be_visible()
                    def hold_list(route):
                        if route.request.headers.get('x-amz-target') == 'TrentService.ListAliases' and not held:
                            held.append(route)
                        else: route.continue_()
                    page.route(endpoint + '/', hold_list)
                    with page.expect_response(lambda response: response.request.headers.get('x-amz-target') == 'TrentService.CreateAlias' and response.status == 200):
                        page.get_by_role('button', name='Create alias', exact=True).click()
                    expect(page.get_by_label('Alias name', exact=True)).to_have_count(0)
                    expect(page.get_by_text('Alias created.', exact=True)).to_be_visible()
                    page.wait_for_function('()=>document.querySelector("[data-kms-aliases-view]")!==null')
                    assert len(held) == 1
                    assert writes['TrentService.CreateAlias'] == 1
                    assert kms.list_aliases(KeyId=first['KeyId'])['Aliases'][0]['AliasName'] == 'alias/ui-ack'
                    before_failure = saved_frame(state)
                    alter(state, 'target_created', 'unavailable_target')
                    try:
                        with page.expect_response(lambda response: response.request.headers.get('x-amz-target') == 'TrentService.ListAliases' and response.status == 500):
                            held.pop().continue_()
                        expect(page.get_by_role('button', name='Retry alias list (read only)', exact=True)).to_be_visible()
                        expect(page.get_by_label('Alias name', exact=True)).to_have_count(0)
                    finally:
                        alter(state, 'unavailable_target', 'target_created')
                        page.unroute(endpoint + '/', hold_list)
                    assert saved_frame(state) == before_failure, 'Read failure mutated persisted state'
                    acknowledged_writes = writes.copy()
                    page.get_by_role('button', name='Retry alias list (read only)', exact=True).click()
                    expect(visible_alias('alias/ui-ack')).to_be_visible()
                    assert writes == acknowledged_writes, 'Read retry replayed an acknowledged write'

                    # Older failed refresh must not overwrite a newer200. The
                    # old500 is fetched from the actual temporarily damaged DB;
                    # only a harmless test header marks body-consumption timing.
                    page.evaluate("""()=>{window.__aliasFetch=window.fetch;window.__aliasOldBodyConsumed=false;
                    window.fetch=async(...args)=>{const response=await window.__aliasFetch(...args);
                    if(response.headers.get('x-alias-old-read')==='yes'){const text=response.text.bind(response);response.text=async()=>{try{return await text()}finally{requestAnimationFrame(()=>requestAnimationFrame(()=>window.__aliasOldBodyConsumed=true))}}}return response}}""")
                    page.route(endpoint + '/', hold_list)
                    with page.expect_request(lambda request: request.headers.get('x-amz-target') == 'TrentService.ListAliases'):
                        page.get_by_role('button', name='Refresh aliases', exact=True).click()
                    expect(page.get_by_role('button', name='Refresh aliases', exact=True)).to_be_enabled()
                    assert len(held) == 1
                    with page.expect_response(lambda response: response.request.headers.get('x-amz-target') == 'TrentService.ListAliases' and response.status == 200):
                        page.get_by_role('button', name='Refresh aliases', exact=True).click()
                    expect(visible_alias('alias/ui-ack')).to_be_visible()
                    before_old_failure = saved_frame(state)
                    old_response = None
                    alter(state, 'target_created', 'unavailable_target')
                    try:
                        old_route = held.pop()
                        old_response = old_route.fetch()
                        assert old_response.status == 500
                    finally:
                        alter(state, 'unavailable_target', 'target_created')
                    try:
                        old_route.fulfill(response=old_response, headers={**old_response.headers, 'x-alias-old-read': 'yes'})
                        page.wait_for_function('()=>window.__aliasOldBodyConsumed===true')
                        expect(visible_alias('alias/ui-ack')).to_be_visible()
                        expect(page.get_by_role('button', name='Retry alias list (read only)', exact=True)).to_have_count(0)
                        assert saved_frame(state) == before_old_failure
                    finally:
                        if old_response is not None: old_response.dispose()
                        page.unroute(endpoint + '/', hold_list)
                        page.evaluate('window.fetch=window.__aliasFetch')

                    # Retarget is explicitly a local API tool, not AWS-console parity.
                    visible_alias('alias/ui-ack').check()
                    page.get_by_role('button', name='Change target (local API)', exact=True).click()
                    expect(page.get_by_label('Alias name', exact=True)).to_have_value('ui-ack')
                    expect(page.get_by_label('Alias name', exact=True)).to_have_js_property('readOnly', True)
                    pick_target(second['KeyId'])
                    page.get_by_role('button', name='Change target', exact=True).focus(); page.keyboard.press('Enter')
                    expect(page.get_by_text('Alias target changed.', exact=True)).to_be_visible()
                    expect(page.get_by_text('No aliases', exact=True)).to_be_visible()
                    assert kms.list_aliases(KeyId=second['KeyId'])['Aliases'][0]['AliasName'] == 'alias/ui-ack'
                    open_key(second['KeyId'])
                    visible_alias('alias/ui-ack').check()
                    page.get_by_role('button', name='Delete alias', exact=True).click()
                    dialog = page.get_by_role('dialog')
                    expect(dialog.get_by_role('button', name='Delete alias', exact=True)).to_be_disabled()
                    audit('delete-confirm', 1440)
                    page.keyboard.press('Escape'); expect(dialog).to_have_count(0)
                    assert len(kms.list_aliases()['Aliases']) == 1
                    page.set_viewport_size({'width': 390, 'height': 844})
                    page.get_by_role('button', name='Delete alias', exact=True).click()
                    confirmation = dialog.get_by_label('Type alias name to confirm', exact=True)
                    confirmation.focus(); expect(confirmation).to_be_focused()
                    page.keyboard.type('alias/ui-ack')
                    expect(confirmation).to_have_value('alias/ui-ack')
                    audit('delete-confirm', 390)
                    dialog.get_by_role('button', name='Delete alias', exact=True).focus(); page.keyboard.press('Enter')
                    expect(page.get_by_text('Alias deleted. The key is unchanged.', exact=True)).to_be_visible()
                    assert kms.list_aliases()['Aliases'] == []
                    assert kms.describe_key(KeyId=second['KeyId'])['KeyMetadata']['KeyState'] == 'Enabled'
                    for width, height in [(1440, 1000), (390, 844)]:
                        page.set_viewport_size({'width': width, 'height': height})
                        audit('list', width)
                    page.set_viewport_size({'width': 1440, 'height': 1000})
                    page.get_by_role('button', name='Create alias', exact=True).click()
                    page.get_by_label('Alias name', exact=True).fill('dummy-old-region-draft')
                    page.get_by_role('button', name='us-east-1', exact=True).click()
                    page.get_by_role('menuitem', name='us-west-2', exact=True).click()
                    expect(page.get_by_label('Alias name', exact=True)).to_have_count(0)
                    expect(page.get_by_text('No customer managed keys', exact=True)).to_be_visible()
                    page.get_by_role('button', name='us-west-2', exact=True).click()
                    page.get_by_role('menuitem', name='us-east-1', exact=True).click()
                    open_key(second['KeyId'])
                    # Exercise-identity remount clears the unsaved suffix/target;
                    # the replacement identity's UI reads work, native writes deny.
                    iam.create_role(RoleName='alias-ui-reader', AssumeRolePolicyDocument=json.dumps({'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': 'sts:AssumeRole', 'Principal': {'Service': 'lambda.amazonaws.com'}}]}))
                    iam.put_role_policy(RoleName='alias-ui-reader', PolicyName='ReadOnly', PolicyDocument=json.dumps({'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': ['kms:ListKeys', 'kms:DescribeKey', 'kms:ListAliases'], 'Resource': '*'}, {'Effect': 'Deny', 'Action': ['kms:CreateAlias', 'kms:UpdateAlias', 'kms:DeleteAlias'], 'Resource': '*'}]}))
                    page.get_by_role('button', name='Create alias', exact=True).click()
                    page.get_by_label('Alias name', exact=True).fill('dummy-old-identity-draft')
                    page.get_by_role('button', name='Account menu', exact=True).click()
                    page.get_by_role('menuitem', name='Choose identity', exact=True).click()
                    page.get_by_label('Principal name', exact=True).fill('alias-ui-reader')
                    page.get_by_role('dialog').get_by_role('button', name='Choose identity', exact=True).click()
                    expect(page.get_by_label('Alias name', exact=True)).to_have_count(0)
                    open_key(second['KeyId'])
                    page.get_by_role('button', name='Create alias', exact=True).click()
                    expect(page.get_by_label('Alias name', exact=True)).to_have_value('')
                    page.get_by_label('Alias name', exact=True).fill('denied')
                    expect(page.get_by_text(second['Arn'], exact=True)).to_be_visible()
                    with page.expect_response(lambda response: response.request.headers.get('x-amz-target') == 'TrentService.CreateAlias' and response.status == 403):
                        page.get_by_role('button', name='Create alias', exact=True).click()
                    # Cloudscape Alert exposes a group, not role=alert. Keep
                    # the observed native403 and exact visible error/header.
                    view = page.locator('[data-kms-aliases-view]')
                    expect(view.get_by_text('Alias operation failed', exact=True)).to_be_visible()
                    expect(view.get_by_text('403: Local KMS alias permission denied.', exact=True)).to_be_visible()
                    assert kms.list_aliases()['Aliases'] == []
                    page.get_by_role('button', name='Account menu', exact=True).click()
                    page.get_by_role('menuitem', name='Sign out', exact=True).click()
                    expect(page.get_by_role('heading', name='Open your local sandbox', exact=True)).to_be_visible()
                    assert kms.describe_key(KeyId=first['KeyId'])['KeyMetadata']['KeyId'] == first['KeyId'], 'Browser logout revoked independent SDK authority'
                    iam.delete_role_policy(RoleName='alias-ui-reader', PolicyName='ReadOnly'); iam.delete_role(RoleName='alias-ui-reader')
                    assert not outside and not errors
                    assert writes['TrentService.CreateAlias'] == 2 and writes['TrentService.UpdateAlias'] == 1 and writes['TrentService.DeleteAlias'] == 1
                finally:
                    try:
                        context.close()
                    finally:
                        browser.close()
        assert all(s['calls'] > 0 and s['calls'] == s['sends'] == s['responses'] for s in stats)
        print(json.dumps({'nativeAliasesUI': True, 'readFailureAfterACKRetriesReadOnly': True, 'actualSDKSameSQLite': True,
            'localePreservesDraft': True, 'regionClearsDraft': True, 'identityClearsDraft': True, 'olderNative500CannotOverrideNewer200': True, 'nativeIAM403': True,
            'keyboardTypedDelete': True, 'deleteModal1440and390': True, 'axe1440and390': True, 'outsideRequests': 0,
            'guardedSDKCounts': {key: sum(s[key] for s in stats) for key in ['calls', 'sends', 'responses']}, 'nativeBrowserMutations': dict(writes),
            'keyDeletion': 'unsupported; aliases only, independent keys remain until temporary DB cleanup'}))
    finally:
        for c in clients: c.close()


if __name__ == '__main__':
    main()
