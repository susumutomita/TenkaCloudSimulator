"""Opt-in two-team Kinesis Video Streams acceptance through the mounted Console."""
from contextlib import ExitStack
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from gcp_dns_sdk import LocalTeam, started


ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / 'internal/aws/kinesisvideo/testdata/two-frames.mkv'
REGION = 'us-east-1'
NAME = 'workshop-video'
ZERO = 'camera-zero'


def native(team, path, body, status=200, token=None, method='POST', region=REGION):
    control = path.startswith('/_simulator/')
    headers = {'Content-Type': 'application/json', 'X-Simulator-Token': token or team.token}
    if not control:
        headers.update({'X-Simulator-Service': 'kinesisvideo', 'X-Simulator-Region': region})
    connection = http.client.HTTPConnection('127.0.0.1', team.port, timeout=10)
    try:
        connection.request(method, path, json.dumps(body), headers)
        response = connection.getresponse()
        data = response.read(8 << 20)
        assert response.status == status, (path, response.status, response.getheader('x-amzn-errortype'))
        if path == '/getMedia':
            assert response.getheader('Content-Type') == 'video/x-matroska'
            return data
        if status >= 400:
            return data
        return json.loads(data) if data else {}
    finally:
        connection.close()


def read_native(team, arn):
    return native(team, '/getMedia', {'StreamARN': arn, 'StartSelector': {'StartSelectorType': 'EARLIEST'}})


def frames(path):
    decoded = subprocess.check_output(['ffmpeg', '-nostdin', '-loglevel', 'error', '-protocol_whitelist',
                                       'file,pipe', '-threads', '1', '-i', str(path), '-f', 'framemd5', '-'])
    rows = [line.rsplit(b',', 1)[1].strip().decode() for line in decoded.splitlines() if not line.startswith(b'#')]
    assert len(rows) == 2 and len(set(rows)) == 2
    return rows


def tags(path):
    info = json.loads(subprocess.check_output(['ffprobe', '-v', 'error', '-protocol_whitelist', 'file,pipe',
        '-show_entries', 'format_tags', '-of', 'json', str(path)]))
    return info['format']['tags']


def scalar_segments(data):
    """Split the native concatenation for independent FFmpeg and FFprobe readback."""
    def end_element(start, expected_id):
        assert data[start:start+4] == expected_id
        pos = start+4
        assert pos < len(data) and data[pos]
        width = 9-data[pos].bit_length()
        assert pos+width <= len(data)
        length = int.from_bytes(data[pos:pos+width], 'big') ^ (1 << (width*7))
        end = pos+width+length
        assert length != (1 << (width*7))-1 and end <= len(data)
        return end
    pos = 0
    while pos < len(data):
        header = end_element(pos, bytes.fromhex('1a45dfa3'))
        end = end_element(header, bytes.fromhex('18538067'))
        yield data[pos:end]
        pos = end


def open_stream(browser, team, name, locale='en', token=None, width=1440):
    ctx = browser.new_context(viewport={'width': width, 'height': 900}, accept_downloads=True)
    ctx.add_init_script(f"localStorage.setItem('tenkacloud-simulator-locale','{locale}')")
    login = ctx.request.post(team.endpoint + '/_simulator/login', form={'token': token or team.token}, max_redirects=0)
    assert login.status == 303
    # Observe the original streaming fetch before its body leaves the inspector cache.
    # The single loss control drops the final line only after the real native send.
    ctx.add_init_script("""(() => {
      const send=window.fetch;
      window.observedMediaAcks=[];
      window.fetch=async (...args)=>{
        const response=await send(...args);
        const path=new URL(response.url,location.href).pathname;
        if(path===window.holdNextMediaPath){
          window.holdNextMediaPath=null;
          await new Promise(resolve=>{window.releaseHeldMedia=resolve});
        }
        if(path!='/putMedia'||response.status!==200)return response;
        const lines=(await response.clone().text()).trim().split('\\n');
        window.observedMediaAcks.push(lines.map(line=>JSON.parse(line)));
        if(window.loseFinalMediaAck){
          window.loseFinalMediaAck=false;
          return new Response(lines.slice(0,2).join('\\n')+'\\n',{status:response.status,headers:{'Content-Type':'application/json'}});
        }
        return response;
      }
    })()""")
    page = ctx.new_page()
    en = locale == 'en'
    page.goto(team.endpoint + '/_simulator/console/#/catalog')
    page.get_by_role('button', name=re.compile('^Catalog service' if en else '^カタログサービス')).click()
    page.get_by_role('option', name=re.compile('Amazon Kinesis Video Streams')).click()
    page.get_by_role('link', name=name, exact=True).click()
    expect(page.get_by_role('heading', name='Stream fragments' if en else 'ストリームのフラグメント', exact=True)).to_be_visible()
    return ctx, page


