"""Prepared real-browser/native state acceptance. UNRUN until root allocation.

Fresh local CLIs/tokens only. No routed API fulfillment or live cloud resources.
New runner checks are separate from Scalar Send and its outstanding ARIA issue.
"""
import json
import hashlib
import os
from pathlib import Path
import shutil
import sqlite3
from xml.etree import ElementTree as ET
import tempfile
from urllib.parse import urlsplit

import boto3
from botocore.config import Config
from playwright.sync_api import expect
from check import running, sync_playwright, assert_reflow


def client(service, endpoint, token):
    parsed=urlsplit(endpoint)
    assert parsed.scheme=='http' and parsed.hostname=='127.0.0.1' and parsed.port
    sdk=boto3.client(service,endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMEXAMPLE',aws_secret_access_key='simulator-only',config=Config(retries={'max_attempts':0}))
    def guard(request,**_):
        actual=urlsplit(request.url)
        assert actual.scheme==parsed.scheme and actual.netloc==parsed.netloc
        request.headers['X-Simulator-Token']=token
    sdk.meta.events.register('before-sign.*.*',guard)
    return sdk


def run():
    axe_path=Path(os.environ['AXE_SCRIPT'])
    provenance=json.loads((axe_path.parent/'provenance.json').read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest()==provenance['bundleSha256']
    with tempfile.TemporaryDirectory(prefix='reference-runner-owned-') as directory:
        with running(Path(directory)/'a.db',authenticated=True) as (endpoint,token),running(Path(directory)/'b.db',authenticated=True) as (peer_endpoint,peer_token):
            sqs,peer_sqs=client('sqs',endpoint,token),client('sqs',peer_endpoint,peer_token)
            logs=client('logs',endpoint,token)
            s3=client('s3',endpoint,token)
            sns,peer_sns=client('sns',endpoint,token),client('sns',peer_endpoint,peer_token)
            try:
                peer_url=peer_sqs.create_queue(QueueName='runner-practice')['QueueUrl']
                peer_topic=peer_sns.create_topic(Name='runner-topic')['TopicArn']
                with sync_playwright() as p:
                    browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE')or shutil.which('chromium'),args=['--no-sandbox'])
                    try:
                        context=browser.new_context(viewport={'width':1440,'height':1000})
                        try:
                            outside=[]
                            def local_only(route):
                                if urlsplit(route.request.url).netloc==urlsplit(endpoint).netloc:route.continue_()
                                else:outside.append(True);route.abort()
                            context.route('**/*',local_only)
                            # Docs themselves stay protected: no anonymous runner is rendered.
                            initial=context.request.get(endpoint+'/_simulator/docs/',max_redirects=0)
                            assert initial.status==303 and initial.headers.get('location')=='/_simulator/login'
                            page=context.new_page();page.goto(endpoint+'/_simulator/docs/')
                            assert page.url==endpoint+'/_simulator/login'
                            assert context.request.post(endpoint+'/_simulator/login',form={'token':token},max_redirects=0).status==303
                            page.goto(endpoint+'/_simulator/docs/')
                            root=page.locator('#native-execution')
                            expect(root.get_by_role('status')).to_contain_text('Executable coverage validated.')
                            # Revoke the real browser session while keeping the mounted UI.
                            # Passive capability loading must expose 401 without renewing/replaying.
                            assert context.request.post(endpoint+'/_simulator/logout',max_redirects=0).status==204
                            with page.expect_response(lambda r:r.url==endpoint+'/_simulator/v1/capabilities' and r.request.method=='GET') as expired:
                                root.get_by_role('button',name='Reload executable coverage',exact=True).click()
                            assert expired.value.status==401
                            expect(root.get_by_role('status')).to_contain_text('Not authenticated (401)')
                            expect(root.get_by_label('Exercise identity (operator only)',exact=True)).not_to_be_visible()
                            expect(root.get_by_role('button',name='Send to local API',exact=True)).to_be_disabled()
                            assert context.request.post(endpoint+'/_simulator/login',form={'token':token},max_redirects=0).status==303
                            root.get_by_role('button',name='Reload executable coverage',exact=True).click()
                            expect(root.get_by_role('status')).to_contain_text('Executable coverage validated.')
                            def select(service,operation):
                                root.get_by_label('Service',exact=True).select_option(service)
                                root.get_by_label('Operation',exact=True).select_option(operation)
                            def send(value,status=200):
                                root.get_by_label('Native request body',exact=True).fill(json.dumps(value))
                                with page.expect_response(lambda r:r.url==endpoint+'/' and r.request.method=='POST') as pending:
                                    root.get_by_role('button',name='Send to local API',exact=True).click()
                                assert pending.value.status==status
                                expect(root.get_by_label('Actual response status',exact=True)).to_contain_text(str(status))
                                return pending.value.json(),pending.value.request
                            select('sqs','CreateQueue');created,request=send({'QueueName':'runner-practice'})
                            url=created['QueueUrl'];assert url in sqs.list_queues()['QueueUrls']
                            assert request.headers['content-type']=='application/x-amz-json-1.0'
                            assert request.headers['x-amz-target']=='AmazonSQS.CreateQueue'
                            select('sqs','SendMessage');root.get_by_role('button',name='Use last actual QueueUrl in this draft',exact=True).click()
                            draft=json.loads(root.get_by_label('Native request body',exact=True).input_value());assert draft['QueueUrl']==url
                            send({'QueueUrl':url,'MessageBody':'runner dummy message'})
                            select('sqs','ReceiveMessage');received,_=send({'QueueUrl':url,'MaxNumberOfMessages':1})
                            handle=received['Messages'][0]['ReceiptHandle'];assert received['Messages'][0]['Body']=='runner dummy message'
                            select('sqs','DeleteMessage');root.get_by_role('button',name='Use last actual QueueUrl in this draft',exact=True).click();root.get_by_role('button',name='Use last actual ReceiptHandle in this draft',exact=True).click()
                            draft=json.loads(root.get_by_label('Native request body',exact=True).input_value());assert draft['QueueUrl']==url and draft['ReceiptHandle']==handle
                            send(draft);assert sqs.receive_message(QueueUrl=url).get('Messages',[])==[]
                            select('sqs','DeleteQueue');send({'QueueUrl':url});assert sqs.list_queues().get('QueueUrls',[])==[]
                            group,stream='/reference/runner','application'
                            select('logs','CreateLogGroup');_,request=send({'logGroupName':group});assert request.headers['content-type']=='application/x-amz-json-1.1';assert request.headers['x-amz-target']=='Logs_20140328.CreateLogGroup'
                            select('logs','CreateLogStream');send({'logGroupName':group,'logStreamName':stream})
                            select('logs','PutLogEvents');value=json.loads(root.get_by_label('Native request body',exact=True).input_value());value.update(logGroupName=group,logStreamName=stream);value['logEvents'][0]['message']='runner dummy event';send(value)
                            assert logs.get_log_events(logGroupName=group,logStreamName=stream)['events'][0]['message']=='runner dummy event'
                            select('logs','GetLogEvents');value,_=send({'logGroupName':group,'logStreamName':stream});assert value['events'][0]['message']=='runner dummy event'
                            select('logs','DeleteLogGroup');send({'logGroupName':group});assert logs.describe_log_groups()['logGroups']==[]
                            # SNS is native Query/XML and only stores local deliveries.
                            ns='{http://sns.amazonaws.com/doc/2010-03-31/}'
                            def query(value,status=200):
                                root.get_by_label('Native request body',exact=True).fill(json.dumps(value))
                                with page.expect_response(lambda r:r.url==endpoint+'/' and r.request.method=='POST') as pending:
                                    root.get_by_role('button',name='Send to local API',exact=True).click()
                                response=pending.value;assert response.status==status
                                assert response.request.headers['x-simulator-service']=='sns'
                                assert response.request.headers['content-type']=='application/x-www-form-urlencoded'
                                assert 'x-amz-target' not in response.request.headers
                                return ET.fromstring(response.text())
                            def sns_fields(operation,**values):return {'Action':operation,'Version':'2010-03-31',**values}
                            select('sns','CreateTopic');xml=query(sns_fields('CreateTopic',Name='runner-topic'))
                            topic=xml.findtext(ns+'CreateTopicResult/'+ns+'TopicArn');assert topic
                            select('sns','ListTopics');xml=query(sns_fields('ListTopics'));assert topic in [x.text for x in xml.findall('.//'+ns+'TopicArn')]
                            select('sns','GetTopicAttributes');query(sns_fields('GetTopicAttributes',TopicArn=topic))
                            select('sns','Subscribe');xml=query(sns_fields('Subscribe',TopicArn=topic,Protocol='email',Endpoint='runner@example.invalid',ReturnSubscriptionArn='true'))
                            subscription=xml.findtext(ns+'SubscribeResult/'+ns+'SubscriptionArn');assert subscription
                            select('sns','ListSubscriptions');xml=query(sns_fields('ListSubscriptions'));assert subscription in [x.text for x in xml.findall('.//'+ns+'SubscriptionArn')]
                            select('sns','ListSubscriptionsByTopic');xml=query(sns_fields('ListSubscriptionsByTopic',TopicArn=topic));assert subscription in [x.text for x in xml.findall('.//'+ns+'SubscriptionArn')]
                            select('sns','Publish');xml=query(sns_fields('Publish',TopicArn=topic,Message='runner local message'))
                            message_id=xml.findtext(ns+'PublishResult/'+ns+'MessageId');assert message_id
                            assert sns.get_topic_attributes(TopicArn=topic)['Attributes']['SubscriptionsConfirmed']=='1'
                            with sqlite3.connect('file:'+str(Path(directory)/'a.db')+'?mode=ro',uri=True) as db:
                                assert db.execute('SELECT message FROM aws_sns_messages WHERE id=? AND topic_arn=?',(message_id,topic)).fetchall()==[('runner local message',)]
                                assert db.execute('SELECT count(*) FROM aws_sns_deliveries WHERE message_id=?',(message_id,)).fetchone()[0]==1
                            with sqlite3.connect('file:'+str(Path(directory)/'b.db')+'?mode=ro',uri=True) as db:
                                assert db.execute('SELECT count(*) FROM aws_sns_messages').fetchone()[0]==0
                            select('sns','Unsubscribe');query(sns_fields('Unsubscribe',SubscriptionArn=subscription));assert sns.list_subscriptions_by_topic(TopicArn=topic)['Subscriptions']==[]
                            select('sns','DeleteTopic');query(sns_fields('DeleteTopic',TopicArn=topic));assert sns.list_topics()['Topics']==[]
                            with sqlite3.connect('file:'+str(Path(directory)/'a.db')+'?mode=ro',uri=True) as db:
                                assert db.execute('SELECT count(*) FROM aws_sns_messages').fetchone()[0]==0
                                assert db.execute('SELECT count(*) FROM aws_sns_deliveries').fetchone()[0]==0
                            assert peer_sns.list_topics()['Topics']==[{'TopicArn':peer_topic}]
                            peer_sns.delete_topic(TopicArn=peer_topic)
                            select('iam','ListRoles')
                            # Query is XML; inspect the real response separately.
                            root.get_by_label('Native request body',exact=True).fill(json.dumps({'Action':'ListRoles','Version':'2010-05-08'}))
                            with page.expect_response(lambda r:r.url==endpoint+'/' and r.request.method=='POST') as pending:root.get_by_role('button',name='Send to local API',exact=True).click()
                            assert pending.value.status==200 and 'x-amz-target' not in pending.value.request.headers
                            assert pending.value.request.headers['x-simulator-service']=='iam'
                            select('sqs','ListQueues');root.get_by_label('Exercise identity (operator only)',exact=True).fill('reference-denied');root.get_by_label('Exercise identity (operator only)',exact=True).blur()
                            send({},403);expect(root.get_by_role('status')).to_contain_text('Denied (403)')
                            root.get_by_label('Exercise identity (operator only)',exact=True).fill('simulator-admin');root.get_by_label('Exercise identity (operator only)',exact=True).blur()
                            # A real participant session cannot select an operator principal.
                            minted=context.request.post(endpoint+'/_simulator/v1/access',data={'Principal':'reference-denied'})
                            assert minted.status==200
                            participant=browser.new_context()
                            try:
                                assert participant.request.post(endpoint+'/_simulator/login',form={'token':minted.json()['AccessToken']},max_redirects=0).status==303
                                participant_page=participant.new_page();participant_page.goto(endpoint+'/_simulator/docs/')
                                expect(participant_page.locator('#native-execution').get_by_label('Exercise identity (operator only)',exact=True)).not_to_be_visible()
                            finally:participant.close()
                            for width in (1440,390):
                                page.set_viewport_size({'width':width,'height':1000});assert_reflow(page)
                                # Audit first-party runner only: unrelated Scalar defect remains separate.
                                page.evaluate(Path(os.environ['AXE_SCRIPT']).read_text())
                                result=page.evaluate("async()=>{const r=await axe.run(document.getElementById('native-execution'),{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}});return r.violations.map(v=>v.id)}")
                                assert result==[]
                            native_posts=[]
                            page.on('request',lambda r:native_posts.append(True) if r.url==endpoint+'/' and r.method=='POST' else None)
                            before_body=root.get_by_label('Native request body',exact=True).input_value()
                            root.get_by_label('Language',exact=True).select_option('ja')
                            expect(root.get_by_label('ネイティブリクエスト本文',exact=True)).to_have_value(before_body)
                            expect(root.get_by_role('button',name='ローカル API へ送信',exact=True)).to_be_visible();expect(root.get_by_label('操作',exact=True)).to_have_value('ListQueues')
                            page.reload();expect(root.get_by_role('button',name='ローカル API へ送信',exact=True)).to_be_visible()
                            expect(root.get_by_role('status')).to_contain_text('実行定義の対応範囲を検証しました。')
                            assert page.evaluate("localStorage.getItem('tenkacloud-simulator-locale')")=='ja'
                            before_body=root.get_by_label('ネイティブリクエスト本文',exact=True).input_value()
                            other=context.new_page()
                            try:
                                other.goto(endpoint+'/_simulator/docs/')
                                other.evaluate("localStorage.setItem('tenkacloud-simulator-locale','en')")
                                expect(root.get_by_role('button',name='Send to local API',exact=True)).to_be_visible()
                                expect(root.get_by_label('Native request body',exact=True)).to_have_value(before_body)
                                page.evaluate('() => new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))')
                                assert native_posts==[],'Presentation language change issued a native operation'
                            finally:other.close()
                            # Phase2: actual Scalar chrome + live S3 reader locale.
                            # Existing unrelated full-page Scalar ARIA remains a separate gate.
                            s3.create_bucket(Bucket='runner-live-bucket')
                            root.get_by_label('Language',exact=True).select_option('ja')
                            page.goto(endpoint+'/_simulator/docs/#tag/s3-basic-object-storage/GET/')
                            expect(page.get_by_role('button',name='リクエストを試す (get /)',exact=True)).to_be_visible(timeout=30000)
                            page.get_by_role('button',name='リクエストを試す (get /)',exact=True).click()
                            with page.expect_response(lambda r:r.request.method=='GET' and r.url.rstrip('/')==endpoint) as actual_s3:
                                page.get_by_role('button').filter(has_text='リクエストを送信').last.click()
                            assert actual_s3.value.status==200 and 'runner-live-bucket' in actual_s3.value.text()
                            expect(page.locator('#s3-result-trigger')).to_contain_text('実際の S3 結果')
                            page.locator('#s3-result-trigger').click()
                            reader=page.get_by_role('dialog',name='実際の S3 応答を理解する',exact=True)
                            expect(reader).to_be_visible();expect(reader.get_by_role('heading',name='runner-live-bucket',exact=True)).to_be_visible()
                            for width in (1440,390):
                                page.set_viewport_size({'width':width,'height':1000});assert_reflow(page)
                                result=page.evaluate("async()=>{const r=await axe.run(document.getElementById('s3-result-dialog'),{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}});return r.violations.map(v=>v.id)}")
                                assert result==[]
                            reader.get_by_role('button',name='閉じる',exact=True).click()
                            page.get_by_role('button',name='クライアントを閉じる',exact=True).click()
                            root.get_by_label('言語',exact=True).select_option('en')
                            expect(page.locator('#s3-result-trigger')).to_contain_text('Live S3 result')
                            assert s3.list_buckets()['Buckets'][0]['Name']=='runner-live-bucket'
                            s3.delete_bucket(Bucket='runner-live-bucket')
                            assert peer_sqs.list_queues()['QueueUrls']==[peer_url]
                            assert not outside
                        finally:context.close()
                    finally:browser.close()
            finally:
                sqs.close();peer_sqs.close();logs.close();s3.close();sns.close();peer_sns.close()
    print(json.dumps({'scope':'Actual runnerSQS/Logs/SNSlocalrecords/nativeSDK/twoDB/authdeny/participant/ENJA/1440+390 firstpartyaxe when executed','ScalarSend':'ActualS3GETwhenexecuted;notMetricsScalarSend/fullScalarARIA' ,'status':'PASS'}))

if __name__=='__main__':run()
