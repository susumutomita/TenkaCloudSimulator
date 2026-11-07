"""Real served login layout acceptance. Execution evidence is recorded by the caller.

Call only with an already authorized fresh local page/context and real served
new asset. No server startup, credentials, screenshots or external requests here.
The existing actual clipboard/file helpers remain mandatory and unchanged.
"""
from playwright.sync_api import expect


def double_computed_text(page):
    # Snapshot first so explicitly sizing a child never compounds its parent.
    # This controlled text-enlargement fixture is not an OS/browser zoom claim.
    page.evaluate('''() => {
      const sizes=[...document.querySelectorAll('body *')].map(n=>{
        const s=getComputedStyle(n);return[n,parseFloat(s.fontSize),parseFloat(s.lineHeight)];
      });
      for(const[n,size,line]of sizes){
        if(Number.isFinite(size))n.style.setProperty('font-size',(size*2)+'px','important');
        if(Number.isFinite(line))n.style.setProperty('line-height',(line*2)+'px','important');
      }
    }''')


def check_login_layout(page, login_url, assert_reflow, strict_axe):
    """No token argument: checks only the public blank sign-in surface.

    Caller must enforce same-origin routing, pinned axe, page error guards and
    owned browser cleanup. No skip for absent tool or failed reflow is allowed.
    """
    results=[]
    for width,height in [(1440,1000),(2048,1152),(390,844)]:
        for locale in ['en','ja']:
            for enlarged in [False,True]:
                page.set_viewport_size({'width':width,'height':height})
                page.goto(login_url)
                language=page.get_by_role('button',name='日本語' if locale=='ja' else 'English',exact=True)
                if language.is_enabled():language.click()
                title='ローカルサンドボックスにサインイン' if locale=='ja' else 'Open your local sandbox'
                expect(page.get_by_role('heading',name=title,exact=True)).to_be_visible()
                if enlarged:double_computed_text(page)
                page.evaluate('document.fonts.ready')
                page.evaluate('new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(()=>requestAnimationFrame(r))))')
                assert_reflow(page)
                strict_axe(page)  # Existing pinned strict tool, not an optional substitute.
                input=page.get_by_label('アクセストークン' if locale=='ja' else 'Access token',exact=True)
                action=page.get_by_role('button',name='サンドボックスを開く' if locale=='ja' else 'Open sandbox',exact=True)
                expect(input).to_be_visible();expect(action).to_be_visible()
                geometry=page.evaluate('''() => {
                  const c=document.querySelector('.simulator-login__content').getBoundingClientRect();
                  const root=parseFloat(getComputedStyle(document.documentElement).fontSize);
                  return{left:c.left,right:c.right,width:c.width,viewport:document.documentElement.clientWidth,root};
                }''')
                assert geometry['left']>=0 and geometry['right']<=geometry['viewport']
                assert abs(geometry['left']+geometry['width']/2-geometry['viewport']/2)<=2
                assert geometry['width']<=42*geometry['root']+2
                a=input.bounding_box();b=action.bounding_box()
                assert a and b and b['y']>=a['y']+a['height']
                # Font-relative bounds check grouping, not an asserted pixel clone.
                assert b['y']-a['y']-a['height']<=4*geometry['root']
                assert abs(b['x']-a['x'])<=2*geometry['root']
                results.append({'width':width,'locale':locale,'controlledText200Percent':enlarged,'geometry':geometry})
    return results


def run():
    import hashlib
    import json
    import os
    import shutil
    from pathlib import Path
    from urllib.parse import urlsplit
    from check import running, sync_playwright, REPORTS, assert_reflow

    axe = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((axe.parent / 'provenance.json').read_text())
    assert hashlib.sha256(axe.read_bytes()).hexdigest() == provenance['bundleSha256']
    with running(authenticated=True) as (endpoint, _), sync_playwright() as p:
        origin = urlsplit(endpoint)
        assert origin.scheme == 'http' and origin.hostname == '127.0.0.1' and origin.port
        browser = p.chromium.launch(
            executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),
            args=['--no-sandbox'])
        try:
            context = browser.new_context()
            try:
                outside, errors, submissions = [], [], []
                def guard(route):
                    actual = urlsplit(route.request.url)
                    if (actual.scheme, actual.netloc) == (origin.scheme, origin.netloc):
                        route.continue_()
                    else:
                        outside.append(True)
                        route.abort()
                context.route('**/*', guard)
                page = context.new_page()
                page.on('pageerror', lambda _: errors.append(True))
                page.on('request', lambda request: submissions.append(True)
                    if request.method == 'POST' else None)
                def strict_axe(target):
                    target.evaluate(axe.read_text())
                    violations = target.evaluate(
                        "async()=>{const r=await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}});return r.violations.map(v=>v.id)}")
                    assert violations == [], 'Login layout accessibility failed'
                results = check_login_layout(page, endpoint + '/_simulator/login', assert_reflow, strict_axe)
                assert len(results) == 12
                assert not outside and not errors and not submissions
                report = {'cases': results, 'actualEmbeddedUI': True,
                    'normalAndControlledText200Percent': True,
                    'localOnly': True, 'credentialEntryOrSubmission': False,
                    'renewalLifecycleVerified': False, 'actualDia1PasswordVerified': False}
                (REPORTS / 'login-session-layout.json').write_text(json.dumps(report, indent=2) + '\n')
                print(json.dumps(report))
            finally:
                context.close()
        finally:
            browser.close()


if __name__ == '__main__':
    run()