def result(page, path, click, status=200):
    with page.expect_response(lambda r: urlsplit(r.url).path == path) as pending:
        click()
    response = pending.value
    assert response.status == status, (path, response.status, response.headers.get('x-amzn-errortype'))
    return response


def audit(page, axe):
    page.evaluate(axe.read_text())
    violation = page.evaluate("""async () => (await axe.run(document, {runOnly:{type:'tag',
      values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})).violations.map(v=>({id:v.id,targets:v.nodes.map(n=>n.target)}))""")
    assert not violation, violation
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')


def scalar(ctx, team, operation, status):
    page = ctx.new_page()
    try:
        page.goto(team.endpoint+'/_simulator/docs/catalog.html?api=kinesisvideo')
        page.get_by_text(operation, exact=True).first.click()
        path = '/' + operation[0].lower()+operation[1:]
        page.get_by_role('button',name='Test Request (post '+path+')',exact=True).click()
        response = result(page,path,page.get_by_role('button',name=re.compile('^Send post request to ')).click,status)
        request = response.request
        assert request.method == 'POST' and request.url == team.endpoint+path and request.redirected_from is None
        assert request.header_value('cookie') and request.header_value('x-simulator-service') == 'kinesisvideo'
        assert not request.header_value('authorization') and not request.header_value('x-simulator-token')
        assert request.post_data_json['StreamName'] == NAME
        if status==200 and operation=='GetDataEndpoint':
            assert request.post_data_json['APIName']=='GET_MEDIA' and response.json()['DataEndpoint']==team.endpoint
        elif status==200:
            assert request.post_data_json['StartSelector']=={'StartSelectorType':'EARLIEST'}
            assert response.headers['content-type']=='video/x-matroska'
            expect(page.get_by_role('link',name='Download Response Body',exact=True)).to_be_visible()
            with page.expect_download() as selected:
                page.get_by_role('link',name='Download Response Body',exact=True).click()
            return selected.value.path().read_bytes()
        else:
            assert response.headers['x-amzn-errortype']=='NotAuthorizedException'
        return b''
    finally:
        page.close()


