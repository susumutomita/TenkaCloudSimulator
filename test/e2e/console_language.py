"""Actual embedded-console language contract; presentation never grants authority.

Uses real authenticated local HTTP and the canonical axe script. No cloud, mocked
backend, dependency install, storage-secret persistence or optional skip path.
"""
from console_locale import select_console_locale
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import time
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright
from check import REPORTS, running
from crossprovider_console import AZURE, GCP, VERSION, native
from reflow import assert_reflow

PREFERENCE = 'tenkacloud-simulator-locale'


def run_console_language():
    axe = Path(os.environ['AXE_SCRIPT']).read_text()
    audits, errors, outside, mutations = [], [], [], []
    with tempfile.TemporaryDirectory(prefix='console-language-') as directory:
        with running(db=Path(directory)/'language.sqlite', authenticated=True) as (endpoint, operator), sync_playwright() as p:
            gcp_name, azure_name, s3_name = 'language-gcp-fixture', 'language-azure-fixture', 'language-s3-fixture'
            native(endpoint, operator, 'POST', GCP+'/b?project=tenkacloud-local', {'name': gcp_name})
            native(endpoint, operator, 'PUT', AZURE+'/'+azure_name+'?restype=container', headers={'x-ms-version': VERSION}, expected=201)
            native(endpoint, operator, 'PUT', '/'+s3_name)
            blob_name, blob_body = 'language-native.txt', b'Original native fixture, never overwritten.'
            blob_path = AZURE+'/'+azure_name+'/'+blob_name
            native(endpoint, operator, 'PUT', blob_path, blob_body,
                   {'x-ms-version': VERSION, 'x-ms-blob-type': 'BlockBlob', 'Content-Type': 'text/plain'}, expected=201)

            def frames():
                return (native(endpoint, operator, 'GET', GCP+'/b?project=tenkacloud-local')[0],
                        native(endpoint, operator, 'GET', AZURE+'?comp=list', headers={'x-ms-version': VERSION})[0],
                        native(endpoint, operator, 'GET', '/')[0])

            baseline = frames()
            browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
            contexts = []
            held = []
            try:
                for width in (1440, 390):
                    # Browser language alone must not silently change the default.
                    context = browser.new_context(viewport={'width': width, 'height': 1000}, locale='ja-JP')
                    contexts.append(context)

                    def guard(route):
                        if route.request.url.startswith(endpoint+'/') or route.request.url.startswith(('blob:', 'data:')):
                            route.continue_()
                        else:
                            outside.append(True); route.abort()
                    context.route('**/*', guard)
                    page = context.new_page()
                    page.on('pageerror', lambda error: errors.append(True))
                    writes = []
                    page.on('request', lambda request: writes.append(request.method) if request.method not in ('GET', 'HEAD', 'OPTIONS') else None)

                    def audit(label):
                        assert_reflow(page)
                        page.evaluate(axe)
                        result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                        assert not result['violations'], [(v['id'], len(v['nodes'])) for v in result['violations']]
                        page.screenshot(path=str(REPORTS/f'language-{label}-{width}.png'), full_page=True, animations='disabled')
                        audits.append([label, width])

                    def locale(next_locale, audit_settings=False):
                        select_console_locale(page, next_locale, audit=audit if audit_settings else None,
                                              keyboard=audit_settings)
                        expect(page.locator('html')).to_have_attribute('lang', next_locale)
                        assert page.evaluate('(key)=>localStorage.getItem(key)', PREFERENCE) == next_locale

                    def provider(name):
                        current = page.locator('html').get_attribute('lang')
                        navigation = page.get_by_role('navigation', name='クラウドプロバイダー' if current == 'ja' else 'Cloud provider', exact=True)
                        selected = navigation.get_by_role('button', name=name, exact=True)
                        selected.focus(); selected.press('Enter')
                        expect(selected).to_have_attribute('aria-pressed', 'true')
                        expect(selected).to_be_focused()  # Newly selected provider, never body.

                    def keyboard_catalog(provider_name, service_name):
                        current = page.locator('html').get_attribute('lang')
                        services = page.get_by_role('button', name='サービス' if current == 'ja' else 'Services', exact=True)
                        services.focus(); services.press('Enter')
                        search = page.get_by_role('searchbox', name=('サービスを検索:' if current == 'ja' else 'Search services in')+' '+provider_name, exact=True)
                        expect(search).to_be_focused(); search.fill(service_name)
                        # Home/End inside a text input edit its caret. They must
                        # neither move focus into the menu nor select a service.
                        search.press('Home'); expect(search).to_be_focused()
                        assert search.evaluate('(el)=>el.selectionStart===0 && el.selectionEnd===0'), 'Home must retain native search cursor editing'
                        search.press('End'); expect(search).to_be_focused()
                        assert search.evaluate('(el)=>el.selectionStart===el.value.length && el.selectionEnd===el.value.length'), 'End must retain native search cursor editing'
                        expect(search).to_have_value(service_name)
                        item = page.get_by_role('menuitem', name=service_name, exact=True)
                        search.press('ArrowDown'); expect(item).to_be_focused()
                        item.press('Enter')
                        expect(services).to_have_attribute('aria-expanded', 'false')
                        expect(services).to_be_focused()  # Selection removed the old menu item.

                    page.goto(endpoint+'/_simulator/console/')
                    expect(page.locator('html')).to_have_attribute('lang', 'en')
                    expect(page.get_by_role('heading', name='Open your local sandbox', exact=True)).to_be_visible()
                    expect(page.get_by_role('button', name='English', exact=True)).to_be_disabled()
                    page.get_by_role('button', name='日本語', exact=True).click()
                    expect(page.get_by_role('heading', name='ローカルサンドボックスにサインイン', exact=True)).to_be_visible()
                    expect(page.get_by_label('アクセストークン', exact=True)).to_have_attribute('type', 'password')
                    audit('login-ja')  # Token field is still empty; no secret screenshot.
                    page.get_by_role('button', name='English', exact=True).click()
                    assert not writes, 'Language buttons must not submit a login request'
                    page.get_by_label('Access token', exact=True).fill(operator)
                    page.get_by_role('button', name='Open sandbox', exact=True).click()
                    expect(page.get_by_role('button', name='Account menu', exact=True)).to_be_visible()
                    cookie = [c for c in context.cookies(endpoint) if c['name'].startswith('tcsim_')]
                    assert len(cookie) == 1 and cookie[0]['value'] != operator
                    write_count = len(writes)

                    def unchanged_authority():
                        current = [c for c in context.cookies(endpoint) if c['name'].startswith('tcsim_')]
                        assert [(c['name'], c['value']) for c in current] == [(c['name'], c['value']) for c in cookie], 'Language must not mint or replace the browser credential'
                        assert len(writes) == write_count, 'Presentation changes must not submit native mutations or login requests'

                    # A real controlled AWS form survives a user language change.
                    page.get_by_role('button', name='Create bucket', exact=True).click()
                    draft = 'unsubmitted-aws-'+str(width)
                    page.get_by_label('Bucket name', exact=True).fill(draft)
                    locale('ja', audit_settings=True)
                    expect(page.get_by_role('heading', name='バケットを作成', exact=True)).to_be_visible()
                    expect(page.get_by_label('バケット名', exact=True)).to_have_value(draft)
                    unchanged_authority(); audit('aws-dirty-ja')
                    page.get_by_role('button', name='キャンセル', exact=True).click()
                    expect(page.get_by_role('heading', name=re.compile(r'^汎用バケット \(1\)$'))).to_be_visible()

                    provider('Google Cloud')
                    view = page.get_by_role('region', name='Cloud Storage', exact=True)
                    expect(view.get_by_role('heading', name='バケット', exact=True)).to_be_visible()
                    expect(view.get_by_role('button', name=gcp_name, exact=True)).to_be_visible()
                    keyboard_catalog('Google Cloud', 'Cloud Storage')
                    scope = page.locator('.gcp-console-scope')
                    expect(scope).to_contain_text('tenkacloud-local')
                    expect(scope).to_contain_text('プロジェクト切替は未対応')
                    expect(scope.locator('input,select,[contenteditable="true"]')).to_have_count(0)
                    expect(page.get_by_role('combobox', name='リージョン', exact=True)).to_have_count(0)
                    view.get_by_role('button', name='バケットを作成', exact=True).click()
                    draft = 'unsubmitted-gcp-'+str(width)
                    view.get_by_label('バケット名', exact=True).fill(draft)
                    locale('en')
                    expect(view.get_by_label('Bucket name', exact=True)).to_have_value(draft)
                    view.get_by_role('button', name='Continue', exact=True).click()
                    view.get_by_role('radio', name='Region', exact=True).check()
                    view.get_by_label('Location', exact=True).select_option('US-EAST1')
                    locale('ja')
                    expect(view.get_by_role('heading', name='データの保存場所を選択', exact=True)).to_be_visible()
                    expect(view.get_by_label('ロケーション', exact=True)).to_have_value('US-EAST1')
                    unchanged_authority(); audit('gcp-dirty-ja')
                    view.get_by_role('button', name='戻る', exact=True).click()
                    expect(view.get_by_label('バケット名', exact=True)).to_have_value(draft)
                    view.get_by_role('button', name='キャンセル', exact=True).click()
                    assert frames() == baseline, 'Cancelled localized forms changed real native state'

                    # Hold an ACTUAL successful server response. Switching language
                    # during the request must not replay it or leave a stale notice.
                    locale('en')
                    created = 'language-created-'+str(width)
                    view.get_by_role('button', name='Create bucket', exact=True).click()
                    view.get_by_label('Bucket name', exact=True).fill(created)
                    for _ in range(4):
                        view.get_by_role('button', name='Continue', exact=True).click()

                    def hold_create(route):
                        if route.request.method != 'POST':
                            route.fallback(); return
                        target = urlsplit(route.request.url)
                        assert target.scheme == 'http' and target.netloc == urlsplit(endpoint).netloc
                        assert target.path == GCP+'/b' and target.query == 'project=tenkacloud-local'
                        response = route.fetch()
                        assert response.status == 200
                        held.append((route, response))
                    pattern = re.compile(re.escape(endpoint+GCP+'/b')+r'\?')
                    page.route(pattern, hold_create)
                    view.get_by_role('button', name='Create', exact=True).click()
                    deadline = time.monotonic()+10
                    while not held and time.monotonic() < deadline:
                        page.wait_for_timeout(20)
                    assert len(held) == 1, 'Expected one actual held native create response'
                    assert json.loads(native(endpoint, operator, 'GET', GCP+'/b/'+created)[0])['name'] == created
                    write_count += 1
                    locale('ja'); unchanged_authority()
                    route, response = held.pop(); route.fulfill(response=response)
                    expect(view.locator('.gcp-storage-notice')).to_contain_text('バケット '+created+' を作成しました。')
                    expect(view.get_by_role('button', name=created, exact=True)).to_be_visible()
                    locale('en')
                    expect(view.locator('.gcp-storage-notice')).to_contain_text('Bucket '+created+' created.')
                    unchanged_authority(); audit('gcp-notice-en')
                    page.unroute(pattern, hold_create)
                    native(endpoint, operator, 'DELETE', GCP+'/b/'+created, expected=204)

                    provider('Azure')
                    azure = page.locator('.azure-blob-view')
                    expect(azure.get_by_role('link', name=azure_name, exact=True)).to_be_visible()
                    keyboard_catalog('Azure', 'Blob Storage')
                    expect(azure.locator('.azure-resource-heading').get_by_role('heading', level=1)).to_contain_text('tenkacloudlocal')
                    expect(page.get_by_role('combobox', name='Storage account', exact=True)).to_have_count(0)
                    expect(page.get_by_role('combobox', name='Region', exact=True)).to_have_count(0)
                    expect(azure.locator('.azure-local-banner')).to_contain_text('This account is host-configured')
                    locale('ja')
                    expect(azure.get_by_role('button', name='+ コンテナー', exact=True)).to_be_visible()
                    expect(azure.get_by_role('link', name=azure_name, exact=True)).to_be_visible()
                    azure.get_by_role('button', name='+ コンテナー', exact=True).click()
                    pane = page.get_by_role('dialog', name='新しいコンテナー', exact=True)
                    expect(pane.get_by_role('textbox', name='名前', exact=True)).to_be_focused()
                    azure_draft = 'unsubmitted-azure-'+str(width)
                    pane.get_by_role('textbox', name='名前', exact=True).fill(azure_draft)
                    pane.get_by_role('combobox', name='Language / 言語', exact=True).select_option('en')
                    pane = page.get_by_role('dialog', name='New container', exact=True)
                    expect(pane.get_by_role('textbox', name='Name', exact=True)).to_have_value(azure_draft)
                    pane.get_by_role('combobox', name='Language / 言語', exact=True).select_option('ja')
                    pane = page.get_by_role('dialog', name='新しいコンテナー', exact=True)
                    expect(pane.get_by_role('textbox', name='名前', exact=True)).to_have_value(azure_draft)
                    unchanged_authority(); audit('azure-pane-ja')
                    pane.get_by_role('button', name='キャンセル', exact=True).click()
                    locale('en')
                    azure.get_by_role('link', name=azure_name, exact=True).click()
                    azure.get_by_role('button', name='Upload', exact=True).click()
                    pane = page.get_by_role('dialog', name='Upload blob', exact=True)
                    pane.get_by_label('Files', exact=True).set_input_files({'name':'language.txt','mimeType':'text/plain','buffer':b'Refused replacement'})
                    pane.get_by_label('Blob name', exact=True).fill(blob_name)
                    pane.get_by_role('combobox', name='Language / 言語', exact=True).select_option('ja')
                    pane = page.get_by_role('dialog', name='BLOB のアップロード', exact=True)
                    expect(pane.get_by_label('BLOB 名', exact=True)).to_have_value(blob_name)
                    assert pane.get_by_label('ファイル', exact=True).evaluate('(el)=>el.files.length===1 && el.files[0].name==="language.txt"')
                    pane.get_by_role('combobox', name='Language / 言語', exact=True).select_option('en')
                    pane = page.get_by_role('dialog', name='Upload blob', exact=True)
                    # A genuine refused native conditional write supplies the error.
                    # Never replace an HTTP response to fabricate translated success.
                    with page.expect_response(lambda response: response.request.method=='PUT' and urlsplit(response.url).path==blob_path) as refusal:
                        pane.get_by_role('button', name='Upload', exact=True).click()
                    assert refusal.value.status == 412
                    write_count += 1
                    expect(pane.get_by_role('alert')).to_have_text('The blob changed after you selected it. Refresh before retrying.')
                    pane.get_by_role('combobox', name='Language / 言語', exact=True).select_option('ja')
                    pane = page.get_by_role('dialog', name='BLOB のアップロード', exact=True)
                    expect(pane.get_by_role('alert')).to_have_text('選択後に BLOB が変更されました。更新してから再試行してください。')
                    expect(pane.get_by_label('BLOB 名', exact=True)).to_have_value(blob_name)
                    assert pane.get_by_label('ファイル', exact=True).evaluate('(el)=>el.files.length===1 && el.files[0].name==="language.txt"')
                    unchanged_authority(); audit('azure-error-ja')
                    pane.get_by_role('button', name='キャンセル', exact=True).click()
                    azure.get_by_role('checkbox', name='選択: '+blob_name, exact=True).check()
                    azure.get_by_role('button', name='削除', exact=True).click()
                    pane = page.get_by_role('dialog', name='BLOB の削除', exact=True)
                    pane.get_by_label('削除の確認: '+blob_name+' と入力', exact=True).fill(blob_name)
                    pane.get_by_role('combobox', name='Language / 言語', exact=True).select_option('en')
                    pane = page.get_by_role('dialog', name='Delete blob', exact=True)
                    expect(pane.get_by_label('Type '+blob_name+' to confirm deletion', exact=True)).to_have_value(blob_name)
                    expect(pane.get_by_role('button', name='Delete', exact=True)).to_be_enabled()
                    pane.get_by_role('button', name='Cancel', exact=True).click()
                    unchanged_authority()
                    assert native(endpoint, operator, 'GET', blob_path, headers={'x-ms-version': VERSION})[0] == blob_body
                    assert frames() == baseline, 'Localized navigation, refusal or cancelled panes changed native data'
                    locale('ja'); page.reload()
                    expect(page.locator('html')).to_have_attribute('lang', 'ja')
                    expect(page.locator('.azure-blob-view').get_by_role('button', name='+ コンテナー', exact=True)).to_be_visible()
                    provider('AWS')
                    expect(page.get_by_role('heading', name=re.compile(r'^汎用バケット \(1\)$'))).to_be_visible()
                    locale('en'); page.reload()
                    expect(page.get_by_role('heading', name=re.compile(r'^General purpose buckets \(1\)$'))).to_be_visible()
                    expect(page.locator('html')).to_have_attribute('lang', 'en')
                    unchanged_authority(); mutations.append(len(writes)-1)

                    # Separate browser storage must not inherit the saved choice.
                    isolated = browser.new_context(locale='ja-JP')
                    contexts.append(isolated); isolated.route('**/*', guard)
                    fresh = isolated.new_page(); fresh.on('pageerror', lambda error: errors.append(True)); fresh.goto(endpoint+'/_simulator/console/')
                    expect(fresh.get_by_role('heading', name='Open your local sandbox', exact=True)).to_be_visible()
                    expect(fresh.locator('html')).to_have_attribute('lang', 'en')
                    expect(fresh.get_by_label('Access token', exact=True)).to_have_value('')
                    isolated.close(); contexts.remove(isolated)
                    context.close(); contexts.remove(context)

                assert not errors, 'Browser page errors (secret-bearing error text is not emitted)'
                assert not outside, 'Unexpected request outside the owned local CLI'
                assert frames() == baseline
                native(endpoint, operator, 'DELETE', GCP+'/b/'+gcp_name, expected=204)
                native(endpoint, operator, 'DELETE', AZURE+'/'+azure_name+'?restype=container', headers={'x-ms-version': VERSION}, expected=202)
                native(endpoint, operator, 'DELETE', '/'+s3_name, expected=204)
                report = {'actualEmbeddedConsole': True, 'viewports': [1440, 390], 'defaultEnglishDespiteJapaneseBrowser': True,
                          'loginEnglishJapanese': True, 'providerPresentationEnglishJapanese': ['AWS', 'Google Cloud', 'Azure'],
                          'savedPreferenceReloadAndOriginStorageIsolation': True, 'keyboardProviderAndServiceFocus': True,
                          'searchHomeEndNativeCursor': True, 'awsAndGcpDirtyDraftPreserved': True,
                          'gcpPendingNativeCreateNotReplayedAndNoticeRetranslated': True,
                          'azureNativePaneLanguagePreservesDraftFileErrorAndConfirmation': True,
                          'configuredNonAwsScopeReadOnly': True, 'nativeStateAndCredentialFrames': True,
                          'explicitUiApiWritesPerViewport': mutations, 'axeStates': audits, 'outsideRequests': 0,
                          'pageErrors': 0, 'actualCloud': False, 'cleanup': 'DELETED'}
                (REPORTS/'console-language.json').write_text(json.dumps(report, indent=2)+'\n')
                print(json.dumps(report))
            finally:
                for route, _ in held:
                    route.abort()
                for context in contexts:
                    context.close()
                browser.close()


if __name__ == '__main__':
    run_console_language()
