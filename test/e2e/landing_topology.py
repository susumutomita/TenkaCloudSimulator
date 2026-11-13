"""Public embedded LP regression: eight Chromium contexts, no browser credentials.

Uses the normal conformance fixture's loopback endpoint and pinned development
axe-core. Native HTML-in-Canvas rendering is not certified by these checks.
"""
from html import unescape
from pathlib import Path
from urllib.parse import urlsplit
import hashlib
import json
import os
import re
import shutil

PREFIX = '/_simulator/'
ASSET_PREFIX = PREFIX + 'landing/'
FILES = {'index.html', 'landing.css', 'landing.mjs', 'topology-data.mjs', 'topology-canvas.mjs'}
PROVIDERS = {'aws': 'aws-lambda', 'gcp': 'gcp-storage', 'azure': 'azure-blob'}
NODE_IDS = {'aws-gateway', 'aws-lambda', 'aws-s3', 'aws-ssm', 'aws-iam',
            'gcp-storage', 'gcp-object', 'azure-container', 'azure-blob'}
ROOT = Path(__file__).resolve().parents[2]


def contract():
    directory = ROOT / 'internal/server/landing'
    assets = {}
    for name in FILES:
        path = directory / name
        assert path.is_file() and not path.is_symlink(), 'Missing canonical landing source'
        assets[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    text = (directory / 'topology-data.mjs').read_text()
    marker = 'const data = '
    assert text.count(marker) == 1
    data, end = json.JSONDecoder().raw_decode(text.split(marker, 1)[1])
    assert text.split(marker, 1)[1][end:].startswith(';')
    assert {n['id'] for n in data['nodes']} == NODE_IDS and len(data['nodes']) == 9
    assert {p['id']: p['defaultNode'] for p in data['providers']} == PROVIDERS
    assert len(data['edges']) == 6
    html = (directory / 'index.html').read_text()
    title = re.search(r'<title>([^<]+)</title>', html)
    assert title
    return {**data, 'title': unescape(title[1]), 'assets': assets,
            'navigationAssets': {'app.css': hashlib.sha256((ROOT/'internal/server/web/app.css').read_bytes()).hexdigest()},
            'links': [{'selector': '.header-cta', 'href': '/_simulator/console/'},
                      {'selector': '.hero-actions .button--primary', 'href': '/_simulator/console/'},
                      {'selector': '.hero-actions .button--secondary', 'href': '/_simulator/docs/'},
                      {'selector': '.start-actions .button--primary', 'href': '/_simulator/console/'},
                      {'selector': '.start-actions .text-link', 'href': '/_simulator/docs/'},
                      {'selector': '.site-footer > a:last-child', 'href': '/_simulator/licenses/'}]}


def one(locator):
    from playwright.sync_api import expect
    expect(locator).to_have_count(1)
    return locator

def assert_source_content(page, oracle, js_enabled):
    from playwright.sync_api import expect
    assert page.title() == oracle['title']
    expect(one(page.locator('#hero-title'))).to_be_visible()
    expect(one(page.locator('#explore-title'))).to_be_visible()
    root = one(page.locator('[data-cloud-topology]'))
    expect(root).to_be_visible()
    expect(root.locator('a[data-node-id]')).to_have_count(9)
    expect(root.locator('[data-node-detail]')).to_have_count(9)
    expect(root.locator('[data-provider-group]')).to_have_count(3)
    for node in oracle['nodes']:
        anchor = one(root.locator('a[data-node-id=' + json.dumps(node['id']) + ']'))
        assert anchor.get_attribute('href') == '#detail-' + node['id']
        expect(one(anchor.locator('strong'))).to_have_text(node['title'])
        expect(one(anchor.locator('small'))).to_have_text(node['subtitle'])
        detail = one(root.locator('[data-node-detail=' + json.dumps(node['id']) + ']'))
        expect(one(detail.locator('h3'))).to_have_text(node['title'])
        expect(detail.get_by_text(node['description'], exact=True)).to_have_count(1)
        expect(detail.get_by_text(node['limits'], exact=True)).to_have_count(1)
        if not js_enabled:
            expect(anchor).to_be_visible()
            expect(detail).to_be_visible()
    for provider in oracle['providers']:
        button = one(root.locator('button[data-provider=' + json.dumps(provider['id']) + ']'))
        expect(button).to_have_text(provider['name'])
        (expect(button).to_be_enabled if js_enabled else expect(button).to_be_disabled)()
        if not js_enabled:
            expect(one(root.locator('[data-provider-group=' + json.dumps(provider['id']) + ']'))).to_be_visible()
    for link in oracle['links']:
        target = one(page.locator(link['selector']))
        expect(target).to_be_visible()
        assert target.get_attribute('href') == link['href']
    expect(root.locator('svg.topology-lines path')).to_have_count(6)
    expect(one(root.locator('.section-heading > p'))).to_contain_text('保存済みリソースの表示ではありません。')
    expect(one(root.locator('.edge-note'))).to_contain_text('S3 からの自動トリガーは含みません。')
    canvas = one(root.locator('canvas[data-topology-canvas]'))
    assert canvas.get_attribute('aria-hidden') == 'true'
    assert canvas.get_attribute('inert') is not None
    expect(canvas.locator('a[href],input,button,select,textarea,[contenteditable=true]')).to_have_count(0)
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth + 1')
    return root, canvas

def assert_selection(root, provider, selected):
    from playwright.sync_api import expect
    expect(one(root.locator('button[data-provider=' + json.dumps(provider) + ']'))).to_have_attribute('aria-pressed', 'true')
    expect(one(root.locator('a[data-node-id=' + json.dumps(selected) + ']'))).to_have_attribute('aria-current', 'true')
    for other in PROVIDERS:
        group = one(root.locator('[data-provider-group=' + json.dumps(other) + ']'))
        (expect(group).to_be_visible if other == provider else expect(group).to_be_hidden)()
        if other != provider:
            expect(one(root.locator('button[data-provider=' + json.dumps(other) + ']'))).to_have_attribute('aria-pressed', 'false')
    details = root.locator('[data-node-detail]:visible')
    expect(details).to_have_count(1)
    assert details.get_attribute('data-node-detail') == selected

def keyboard_activate(page, target, key):
    # Real Tab navigation, not focus()/force()/arbitrary first match.
    for _ in range(64):
        if target.evaluate('(element) => document.activeElement === element'):
            break
        page.keyboard.press('Tab')
    assert target.evaluate('(element) => document.activeElement === element')
    target.press(key)
    node_id = target.get_attribute('data-node-id')
    if node_id is not None:
        from playwright.sync_api import expect
        assert key == 'Enter'
        assert node_id in {'aws-gateway','aws-lambda','aws-s3','aws-ssm','aws-iam','gcp-storage','gcp-object','azure-container','azure-blob'}
        detail = one(page.locator('#detail-' + node_id))
        expect(detail).to_be_visible()
        expect(detail).to_have_attribute('tabindex', '-1')
        assert detail.evaluate('(element) => document.activeElement === element')
        assert urlsplit(page.url).fragment == 'detail-' + node_id
        expect(target).to_have_attribute('aria-current', 'true')
        # Native fragment focus must continue into the selected article's CTA.
        page.keyboard.press('Tab')
        cta = one(detail.locator('a.text-link'))
        expect(cta).to_be_visible()
        assert cta.get_attribute('href') == '/_simulator/console/'
        assert cta.evaluate('(element) => document.activeElement === element')
        expect(target).to_have_attribute('aria-current', 'true')
    else:
        # Space on provider buttons retains the original focus contract.
        assert target.evaluate('(element) => document.activeElement === element')

def interactive_case(page, root):
    assert_selection(root, 'aws', 'aws-lambda')
    actions = []
    for provider in ('gcp', 'azure', 'aws'):
        button = one(root.locator('button[data-provider=' + json.dumps(provider) + ']'))
        keyboard_activate(page, button, 'Space')
        assert_selection(root, provider, PROVIDERS[provider])
        actions.append({'kind': 'keyboard-provider', 'provider': provider, 'selected': PROVIDERS[provider]})
    for node in ('aws-s3', 'aws-ssm', 'aws-iam'):
        target = one(root.locator('a[data-node-id=' + json.dumps(node) + ']'))
        keyboard_activate(page, target, 'Enter')
        assert_selection(root, 'aws', node)
        actions.append({'kind': 'keyboard-node', 'provider': 'aws', 'selected': node, 'exactArticleFocused': True, 'nextRealTabToArticleCTA': True})
    return actions

def fragment_and_modifier_case(page, root, origin, oracle, responses, errors, report):
    """One actual 1440/default navigation probe after all old case assertions."""
    from playwright.sync_api import expect, TimeoutError as PlaywrightTimeoutError
    assert_selection(root, 'aws', 'aws-iam')
    captured, served = [], []
    def capture(response):
        captured.append(response)
    def drain():
        # Freeze response bodies while every owning document/session is alive.
        for response in captured:
            path = urlsplit(response.url).path
            name = 'index.html' if path == PREFIX else path.removeprefix(ASSET_PREFIX)
            assert name in oracle['assets'] and response.status == 200
            digest = hashlib.sha256(response.body()).hexdigest()
            assert digest == oracle['assets'][name]
            served.append({'path': path, 'SHA256': digest, 'status': response.status})
        captured.clear()
    page.context.on('response', capture)
    popup = None
    try:
        document_response = next(r for r in responses if urlsplit(r.url).path == PREFIX)
        directives = [part.strip().split() for part in document_response.headers.get('content-security-policy', '').split(';')]
        sandbox = next(parts for parts in directives if parts and parts[0] == 'sandbox')
        assert 'allow-popups' not in sandbox and 'allow-popups-to-escape-sandbox' not in sandbox
        page.evaluate("""() => {
            window.__lpModifierProbe = null;
            document.addEventListener('click', event => {
                if (!(event.target instanceof Element) || !event.target.closest('a[data-node-id="aws-s3"]')) return;
                const result = {trusted:event.isTrusted, ctrl:event.ctrlKey, meta:event.metaKey,
                    shift:event.shiftKey, alt:event.altKey, button:event.button};
                queueMicrotask(() => { result.defaultPrevented = event.defaultPrevented; window.__lpModifierProbe = result; });
            }, {once:true});
        }""")
        target = one(root.locator('a[data-node-id="aws-s3"]'))
        original_url = page.url
        original_pages = len(page.context.pages)
        no_popup_observed = False
        try:
            with page.context.expect_page(timeout=1000) as opened:
                target.click(modifiers=['Control'])
            popup = opened.value
        except PlaywrightTimeoutError:
            no_popup_observed = True
        observed_popup = popup is not None
        def url_kind(value):
            if value == 'about:blank':
                return 'ABOUT_BLANK'
            parsed = urlsplit(value)
            if parsed.scheme + '://' + parsed.netloc == origin and parsed.path == PREFIX and not parsed.query:
                if parsed.fragment in {'detail-aws-gateway','detail-aws-lambda','detail-aws-s3','detail-aws-ssm','detail-aws-iam','detail-gcp-storage','detail-gcp-object','detail-azure-container','detail-azure-blob'}:
                    return 'KNOWN_LP_NODE_FRAGMENT'
                return 'PUBLIC_LP_OTHER_FRAGMENT'
            return 'OTHER_REDACTED'
        observation = {'originalPageCount': original_pages, 'afterCtrlPageCount':len(page.context.pages),
            'contextPageObservedWithin1000ms':observed_popup,
            'pageURLKindsBeforeClose':[url_kind(p.url) for p in page.context.pages[:4]],
            'pageKindsTruncated':len(page.context.pages)>4}
        (report / 'modifier-page-observation.json').write_text(json.dumps(observation,indent=2)+'\n')
        if popup is not None:
            popup.on('pageerror', lambda error: errors.append(type(error).__name__))
            popup.wait_for_load_state('networkidle')
            expect(popup).to_have_url(origin + PREFIX + '#detail-aws-s3')
            popup_root, _ = assert_source_content(popup, oracle, True)
            assert_selection(popup_root, 'aws', 'aws-s3')
        probe = page.evaluate('window.__lpModifierProbe')
        assert probe == {'trusted':True,'ctrl':True,'meta':False,'shift':False,'alt':False,'button':0,'defaultPrevented':False}
        assert page.url == original_url
        assert_selection(root, 'aws', 'aws-iam')
        drain()
        if popup is not None:
            with popup.expect_event('close', timeout=5000):
                popup.close()
            assert popup.is_closed()
            popup = None
        observation['afterOwnedClosePageCount'] = len(page.context.pages)
        observation['pageURLKindsAfterClose'] = [url_kind(p.url) for p in page.context.pages[:4]]
        (report / 'modifier-page-observation.json').write_text(json.dumps(observation,indent=2)+'\n')
        assert len(page.context.pages) == original_pages
        # Fresh document initial-fragment selection, then real hashchange events.
        page.goto('about:blank')
        response = page.goto(origin + PREFIX + '#detail-gcp-object', wait_until='networkidle')
        assert response and response.status == 200
        root, _ = assert_source_content(page, oracle, True)
        assert_selection(root, 'gcp', 'gcp-object')
        page.evaluate("location.hash = '#detail-azure-blob'")
        expect(page).to_have_url(origin + PREFIX + '#detail-azure-blob')
        assert_selection(root, 'azure', 'azure-blob')
        page.evaluate("location.hash = '#detail-unknown'")
        expect(page).to_have_url(origin + PREFIX + '#detail-unknown')
        assert_selection(root, 'azure', 'azure-blob')
        page.go_back(wait_until='networkidle')
        expect(page).to_have_url(origin + PREFIX + '#detail-azure-blob')
        assert_selection(root, 'azure', 'azure-blob')
        page.go_back(wait_until='networkidle')
        expect(page).to_have_url(origin + PREFIX + '#detail-gcp-object')
        assert_selection(root, 'gcp', 'gcp-object')
        drain()
        # A new unknown-fragment document must retain the documented default.
        page.goto('about:blank')
        response = page.goto(origin + PREFIX + '#unrelated-section', wait_until='networkidle')
        assert response and response.status == 200
        root, _ = assert_source_content(page, oracle, True)
        assert_selection(root, 'aws', 'aws-lambda')
        target = one(root.locator('a[data-node-id="aws-s3"]'))
        target.click()
        expect(page).to_have_url(origin + PREFIX + '#detail-aws-s3')
        assert_selection(root, 'aws', 'aws-s3')
        drain()
    finally:
        page.context.remove_listener('response', capture)
        if popup is not None:
            popup.close()
    return {'coverage': 'One trusted Chromium 1440/default Ctrl-click remains uncancelled by application. Popup behavior is observed within1000ms, not assumed from sandbox or advertised as cross-browser/newtab support. Other OS modifier gestures unverified.',
        'ctrlClickTrustedAndUncancelled': True, 'sandboxDisallowsPopups': True,
        'popupObservedWithin1000ms': observed_popup, 'noPopupObservedWithin1000ms': no_popup_observed,
        'popupNewTabSupport': 'OBSERVATION_ONLY_NOT_A_CAPABILITY_GUARANTEE',
        'originalCtrlSelectionPreserved': True,
        'initialKnownFragment': True, 'knownHashchange': True, 'unknownHashPreserves': True,
        'newUnknownFragmentUsesDefault': True, 'plainClickFollowsFragment': True, 'browserBackRestoresKnownFragment': True,
        'browserServedAssets': served}

def public_cta_case(page, context, origin, oracle, responses):
    """Only the 1440/no-JS context, no login POST or credential handling."""
    from playwright.sync_api import expect
    assert not context.cookies(), 'anonymous LP must not create authority cookies'
    target = one(page.locator('.header-cta'))
    for _ in range(64):
        if target.evaluate('(element) => document.activeElement === element'):
            break
        page.keyboard.press('Tab')
    assert target.evaluate('(element) => document.activeElement === element')
    start = len(responses)
    with page.expect_response(lambda r: urlsplit(r.url).path == '/_simulator/console/') as redirected:
        target.press('Enter')
    redirect = redirected.value
    assert redirect.status == 303
    assert redirect.headers.get('location') == '/_simulator/login'
    expect(page).to_have_url(origin + '/_simulator/login')
    page.wait_for_load_state('networkidle')
    assert page.title() == 'TenkaCloud Simulator'
    assert not context.cookies(), 'public navigation must not authenticate'
    served = []
    for response in responses[start:]:
        path = urlsplit(response.url).path
        assert path in {'/_simulator/console/', '/_simulator/login', '/_simulator/console/app.css'}
        if path == '/_simulator/console/':
            assert response.status == 303
        elif path == '/_simulator/login':
            assert response.status == 200
            assert response.headers.get('content-type', '').split(';')[0] == 'text/html'
        else:
            assert response.status == 200
            assert hashlib.sha256(response.body()).hexdigest() == oracle['navigationAssets']['app.css']
        served.append({'path': path, 'status': response.status})
    assert {r['path'] for r in served} >= {'/_simulator/console/', '/_simulator/login'}
    return {'coverage': 'One anonymous no-JS actual CTA follow; verifies native redirect/login HTML, not React login rendering or authentication',
        'redirect303': True, 'exactLoginLocation': True, 'loginHTML200': True, 'authorityCookies': 0,
        'served': served}

STABLE_LAYOUT = '() => new Promise(resolve => {\n    const started=performance.now(),maxMs=5000,requiredFrames=3;\n    const ids=[\'aws-gateway\',\'aws-lambda\',\'aws-s3\',\'aws-ssm\',\'aws-iam\',\'gcp-storage\',\'gcp-object\',\'azure-container\',\'azure-blob\'];\n    let previous=null,stableFrames=0,frames=0,last=null,first=null,done=false,raf=null,fontsReady=false;\n    const rect=e=>{const r=e.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height};};\n    const finish=status=>{\n        if(done)return;done=true;clearTimeout(timer);if(raf!==null)cancelAnimationFrame(raf);\n        resolve({status,maxMs,requiredFrames,stableFrames,frames,fontsReady,elapsedMs:Math.round((performance.now()-started)*1000)/1000,\n            first,last,qualification:\'Actual unmodified scroll/layout measured across requestAnimationFrame after document.fonts.ready; no CSS, motion, scroll or focus override.\'});\n    };\n    const timer=setTimeout(()=>finish(\'UNSTABLE_OR_FONTS_TIMEOUT\'),maxMs);\n    const tick=()=>{\n        if(done)return;\n        const nodes=[];\n        for(const id of ids){\n            const matches=document.querySelectorAll(\'a[data-node-id="\'+id+\'"]\');\n            if(matches.length!==1){finish(\'SOURCE_NODE_INVENTORY_UNAVAILABLE\');return;}\n            nodes.push({id,rect:rect(matches[0])});\n        }\n        const paths=Array.from(document.querySelectorAll(\'svg.topology-lines path\'));\n        if(paths.length!==6){finish(\'SOURCE_PATH_INVENTORY_UNAVAILABLE\');return;}\n        const current=document.querySelector(\'a[data-node-id][aria-current="true"]\')?.getAttribute(\'data-node-id\');\n        if(!ids.includes(current)){finish(\'SOURCE_SELECTION_UNAVAILABLE\');return;}\n        const detail=document.getElementById(\'detail-\'+current);\n        if(!detail){finish(\'SOURCE_DETAIL_UNAVAILABLE\');return;}\n        last={scrollX,scrollY,viewportWidth:innerWidth,viewportHeight:innerHeight,\n            scrollWidth:document.documentElement.scrollWidth,scrollHeight:document.documentElement.scrollHeight,\n            nodes,paths:paths.map((p,index)=>({index,rect:rect(p)})),current,detailRect:rect(detail)};\n        if(first===null)first=last;\n        const signature=JSON.stringify(last);frames++;\n        stableFrames=signature===previous?stableFrames+1:1;previous=signature;\n        if(stableFrames>=requiredFrames){finish(\'STABLE\');return;}\n        if(performance.now()-started>=maxMs){finish(\'UNSTABLE_OR_FONTS_TIMEOUT\');return;}\n        raf=requestAnimationFrame(tick);\n    };\n    if(!document.fonts||!document.fonts.ready){finish(\'FONT_API_UNAVAILABLE\');return;}\n    document.fonts.ready.then(()=>{fontsReady=true;if(!done)raf=requestAnimationFrame(tick);},()=>finish(\'FONT_READINESS_REJECTED\'));\n})\n'


def audit(page, axe_source, report, name):
    page.evaluate(axe_source)
    stability = page.evaluate(STABLE_LAYOUT)
    (report/(name+'-layout.json')).write_text(json.dumps(stability, indent=2)+'\n')
    assert stability['status'] == 'STABLE' and stability['stableFrames'] >= 3 and stability['fontsReady']
    before = page.evaluate('({x:scrollX,y:scrollY})')
    result = page.evaluate("""async () => {
        const result = await axe.run(document, {runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}});
        const finding = v => ({id:v.id,nodes:v.nodes.length,details:v.nodes.map(n=>({
            target:n.target,summary:n.failureSummary,
            checks:[...n.any,...n.all,...n.none].map(c=>({id:c.id,message:c.message,data:c.data}))
        }))});
        return {violations:result.violations.map(finding),
                incomplete:result.incomplete.map(finding),passes:result.passes.length};
    }""")
    after = page.evaluate('({x:scrollX,y:scrollY})')
    # Save strict findings and unmodified scroll before raising a regression.
    record = {'axe': result, 'scrollBefore': before, 'scrollAfter': after}
    (report/(name+'-axe.json')).write_text(json.dumps(record, indent=2)+'\n')
    assert before == after, 'Accessibility audit changed the current scroll position'
    assert result['violations'] == [] and result['incomplete'] == [], record
    return record


def case(browser, origin, oracle, report, axe_source, width, mode):
    context = browser.new_context(viewport={'width':width,'height':900 if width == 1440 else 844},
        java_script_enabled=mode != 'no-js', reduced_motion='reduce' if mode == 'reduced-motion' else 'no-preference',
        service_workers='block')
    allowed = {PREFIX} | {ASSET_PREFIX+name for name in FILES if name != 'index.html'}
    blocked, forbidden, errors, responses = [], [], [], []
    navigation = False
    page = None
    primary = False
    name = str(width)+'-'+mode
    try:
        assert context.cookies() == [], 'Fresh public context has authority cookies'
        def guard(route):
            request = route.request
            url = urlsplit(request.url)
            actual_origin = url.scheme+'://'+url.netloc
            paths = {'/_simulator/console/','/_simulator/login','/_simulator/console/app.css'} if navigation else allowed
            if actual_origin != origin or request.method != 'GET' or url.path not in paths or url.query:
                forbidden.append({'method':request.method,'sameOrigin':actual_origin == origin})
                route.abort()
            elif mode == 'blocked-enhancement' and url.path == ASSET_PREFIX+'topology-canvas.mjs':
                blocked.append(url.path); route.abort()
            else:
                route.continue_()
        context.route('**/*', guard)
        page = context.new_page()
        page.set_default_timeout(10000)
        page.on('pageerror', lambda error: errors.append(type(error).__name__))
        page.on('response', lambda response: responses.append(response))
        response = page.goto(origin+PREFIX, wait_until='networkidle')
        assert response and response.status == 200
        root, canvas = assert_source_content(page, oracle, mode != 'no-js')
        record = {'width':width,'mode':mode,'applicationJavaScriptEnabled':mode != 'no-js'}
        if mode == 'no-js':
            assert canvas.get_attribute('hidden') is not None
        else:
            record['actions'] = interactive_case(page, root)
            record['currentAPI'] = page.evaluate("""() => {
                const c=document.createElement('canvas'); c.setAttribute('content','drawable');
                const ctx=c.getContext('2d'); return {content:c.content==='drawable',
                    draw:!!ctx&&typeof ctx.drawElementImage==='function',requestPaint:typeof c.requestPaint==='function'};
            }""")
            if not all(record['currentAPI'].values()) or mode == 'blocked-enhancement':
                assert canvas.get_attribute('hidden') is not None
            record['nativeDrawAcceptance'] = 'NOT_PROVED_BY_THIS_REGRESSION'
            record['audit'] = audit(page, axe_source, report, name)
        assert not errors and not forbidden
        if mode == 'blocked-enhancement':
            assert blocked == [ASSET_PREFIX+'topology-canvas.mjs']
        served = []
        for observed in responses:
            path = urlsplit(observed.url).path
            asset = 'index.html' if path == PREFIX else path.removeprefix(ASSET_PREFIX)
            assert asset in oracle['assets'] and observed.status == 200
            digest = hashlib.sha256(observed.body()).hexdigest()
            assert digest == oracle['assets'][asset], 'Browser received a stale landing asset'
            served.append({'path':path,'SHA256':digest,'status':observed.status})
        if mode in ('default','reduced-motion'):
            assert {item['path'] for item in served} == allowed
        record['browserServedAssets'] = served
        page.screenshot(path=str(report/(name+'.png')))
        if width == 1440 and mode == 'default':
            record['fragmentAndModifierNavigation'] = fragment_and_modifier_case(page,root,origin,oracle,responses,errors,report)
        if width == 1440 and mode == 'no-js':
            navigation = True
            record['publicCTANavigation'] = public_cta_case(page,context,origin,oracle,responses)
        assert context.cookies() == [], 'Public LP or CTA navigation created authority cookies'
        assert not errors and not forbidden, 'Late browser error or unexpected request'
        return {**record,'unexpectedRequests':forbidden,'pageErrors':errors,'blockedOwnedModule':blocked}
    except BaseException as error:
        primary = True
        diagnostic = {'width':width,'mode':mode,'primaryType':type(error).__name__,
                      'pageErrors':errors,'unexpectedRequests':forbidden,'blockedOwnedModule':blocked}
        if page is not None:
            try:
                page.screenshot(path=str(report/(name+'-failure-masked.png')), timeout=5000,
                                mask=[page.locator('input,textarea,[contenteditable]')])
            except Exception as secondary:
                diagnostic['screenshotErrorType'] = type(secondary).__name__
        (report/(name+'-failure.json')).write_text(json.dumps(diagnostic,indent=2)+'\n')
        raise
    finally:
        try:
            context.close()
        except Exception as error:
            (report/(name+'-context-cleanup.json')).write_text(json.dumps({'errorType':type(error).__name__})+'\n')
            if not primary:
                raise


def run_landing_topology(origin, report):
    """Endpoint only: never receives a fixture token or makes native API calls."""
    from playwright.sync_api import sync_playwright
    parsed = urlsplit(origin)
    assert parsed.scheme == 'http' and parsed.hostname == '127.0.0.1' and parsed.port
    assert not parsed.username and not parsed.password and not parsed.query and parsed.path in ('','/')
    origin = origin.rstrip('/')
    report = Path(report); report.mkdir(parents=True, exist_ok=True)
    oracle = contract()
    axe_path = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((axe_path.parent/'provenance.json').read_text())
    assert provenance['name'] == 'axe-core' and provenance['version'] == '4.11.0'
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance['bundleSha256']
    axe_source = axe_path.read_text()
    (report/'axe-provenance.json').write_text(json.dumps(provenance,indent=2)+'\n')
    results = []
    with sync_playwright() as p:
        executable = os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
        browser = p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox'])
        try:
            for width in (1440,390):
                for mode in ('no-js','default','reduced-motion','blocked-enhancement'):
                    results.append(case(browser,origin,oracle,report,axe_source,width,mode))
                    (report/'case-results.json').write_text(json.dumps(results,indent=2)+'\n')
        finally:
            browser.close()
    assert len(results) == 8
    assert {(r['width'],r['mode']) for r in results} == {(w,m) for w in (1440,390) for m in ('no-js','default','reduced-motion','blocked-enhancement')}
    summary = {'cases':8,'strictAxeStates':6,'unexpectedRequests':0,'authorityCookies':0,
               'nativeCanvasRendering':'NOT_CERTIFIED','caseResults':results}
    (report/'acceptance.json').write_text(json.dumps(summary,indent=2)+'\n')
    return summary