def run(binary, chromium, axe):
    assert socket.if_nameindex() == [(1, 'lo')]
    cap = dict(line.split(':', 1) for line in Path('/proc/self/status').read_text().splitlines() if ':' in line)
    assert int(cap['CapEff'], 16) == 0 and os.geteuid() != 0
    expected_frames = frames(SOURCE)
    with tempfile.TemporaryDirectory(prefix='kinesis-media-console-') as directory, ExitStack() as stack:
        teams = []
        for suffix in ('owner','peer'):
            folder = Path(directory) / suffix
            folder.mkdir()
            teams.append(stack.enter_context(started(LocalTeam(folder, binary))))
        owner, peer = teams
        streams = {}
        for team, name, hours in [(owner, NAME, 24), (owner, ZERO, 0), (peer, NAME, 24)]:
            arn = native(team, '/createStream', {'StreamName': name, 'DataRetentionInHours': hours, 'MediaType':'video/h264'})['StreamARN']
            streams[team, name] = arn
            assert native(team, '/describeStream', {'StreamARN': arn})['StreamInfo']['DataRetentionInHours'] == hours
        regional = native(owner,'/createStream',{'StreamName':NAME,'DataRetentionInHours':0},region='us-west-2')['StreamARN']
        arn = streams[owner, NAME]
        requests, outside, receipts = [], [], []
        def observe(page, team):
            def inspect(reply):
                if urlsplit(reply.url).scheme in ('http', 'https'):
                    if not reply.url.startswith(team.endpoint + '/'):
                        outside.append(urlsplit(reply.url).hostname)
                    else:
                        requests.append((urlsplit(reply.url).path, reply.status))
            page.on('response', inspect)
        def count(path):
            return sum(p == path for p, _ in requests)
        pw = stack.enter_context(sync_playwright())
        browser = stack.enter_context(pw.chromium.launch(executable_path=str(chromium), chromium_sandbox=True,
          args=['--no-proxy-server','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1']))
        ctx, page = open_stream(browser, owner, NAME)
        observe(page, owner)
        chooser = page.get_by_label('Matroska file',exact=True)
        upload = page.get_by_role('button', name='Upload fragment')
        earliest = page.get_by_role('button', name='Read earliest fragments')
        chooser.set_input_files(SOURCE)
        expect(page.get_by_text(re.compile(r'^two-frames\.mkv \('))).to_be_visible()
        page.get_by_role('button', name='Discard file').press('Enter')
        expect(upload).to_be_disabled()
        assert count('/putMedia') == 0
        chooser.set_input_files(SOURCE)
        page.evaluate("window.holdNextMediaPath='/getDataEndpoint'")
        result(page,'/getDataEndpoint',upload.click)
        page.wait_for_function("typeof window.releaseHeldMedia==='function'")
        page.get_by_role('button',name='Cancel transfer').press('Enter')
        page.evaluate('window.releaseHeldMedia()')
        expect(page.get_by_text('Transfer canceled.',exact=True)).to_be_visible()
        expect(upload).to_be_enabled()
        assert count('/putMedia') == 0 and not read_native(owner,arn)
        chooser.set_input_files({'name':'invalid.mkv','mimeType':'video/x-matroska','buffer':b'invalid'})
        result(page, '/putMedia', upload.click, 400)
        expect(page.get_by_role('status').filter(has_text='HTTP 400')).to_be_visible()
        assert not read_native(owner, arn)
        for _ in range(2):
            chooser.set_input_files(SOURCE)
            selected_hash = chooser.evaluate('''async e=>Array.from(new Uint8Array(await crypto.subtle.digest(
                "SHA-256",await e.files[0].arrayBuffer())),n=>n.toString(16).padStart(2,"0")).join("")''')
            assert selected_hash == hashlib.sha256(SOURCE.read_bytes()).hexdigest()
            response = result(page, '/putMedia', upload.click)
            assert response.request.headers['x-amzn-stream-arn'] == arn
            assert int(response.request.all_headers()['content-length']) == SOURCE.stat().st_size
            page.wait_for_function('n=>window.observedMediaAcks.length===n', arg=len(receipts)+1)
            acks = page.evaluate('window.observedMediaAcks.at(-1)')
            assert [a['EventType'] for a in acks] == ['BUFFERING','RECEIVED','PERSISTED']
            ident = acks[-1]['FragmentNumber']
            expect(page.get_by_text('Stored fragment '+ident, exact=True)).to_be_visible()
            receipts.append(ident)
        assert len(set(receipts)) == 2
        result(page, '/getMedia', earliest.click)
        for ident in receipts:
            expect(page.get_by_role('heading',name='Fragment '+ident,exact=True)).to_be_visible()
        one = page.get_by_role('region', name='Fragment '+receipts[0],exact=True)
        with page.expect_download() as download:
            one.get_by_role('button',name='Download MKV').click()
        downloaded = Path(directory) / 'downloaded.mkv'
        download.value.save_as(downloaded)
        stored = tags(downloaded)
        assert frames(downloaded) == expected_frames
        required = ['FRAGMENT_NUMBER','PRODUCER_TIMESTAMP','SERVER_TIMESTAMP','CONTINUATION_TOKEN','MILLIS_BEHIND_NOW']
        assert all(stored['AWS_KINESISVIDEO_'+k] for k in required)
        assert stored['AWS_KINESISVIDEO_FRAGMENT_NUMBER'] == receipts[0]
        for key in ('PRODUCER_TIMESTAMP','SERVER_TIMESTAMP'):
            expect(one.get_by_text(stored['AWS_KINESISVIDEO_'+key],exact=True).first).to_be_visible()
        audit(page, axe)
        resumed = result(page, '/getMedia', one.get_by_role('button',name='Read after this fragment').click)
        assert resumed.request.post_data_json['StartSelector'] == {'StartSelectorType':'CONTINUATION_TOKEN',
                                'ContinuationToken':stored['AWS_KINESISVIDEO_CONTINUATION_TOKEN']}
        expect(page.get_by_role('heading',name='Fragment '+receipts[0],exact=True)).to_have_count(0)
        expect(page.get_by_role('heading',name='Fragment '+receipts[1],exact=True)).to_be_visible()
        result(page, '/getMedia', page.get_by_role('button',name='Read after this fragment').click)
        expect(page.get_by_text('No retained fragments in this read.',exact=True)).to_be_visible()
        # Truncate only the final acknowledgement after the *actual* retained upload commits.
        page.evaluate('window.loseFinalMediaAck=true')
        chooser.set_input_files(SOURCE)
        upload.click()
        expect(page.get_by_role('alert').filter(has_text='The upload outcome is unknown.')).to_be_visible()
        expect(upload).to_be_disabled()
        sent = count('/putMedia')
        complete = page.evaluate('window.observedMediaAcks.at(-1)')
        assert [e['EventType'] for e in complete] == ['BUFFERING','RECEIVED','PERSISTED']
        committed = [complete[-1]['FragmentNumber']]
        assert committed[0] not in receipts
        result(page, '/getMedia', earliest.click)
        expect(page.get_by_role('heading',name='Fragment '+committed[0],exact=True)).to_be_visible()
        expect(upload).to_be_disabled()
        assert count('/putMedia') == sent
        receipts += committed
        scalar(ctx,owner,'GetDataEndpoint',200)
        media=scalar(ctx,owner,'GetMedia',200)
        scalar_ids = []
        scalar_file = Path(directory)/'scalar.mkv'
        for segment in scalar_segments(media):
            scalar_file.write_bytes(segment)
            scalar_ids.append(tags(scalar_file)['AWS_KINESISVIDEO_FRAGMENT_NUMBER'])
            assert frames(scalar_file)==expected_frames
        assert scalar_ids==receipts
        # Server timestamps in each segment are immutable across downloads; lag may change.
        assert stored['AWS_KINESISVIDEO_CONTINUATION_TOKEN'].encode() in media
        page.evaluate("window.releaseHeldMedia=null;window.holdNextMediaPath='/getMedia'")
        result(page,'/getMedia',earliest.click)
        page.wait_for_function("typeof window.releaseHeldMedia==='function'")
        page.get_by_role('navigation',name='Catalog location').get_by_role('link').nth(1).click()
        page.get_by_role('link',name=ZERO,exact=True).click()
        expect(page.get_by_role('heading',name='Stream fragments')).to_be_visible()
        page.evaluate('window.releaseHeldMedia()')
        result(page,'/getMedia',page.get_by_role('button',name='Read earliest fragments').click)
        expect(page.get_by_text('No retained fragments in this read.',exact=True)).to_be_visible()
        for ident in receipts:
            expect(page.get_by_role('heading',name='Fragment '+ident,exact=True)).to_have_count(0)
        # Retention editing still uses the original versioned native operation.
        page.get_by_role('navigation',name='Catalog location').get_by_role('link').nth(1).click()
        page.get_by_role('link',name=NAME,exact=True).click()
        page.get_by_role('button',name='Edit',exact=True).click()
        hours=page.get_by_role('spinbutton',name='Retention (hours)',exact=True)
        expect(hours).to_have_value('24')
        hours.fill('25')
        changed=result(page,'/updateDataRetention',page.get_by_role('button',name='Save changes',exact=True).click)
        assert changed.request.post_data_json['DataRetentionChangeInHours']==1
        assert native(owner,'/describeStream',{'StreamARN':arn})['StreamInfo']['DataRetentionInHours']==25
        expect(page.get_by_role('heading',name='Stream fragments')).to_be_visible()
        page.get_by_role('button',name=re.compile('us-east-1')).click()
        page.get_by_role('menuitem',name='us-west-2').click()
        page.get_by_role('button',name=re.compile('^Catalog service')).click()
        page.get_by_role('option',name=re.compile('Amazon Kinesis Video Streams')).click()
        page.get_by_role('link',name=NAME,exact=True).click()
        west=result(page,'/getMedia',earliest.click)
        assert west.request.post_data_json['StreamARN']==regional
        assert west.request.headers['x-simulator-region']=='us-west-2'
        expect(page.get_by_text('No retained fragments in this read.',exact=True)).to_be_visible()
        ctx.close()
        zctx, zp = open_stream(browser, owner, ZERO)
        observe(zp, owner)
        zp.get_by_label('Matroska file',exact=True).set_input_files(SOURCE)
        result(zp,'/putMedia',zp.get_by_role('button',name='Upload fragment').click)
        zp.wait_for_function('window.observedMediaAcks.length===1')
        events = zp.evaluate('window.observedMediaAcks[0]')
        assert [e['EventType'] for e in events] == ['BUFFERING','RECEIVED'] and all('FragmentNumber' not in e for e in events)
        expect(zp.get_by_text('Fragment received. Current retention is 0 hours.',exact=True)).to_be_visible()
        assert not read_native(owner,streams[owner,ZERO])
        zctx.close()
        native(owner,'/_simulator/v1/iam/principals/video-reader/policy',{'Version':'2012-10-17','Statement':[{
            'Effect':'Allow','Action':['kinesisvideo:ListStreams','kinesisvideo:DescribeStream','kinesisvideo:ListTagsForResource',
                                       'kinesisvideo:GetDataEndpoint','kinesisvideo:GetMedia'],'Resource':'*'}]},method='PUT')
        reader = native(owner,'/_simulator/v1/access',{'Principal':'video-reader'})['AccessToken']
        rctx, rp = open_stream(browser, owner, NAME,token=reader)
        observe(rp, owner)
        rp.get_by_label('Matroska file',exact=True).set_input_files(SOURCE)
        result(rp,'/putMedia',rp.get_by_role('button',name='Upload fragment').click,401)
        expect(rp.get_by_role('status').filter(has_text='HTTP 401')).to_be_visible()
        result(rp,'/getMedia',rp.get_by_role('button',name='Read earliest fragments').click)
        expect(rp.get_by_role('heading',name='Fragment '+receipts[-1],exact=True)).to_be_visible()
        assert urlsplit(rp.url).path.startswith('/_simulator/console')
        native(owner,'/deleteStream',{'StreamARN':arn},401,token=reader)
        native(owner,'/_simulator/v1/access',{'Principal':'video-reader'},403,token=reader)
        native(owner,'/describeStream',{'StreamARN':arn},401,token=peer.token)
        native(owner,'/_simulator/v1/iam/principals/video-reader/policy',{'Version':'2012-10-17','Statement':[{
            'Effect':'Allow','Action':['kinesisvideo:ListStreams','kinesisvideo:DescribeStream','kinesisvideo:ListTagsForResource',
                                       'kinesisvideo:GetDataEndpoint'],'Resource':'*'}]},method='PUT')
        result(rp,'/getMedia',rp.get_by_role('button',name='Read earliest fragments').click,401)
        expect(rp.get_by_role('status').filter(has_text='HTTP 401')).to_be_visible()
        for ident in receipts:
            expect(rp.get_by_role('heading',name='Fragment '+ident,exact=True)).to_have_count(0)
        expect(rp.get_by_role('button',name='Read after this fragment')).to_have_count(0)
        scalar(rctx,owner,'GetMedia',401)
        rctx.close()
        for team in teams:
            team.stop()
            team.start()
        jctx, jp = open_stream(browser,owner,NAME,locale='ja',width=390)
        observe(jp, owner)
        result(jp,'/getMedia',jp.get_by_role('button',name='先頭からフラグメントを読み取る').click)
        for ident in receipts:
            expect(jp.get_by_role('heading',name='フラグメント '+ident,exact=True)).to_be_visible()
        audit(jp, axe)
        jctx.close()
        pctx, pp = open_stream(browser,peer,NAME)
        observe(pp, peer)
        result(pp,'/getMedia',pp.get_by_role('button',name='Read earliest fragments').click)
        expect(pp.get_by_text('No retained fragments in this read.',exact=True)).to_be_visible()
        pctx.close()
        assert not read_native(peer,streams[peer,NAME])
        for (team, name), saved in streams.items():
            native(team,'/deleteStream',{'StreamARN':saved})
            remaining=native(team,'/listStreams',{})['StreamInfoList']
            assert name not in [info['StreamName'] for info in remaining]
        native(owner,'/deleteStream',{'StreamARN':regional},region='us-west-2')
        assert not native(owner,'/listStreams',{},region='us-west-2')['StreamInfoList']
        assert all(not native(team,'/listStreams',{})['StreamInfoList'] for team in teams)
        assert not outside
        return {'nativeBrowserResponses':len(requests),'cameraFragmentIds':receipts,'scalarDownloadedFragmentIds':scalar_ids,'retentionChange':[24,25],'regionSelection':'us-west-2','canceledBeforeUpload':True,'staleReadSuppressed':True,'downloadedFrameMD5':expected_frames,
                'downloadSHA256':hashlib.sha256(downloaded.read_bytes()).hexdigest(),'awsTags':required,
                'twoTeamRestart':True,'zeroRetention':True,'deniedPut':401,'deniedDelete':401,'operatorOnlyAccess':403,'foreignToken':401,
                'missingFinalAckObserved':True,'automaticResends':0,'auditedWidths':[1440,390],'scalarSends':['GetDataEndpoint 200','GetMedia 200','GetMedia 401'],'cleanup':True}


if __name__ == '__main__':
    print(json.dumps(run(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True),
                         Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True),
                         Path(os.environ['AXE_SCRIPT']).resolve(strict=True))))
