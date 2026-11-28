"""Match actual CodeCommit Console and Scalar writes to SNS notifications received from SQS."""
import json
import time

from codestar_notification_scalar import NotificationReference
from codestar_notifications import ACCOUNT, CREATED, REGION, UPDATED, Refusal, clients


SERVICE = 'codestar-notifications'


class BranchNotificationWitness:
    def __init__(self, browser, team, peer, use_sdk, stack, repository):
        self.use_sdk = use_sdk
        self.stack = stack
        self.team = team
        self.source = repository['Arn']
        self.repository_id = repository['repositoryId']
        self.repository_name = repository['repositoryName']
        self.result = {'stockSDK': use_sdk, 'sends': 0, 'replies': 0, 'deliveries': []}
        self.api = self.client(team, team.token)
        self.topic = self.api('sns', 'CreateTopic', Name='git-console-changes')['TopicArn']
        self.queue = self.api('sqs', 'CreateQueue', QueueName='git-console-changes')['QueueUrl']
        queue_arn = self.api('sqs', 'GetQueueAttributes', QueueUrl=self.queue,
                             AttributeNames=['QueueArn'])['Attributes']['QueueArn']
        self.subscription = self.api('sns', 'Subscribe', TopicArn=self.topic,
                                     Protocol='sqs', Endpoint=queue_arn)['SubscriptionArn']
        policy = {'Version': '2012-10-17', 'Statement': [{
            'Effect': 'Allow', 'Principal': {'Service': 'codestar-notifications.amazonaws.com'},
            'Action': 'SNS:Publish', 'Resource': self.topic,
        }]}
        self.api('sns', 'SetTopicAttributes', TopicArn=self.topic, AttributeName='Policy',
                 AttributeValue=json.dumps(policy))
        reference = NotificationReference(browser, team, team.token, stack)
        self.rule = reference.send('CreateNotificationRule', {
            'Name': 'Git Console changes', 'Resource': self.source, 'DetailType': 'BASIC',
            'EventTypeIds': [CREATED, UPDATED],
            'Targets': [{'TargetType': 'SNS', 'TargetAddress': self.topic}],
        }, 200)['Arn']
        saved = self.api(SERVICE, 'DescribeNotificationRule', Arn=self.rule)
        assert saved['Resource'] == self.source and saved['Status'] == 'ENABLED'
        assert saved['Targets'] == [{'TargetType': 'SNS', 'TargetAddress': self.topic,
                                     'TargetStatus': 'ACTIVE'}]
        self.result['rule'] = self.rule
        self.refusal(self.client(peer, peer.token), 404, 'ResourceNotFoundException')

    def client(self, team, token):
        return clients(team, token, self.stack, not self.use_sdk, self.result)

    def refusal(self, client, status, code):
        try:
            client(SERVICE, 'DescribeNotificationRule', Arn=self.rule)
        except Refusal as error:
            assert (error.status, error.code) == (status, code)
        else:
            raise AssertionError('DescribeNotificationRule returned the other scope’s rule')

    def received(self, branch, commit, previous=None):
        deadline = time.monotonic() + 15
        messages = []
        while not messages and time.monotonic() < deadline:
            messages = self.api('sqs', 'ReceiveMessage', QueueUrl=self.queue,
                                WaitTimeSeconds=1, MaxNumberOfMessages=10).get('Messages', [])
        assert len(messages) == 1, (branch, len(messages))
        item = messages[0]
        transport = json.loads(item['Body'])
        assert transport['Type'] == 'Notification' and transport['TopicArn'] == self.topic
        event = json.loads(transport['Message'])
        assert event['notificationRuleArn'] == self.rule and event['resources'] == [self.source]
        assert (event['source'], event['account'], event['region']) == ('aws.codecommit', ACCOUNT, REGION)
        detail = event['detail']
        expected = {'event': 'referenceUpdated' if previous else 'referenceCreated',
                    'repositoryName': self.repository_name, 'repositoryId': self.repository_id,
                    'referenceType': 'branch', 'referenceName': branch,
                    'referenceFullName': 'refs/heads/' + branch, 'commitId': commit}
        if previous:
            expected['oldCommitId'] = previous
        assert detail == expected, detail
        seen = self.result['deliveries']
        assert item['MessageId'] and all(r['sqsId'] != item['MessageId'] for r in seen)
        assert transport['MessageId'] and all(r['snsId'] != transport['MessageId'] for r in seen)
        self.api('sqs', 'DeleteMessage', QueueUrl=self.queue, ReceiptHandle=item['ReceiptHandle'])
        seen.append({'sqsId': item['MessageId'], 'snsId': transport['MessageId'], **detail})

    def disable(self):
        self.api(SERVICE, 'UpdateNotificationRule', Arn=self.rule, Status='DISABLED')
        assert self.api(SERVICE, 'DescribeNotificationRule', Arn=self.rule)['Status'] == 'DISABLED'

    def quiet_after_commit(self, reason, branch, commit):
        current = self.api('codecommit', 'GetBranch', repositoryName=self.repository_name,
                           branchName=branch)['branch']['commitId']
        assert current == commit
        deadline = time.monotonic() + 1.5
        while time.monotonic() < deadline:
            reply = self.api('sqs', 'ReceiveMessage', QueueUrl=self.queue,
                             MaxNumberOfMessages=10, WaitTimeSeconds=1)
            assert not reply.get('Messages'), 'Unexpected delivery for ' + reason
            time.sleep(0.1)
        self.result[reason] = commit

    def remove_after_restart(self, browser):
        self.api = self.client(self.team, self.team.token)
        reference = NotificationReference(browser, self.team, self.team.token, self.stack)
        saved = reference.send('DescribeNotificationRule', {'Arn': self.rule}, 200, 'Git Console changes')
        assert saved['Resource'] == self.source and saved['Status'] == 'DISABLED'
        assert reference.send('DeleteNotificationRule', {'Arn': self.rule}, 200)['Arn'] == self.rule
        self.refusal(self.api, 404, 'ResourceNotFoundException')
        self.result['ruleReopenedAndDeleted'] = True

    def clean_up(self):
        self.api('sns', 'Unsubscribe', SubscriptionArn=self.subscription)
        self.api('sqs', 'DeleteQueue', QueueUrl=self.queue)
        self.api('sns', 'DeleteTopic', TopicArn=self.topic)
        assert self.result['sends'] == self.result['replies']
        assert len(self.result['deliveries']) == 3
        self.result['cleanup'] = True
