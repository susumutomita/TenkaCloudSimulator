"""Authentic SES v2 and Route 53 workflow on two disposable local CLI databases."""

from contextlib import ExitStack
from email import policy
from email.parser import BytesParser
from http.client import HTTPConnection
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from urllib.parse import urlsplit
from xml.etree import ElementTree as XML

from sts_session_lifecycle import LocalSimulator, need

DOMAIN = 'owned.test'
FROM = 'sender@' + DOMAIN
MAILBOX = 'inbox@inbox.ses.test'
ABSENT = 'absent@inbox.ses.test'
REGION = 'us-east-1'
SINK = '/_simulator/v1/ses/sink/' + REGION + '/messages'
IDENTITIES = '/v2/email/identities'
SEND = '/v2/email/outbound-emails'
TEMPLATES = '/v2/email/templates'
ZONE = '/2013-04-01/hostedzone'
TEMPLATE = 'local-welcome'

def direct(team, method, path, body=None, expected=200, service=None, token=None):
    assert path.startswith('/') and not path.startswith('//')
    headers = {'X-Simulator-Token':token or team.token}
    if service:
        headers.update({'X-Simulator-Service':service,'X-Simulator-Region':REGION})
    if isinstance(body,dict):
        body = json.dumps(body,ensure_ascii=False).encode()
        headers['Content-Type'] = 'application/json'
    elif body is not None:
        headers['Content-Type'] = 'application/xml'
    c = HTTPConnection('127.0.0.1',team.port,timeout=8)
    try:
        c.request(method,path,body,headers)
        response = c.getresponse()
        data = response.read(300_000)
        assert response.status == expected, (method,path,expected,response.status)
        return data
    finally:
        c.close()

class NativeClients:
    """Optional stock client. The direct variant is independently labeled in the receipt."""
    def __init__(self, team, use_sdk, stack):
        self.team,self.use_sdk,self.sent,self.responses = team,use_sdk,0,0
        if not use_sdk:
            return
        import boto3, botocore
        from botocore.config import Config
        assert (boto3.__version__,botocore.__version__)==('1.42.0','1.41.6')
        session=boto3.Session()
        for name in ['sesv2','route53']:
            client=session.client(name,region_name=REGION,endpoint_url=team.endpoint,
                aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='simulator-only',
                config=Config(retries={'total_max_attempts':1},connect_timeout=3,read_timeout=8,proxies={},
                    request_checksum_calculation='when_required',response_checksum_validation='when_required'))
            setattr(self,name,client);stack.callback(client.close)
            client.meta.events.register('before-sign.*',self.sign)
            client.meta.events.register('before-send.*',self.sending)
            client.meta.events.register('after-call.*',self.received)

    def sign(self,request,**_):
        request.headers['X-Simulator-Token']=self.team.token

    def sending(self,request,**_):
        url=urlsplit(request.url)
        assert url.scheme=='http' and url.netloc=='127.0.0.1:'+str(self.team.port) and not url.fragment
        assert url.path.startswith(('/v2/email/',ZONE)) and request.headers['Authorization'].startswith(b'AWS4-HMAC-SHA256 ')
        self.sent+=1

    def received(self,http_response,**_):
        assert 200<=http_response.status_code<600
        self.responses+=1

    def zone(self,delete=None):
        if self.use_sdk:
            if delete: return self.route53.delete_hosted_zone(Id=delete)
            return self.route53.create_hosted_zone(Name=DOMAIN,CallerReference='local-mail')['HostedZone']['Id']
        if delete: return direct(self.team,'DELETE',ZONE+'/'+delete.split('/')[-1],expected=200,service='route53')
        data=direct(self.team,'POST',ZONE,'<CreateHostedZoneRequest><Name>'+DOMAIN+'</Name><CallerReference>local-mail</CallerReference></CreateHostedZoneRequest>',201,'route53')
        return XML.fromstring(data).findtext('.//{*}HostedZone/{*}Id')

    def record(self,zone,token,action):
        import re
        assert re.fullmatch('[a-z2-7]{32}',token)
        name=token+'._domainkey.'+DOMAIN+'.';value=token+'.dkim.amazonses.com.'
        if self.use_sdk:
            result=self.route53.change_resource_record_sets(HostedZoneId=zone,ChangeBatch={'Changes':[{'Action':action,'ResourceRecordSet':{
                'Name':name,'Type':'CNAME','TTL':60,'ResourceRecords':[{'Value':value}]}}]})
            assert result['ChangeInfo']['Status']=='INSYNC'
            return
        xml='<ChangeResourceRecordSetsRequest><ChangeBatch><Changes><Change><Action>'+action+'</Action><ResourceRecordSet><Name>'+name+'</Name><Type>CNAME</Type><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>'+value+'</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>'
        direct(self.team,'POST',ZONE+'/'+zone.split('/')[-1]+'/rrset',xml,200,'route53')

    def ses(self,operation,fields,expected=200):
        if self.use_sdk:
            from botocore.exceptions import ClientError
            try: result=getattr(self.sesv2,operation)(**fields)
            except ClientError as exc:
                assert exc.response['ResponseMetadata']['HTTPStatusCode']==expected, (operation,exc.response['Error']['Code'])
                return exc.response
            assert result['ResponseMetadata']['HTTPStatusCode']==expected,(operation,expected)
            return result
        routes={
            'create_email_identity':('POST',IDENTITIES), 'get_email_identity':('GET',IDENTITIES+'/'+DOMAIN),
            'delete_email_identity':('DELETE',IDENTITIES+'/'+DOMAIN), 'list_email_identities':('GET',IDENTITIES),
            'send_email':('POST',SEND), 'create_email_template':('POST',TEMPLATES), 'delete_email_template':('DELETE',TEMPLATES+'/'+TEMPLATE),
            'get_email_template':('GET',TEMPLATES+'/'+TEMPLATE)}
        method,path=routes[operation]
        body=direct(self.team,method,path,fields if method=='POST' else None,expected,'ses')
        return json.loads(body) if body else {}

