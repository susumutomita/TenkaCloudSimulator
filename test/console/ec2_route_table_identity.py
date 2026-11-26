"""Browser-only regression against the embedded Console bundle with a controlled read transport.

No live IAM, signed-in server, native EC2 or network claim. The late response deliberately
ignores cancellation. Identity changes while the selected VPC/region stay the same.
Run with the repository's Playwright dependencies and an installed Chromium.
"""
import os
import shutil
from pathlib import Path

from playwright.sync_api import expect, sync_playwright


ORIGIN = 'http://127.0.0.1:4566'
APP = Path(__file__).resolve().parents[2] / 'internal/server/web/app.js'
VPC = 'vpc-' + '1' * 17
TRANSPORT = r"""
window.__routes = {calls: [], releases: {}, errors: []};
const fixture = window.__routes;
const xml = (name, body) => '<'+name+' xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">'+body+'</'+name+'>';
const reply = body => new Response(body,{status:200,headers:{'Content-Type':'text/xml'}});
const routes = label => reply(xml('DescribeRouteTablesResponse',
  '<requestId>fixture</requestId><routeTableSet><item><routeTableId>rtb-22222222222222222</routeTableId>'+
  '<vpcId>vpc-11111111111111111</vpcId><ownerId>000000000000</ownerId><routeSet/><associationSet/>'+
  '<tagSet><item><key>Scope</key><value>'+label+'</value></item></tagSet></item></routeTableSet>'));
window.fetch = async (url, opts={}) => {
  const identity=opts.headers?.['X-Simulator-Principal'] ?? 'learner-a';
  if (url === '/_simulator/v1/capabilities') return {ok:true,status:200,
    headers:{get:k=>k==='X-Simulator-Authentication'?'required':null},
    json:async()=>({principal:identity,identitySelection:true,services:{ec2:{console:true,
      operations:['DescribeInstances','DescribeVpcs','DescribeRouteTables'],routeTableReadback:'DescribeRouteTables'}}})};
  const params = new URLSearchParams(opts.body), action=params.get('Action');
  if (url!=='/' || opts.method!=='POST' || opts.headers['X-Simulator-Service']!=='ec2' ||
      opts.headers['X-Simulator-Region']!=='us-east-1' || [...params.keys()].join(',')!==(action==='DescribeRouteTables'?'Action,Version,MaxResults':'Action,Version') ||
      (action==='DescribeRouteTables' && params.get('MaxResults')!=='100') ||
      params.get('Version')!=='2016-11-15' || !['learner-a','learner-b'].includes(identity)) {
    fixture.errors.push('unexpected transport '+url+'/'+action); throw Error(fixture.errors.at(-1));
  }
  if (action==='DescribeVpcs') return reply(xml('DescribeVpcsResponse','<vpcSet><item>'+
    '<vpcId>vpc-11111111111111111</vpcId><cidrBlock>10.0.0.0/16</cidrBlock>'+
    '<simulatorManagedBy>cloudformation</simulatorManagedBy></item></vpcSet>'));
  if (action!=='DescribeRouteTables' || opts.credentials!=='same-origin' || opts.redirect!=='error') {
    fixture.errors.push('unexpected operation '+action); throw Error(fixture.errors.at(-1));
  }
  fixture.calls.push({identity,signal:opts.signal});
  if (fixture.calls.length===1) return routes('first-identity-only');
  return new Promise(resolve=>{fixture.releases[identity]=value=>resolve(routes(value));});
};
"""


def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'))
        try:
            page = browser.new_page(viewport={'width': 1280, 'height': 900})
            def serve(route):
                if route.request.url == ORIGIN + '/_simulator/console/':
                    route.fulfill(content_type='text/html', body='<html><body><div id="root"></div></body></html>')
                else:
                    route.abort()

            page.route('**/*', serve)
            page.goto(ORIGIN + '/_simulator/console/#/vpc')
            page.evaluate(TRANSPORT)
            page.add_style_tag(content=APP.with_name('app.css').read_text())
            page.add_script_tag(content=APP.read_text())
            row = page.get_by_role('radio', name='Select ' + VPC, exact=True)
            row.check()
            expect(page.get_by_text('first-identity-only', exact=True)).to_be_visible()
            page.get_by_role('button', name='Refresh route tables', exact=True).click()
            page.wait_for_function("typeof __routes.releases['learner-a']==='function'")
            expect(page.get_by_text('first-identity-only', exact=True)).to_have_count(0)

            page.get_by_role('button', name='Account menu').click()
            page.get_by_text('Choose identity', exact=True).first.click()
            dialog = page.get_by_role('dialog')
            dialog.get_by_label('Principal name', exact=True).fill('learner-b')
            dialog.get_by_role('button', name='Choose identity', exact=True).click()
            page.wait_for_function('__routes.calls[1].signal.aborted')
            row.check()
            page.wait_for_function("typeof __routes.releases['learner-b']==='function'")
            page.evaluate("__routes.releases['learner-b']('new-identity-only')")
            expect(page.get_by_text('new-identity-only', exact=True)).to_be_visible()
            page.evaluate("__routes.releases['learner-a']('late-identity-only')")
            page.evaluate('new Promise(done=>requestAnimationFrame(()=>requestAnimationFrame(done)))')
            expect(page.get_by_text('new-identity-only', exact=True)).to_be_visible()
            for old in ('first-identity-only', 'late-identity-only'):
                expect(page.get_by_text(old, exact=True)).to_have_count(0)
            assert page.evaluate('__routes.errors') == []
            assert page.evaluate('__routes.calls.map(c=>c.identity)') == ['learner-a', 'learner-a', 'learner-b']
        finally:
            browser.close()
    print('PASS embedded Console in Chromium with fixture transport: same VPC/region, A -> B, late A ignored.')


if __name__ == '__main__':
    main()
