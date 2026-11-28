"""Run the official Cloud Trace v1 discovery client against two local standalone processes."""
from datetime import datetime, timedelta, timezone
import http.client
import importlib.metadata
import json
import os
from pathlib import Path
import tempfile

import httplib2
from googleapiclient.discovery import build
from googleapiclient.errors import HttpError

from gcp_dns_sdk import LocalTeam, LocalTokenHttp, started

TRACE_A = '1234567890abcdef1234567890abcdef'
TRACE_B = '2234567890abcdef1234567890abcdef'
TRACE_C = '3234567890abcdef1234567890abcdef'


def api(team, token, project):
    root = '/_simulator/providers/gcp/cloudtrace/'
    collection = root + 'v1/projects/' + project + '/traces'
    transport = LocalTokenHttp(httplib2.Http(proxy_info=None, timeout=5), team.port, collection, token)
    service = build('cloudtrace', 'v1', http=transport, static_discovery=True,
                    cache_discovery=False, client_options={'api_endpoint': team.endpoint + root})
    return service.projects()


def expect_error(code, req):
    try:
        req.execute(num_retries=0)
    except HttpError as e:
        assert e.resp.status == code, (e.resp.status, code)
        if code != 401:
            assert json.loads(e.content)['error']['code'] == code
    else:
        raise AssertionError(f'expected HTTP {code}')


def new_reader(team):
    name = 'trace-sdk-participant'
    conn = http.client.HTTPConnection('127.0.0.1', team.port, timeout=5)
    try:
        conn.request('PUT', '/_simulator/v1/iam/principals/'+name+'/policy',
                     body=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'*','Resource':'*'}]}),
                     headers={'Content-Type':'application/json','X-Simulator-Token':team.token})
        reply = conn.getresponse()
        assert 200 <= reply.status < 300
        reply.read()
    finally:
        conn.close()
    return team.control('POST','/_simulator/v1/access',{'Principal':name})['AccessToken']


def run():
    assert importlib.metadata.version('google-api-python-client') == '2.199.0'
    binary = Path(os.environ.get('SIMULATOR_BINARY','dist/tenkacloud-simulator')).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix='gcp-trace-') as temp:
        source, peer_dir = (Path(temp)/n for n in ('one','two'))
        source.mkdir(); peer_dir.mkdir()
        team,peer=LocalTeam(source,binary),LocalTeam(peer_dir,binary)
        now=datetime.now(timezone.utc).replace(microsecond=123000)
        start=(now-timedelta(seconds=3)).isoformat(timespec='milliseconds').replace('+00:00','Z')
        end=now.isoformat(timespec='milliseconds').replace('+00:00','Z')
        root={'spanId':'18446744073709551615','name':'request','startTime':start,'endTime':end,'kind':'RPC_SERVER'}
        child=dict(root,spanId='9007199254740993',parentSpanId=root['spanId'],name='child')
        changed=dict(root,name='second observation')
        with started(peer), started(team):
            project=team.control('GET','/_simulator/v1/capabilities')['cloudStorage']['gcp']['project']
            assert team.token!=peer.token and team.port!=peer.port
            first, other=api(team,team.token,project),api(peer,peer.token,project)

            def trace(i,spans):
                return {'projectId':project,'traceId':i,'spans':spans}
            def append(client, *records):
                return client.patchTraces(projectId=project,body={'traces':list(records)})
            def get(client, i):
                return client.traces().get(projectId=project,traceId=i)
            def listing(client, **options):
                return client.traces().list(projectId=project,startTime=start,endTime=end,**options)

            assert append(first,trace(TRACE_A,[root,child]),trace(TRACE_B,[root])).execute(num_retries=0)=={}
            assert append(first,trace(TRACE_A,[root,changed])).execute(num_retries=0)=={}
            saved=get(first,TRACE_A).execute(num_retries=0)
            assert saved['projectId']==project and saved['traceId']==TRACE_A
            assert [(s['spanId'],s['name']) for s in saved['spans']]==[(s['spanId'],s['name']) for s in (root,child,root,changed)]
            assert saved['spans'][1]['parentSpanId']==root['spanId']
            minimal=listing(first,pageSize=1).execute(num_retries=0)
            assert minimal['traces']==[{'projectId':project,'traceId':TRACE_A}]
            cursor=minimal['nextPageToken']
            assert append(first,trace(TRACE_C,[root])).execute(num_retries=0)=={}
            second=listing(first,pageSize=1,pageToken=cursor).execute(num_retries=0)
            assert second=={'traces':[{'traceId':TRACE_B,'projectId':project}]}
            assert listing(first,view='COMPLETE',pageSize=1).execute(num_retries=0)['traces']==[saved]
            roots=listing(first,view='ROOTSPAN',pageSize=1).execute(num_retries=0)['traces'][0]
            assert roots['spans']==[s for s in saved['spans'] if not s.get('parentSpanId')]
            expect_error(400,append(first,trace(TRACE_A,[root]),trace(TRACE_B,[dict(root,spanId='0')])))
            assert get(first,TRACE_A).execute(num_retries=0)==saved
            expect_error(404,get(other,TRACE_A))
            expect_error(400,listing(other,pageToken=cursor,pageSize=1))
            participant_token=new_reader(team)
            denied=api(team,participant_token,project)
            expect_error(403,get(denied,TRACE_A))
            expect_error(403,append(denied,trace(TRACE_A,[root])))
            expect_error(403,api(team,team.token,'different-project').traces().get(projectId='different-project',traceId=TRACE_A))
            assert append(other,trace(TRACE_A,[changed])).execute(num_retries=0)=={}
            peer_before=get(other,TRACE_A).execute(num_retries=0)
            assert peer_before!=saved
            old_token=team.token
            team.stop(); team.start()
            # Restarting with the same operator token file preserves that credential.
            # The process-local participant token and paging authority are renewed.
            assert team.token==old_token
            expect_error(401,get(api(team,participant_token,project),TRACE_A))
            renewed=api(team,team.token,project)
            assert get(renewed,TRACE_A).execute(num_retries=0)==saved
            expect_error(400,listing(renewed,pageToken=cursor,pageSize=1))
            assert get(other,TRACE_A).execute(num_retries=0)==peer_before
    assert not Path(temp).exists()
    print('Cloud Trace v1 append, get, three list views, duplicate spans, isolated projects, restart and cleanup passed')


if __name__=='__main__':
    run()