def simple(recipient=MAILBOX):
    return {'FromEmailAddress':FROM,'Destination':{'ToAddresses':[recipient]},'Content':{'Simple':{
        'Subject':{'Data':'受信確認'},'Body':{'Text':{'Data':'literal {{who}}'},'Html':{'Data':'<b>受信</b>'}}}}}

def parsed(team,ident,subject,text,html=None):
    data=direct(team,'GET',SINK+'/'+ident+'/content')
    msg=BytesParser(policy=policy.default).parsebytes(data)
    assert str(msg['From'])==FROM and str(msg['To'])==MAILBOX and str(msg['Message-ID'])=='<'+ident+'@inbox.ses.test>'
    assert str(msg['Subject'])==subject and msg['Date'].datetime
    parts={part.get_content_type():part.get_payload(decode=True).decode() for part in msg.walk() if not part.is_multipart()}
    assert parts==({'text/plain':text,'text/html':html} if html is not None else {'text/plain':text})
    return data

def verify(binary,use_sdk=True,browser_flow=None):
    with TemporaryDirectory(prefix='ses-local-mail-') as temp, ExitStack() as stack:
        teams=[LocalSimulator(Path(temp)/k,binary) for k in ['owner','peer']]
        for team in teams: stack.callback(team.close);team.start()
        owner,peer=teams
        a,b=[NativeClients(team,use_sdk,stack) for team in teams]
        zone=a.zone(); peer_zone=b.zone()
        fields={'EmailIdentity':DOMAIN}
        b.ses('create_email_identity',fields)
        if browser_flow: initial=browser_flow.create_domain(owner)
        else: initial=a.ses('create_email_identity',fields)
        tokens=initial['DkimAttributes']['Tokens']
        assert len(set(tokens))==3 and initial['DkimAttributes']['Status']=='PENDING'
        assert set(b.ses('get_email_identity',fields)['DkimAttributes']['Tokens']).isdisjoint(tokens)
        a.ses('send_email',simple(),400)
        for token in tokens: a.record(zone,token,'CREATE')
        assert a.ses('get_email_identity',fields)['VerificationStatus']=='SUCCESS'
        template={'TemplateName':TEMPLATE,'TemplateContent':{'Subject':'Hello {{who}}','Text':'Saved {{who}}'}}
        a.ses('create_email_template',template)
        assert a.ses('get_email_template',{'TemplateName':TEMPLATE})['TemplateContent']==template['TemplateContent']
        result=a.ses('send_email',simple());good=result['MessageId']
        blob=parsed(owner,good,'受信確認','literal {{who}}','<b>受信</b>')
        body={'FromEmailAddress':FROM,'Destination':{'ToAddresses':[MAILBOX]},'Content':{'Template':{'TemplateName':TEMPLATE,'TemplateData':'{"who":"日本語"}'}}}
        templ=a.ses('send_email',body)['MessageId']; parsed(owner,templ,'Hello 日本語','Saved 日本語')
        missing=a.ses('send_email',simple(ABSENT))['MessageId']
        rejection=json.loads(direct(owner,'GET',SINK+'/'+missing))
        assert rejection['outcome']=='rejected' and rejection['reason']=='mailbox-not-found' and rejection['contentBytes']==0
        direct(owner,'GET',SINK+'/'+missing+'/content',expected=404)
        forbidden=simple('external@example.org');a.ses('send_email',forbidden,400)
        b.ses('send_email',simple(),400);direct(peer,'GET',SINK+'/'+good,expected=404)
        direct(owner,'GET',SINK.replace(REGION,'us-west-2')+'/'+good,expected=404)
        direct(owner,'PUT','/_simulator/v1/iam/principals/mailer/policy',{'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'ses:SendEmail','Resource':'arn:aws:ses:us-east-1:000000000000:identity/'+DOMAIN}]})
        access=json.loads(direct(owner,'POST','/_simulator/v1/access',{'Principal':'mailer'}))['AccessToken']
        direct(owner,'GET',SINK,expected=403,token=access)
        direct(owner,'POST',SEND,body,403,'ses',access)
        ids={good,templ,missing}
        details=None
        if browser_flow:
            details=browser_flow.shared(owner,peer,access,a,ids)
        assert {r['messageId'] for r in json.loads(direct(owner,'GET',SINK))['messages']}==ids
        owner.close();owner.start();peer.close();peer.start()
        assert parsed(owner,good,'受信確認','literal {{who}}','<b>受信</b>')==blob
        assert json.loads(direct(owner,'GET',SINK+'/'+missing))==rejection
        assert json.loads(direct(peer,'GET',SINK))['messages']==[]
        a.record(zone,tokens[0],'DELETE')
        assert a.ses('get_email_identity',fields)['VerificationStatus']=='PENDING'
        if browser_flow: browser_flow.revoked(owner)
        a.ses('send_email',simple(),400)
        for token in tokens[1:]:a.record(zone,token,'DELETE')
        for ident in ids:direct(owner,'DELETE',SINK+'/'+ident,expected=204)
        assert json.loads(direct(owner,'GET',SINK))['messages']==[]
        a.ses('delete_email_template',{'TemplateName':TEMPLATE})
        for client,z in [(a,zone),(b,peer_zone)]:
            client.ses('delete_email_identity',fields);assert client.ses('list_email_identities',{})['EmailIdentities']==[]
            client.zone(z)
        assert a.sent+b.sent==a.responses+b.responses and (not use_sdk or a.sent+b.sent>=25)
        return dict(stockSDK=use_sdk,signed=a.sent+b.sent,responses=a.responses+b.responses,acceptedFixtures=3,
                    simpleMessageId=good,templateMessageId=templ,rejectedMessageId=missing,
                    localOutcome='delivered',unknownMailboxOutcome=rejection['outcome'],
                    rejectedContentBytes=rejection['contentBytes'],downloadVerifiedAfterRestart=True,
                    reopen=2,cleanup=True,browser=details)

if __name__=='__main__':
    import argparse,hashlib
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--binary',type=Path,required=True);p.add_argument('--binary-sha256',required=True);p.add_argument('--direct-local',action='store_true')
    args=p.parse_args()
    with args.binary.open('rb') as f: need(hashlib.file_digest(f,'sha256').hexdigest()==args.binary_sha256,'wrong binary')
    print(json.dumps(verify(args.binary.resolve(),not args.direct_local)))
