# AWS CodeStar Notifications

The standalone AWS simulator supports five regional REST JSON operations with the pinned `codestar-notifications` SDK. A rule connects a real local CodeCommit repository to local Standard SNS topics. Successful delivery publishes a native SNS message and a native SQS message for every subscribed queue. Use an ordinary signed-in local exercise identity in the same team. [AWS notification rules](https://docs.aws.amazon.com/dtconsole/latest/userguide/notification-rules.html)

| Operation | HTTP request | Local behavior |
| --- | --- | --- |
| CreateNotificationRule | POST /createNotificationRule | Saves the repository's physical identity, event selection, target topics and creator. Repeating the same client token and parameters returns the original ARN while the rule exists. |
| DescribeNotificationRule | POST /describeNotificationRule | Returns the persisted rule, event summaries and current target status. |
| ListNotificationRules | POST /listNotificationRules | Lists IDs and ARNs newest first. RESOURCE, CREATED_BY, EVENT_TYPE_ID and TARGET_ADDRESS support alternatives under one name and conjunction across names. Continuations are scoped to the actor, region, query and server process. |
| UpdateNotificationRule | POST /updateNotificationRule | Changes the name, status, event selection or targets and cancels undelivered work for the previous configuration. |
| DeleteNotificationRule | POST /deleteNotificationRule | Deletes the rule and its undelivered work. Published messages remain available through SNS and SQS. |

Use `Content-Type: application/json` and sign for `codestar-notifications`. The local API reference can select the same service with `X-Simulator-Service`. Create, describe, update and delete require the corresponding IAM action on the rule ARN. List requires `codestar-notifications:ListNotificationRules` on `*`. The local exercise also requires `codecommit:GetRepository` on the source ARN for create and update. The ordinary CodeCommit writer needs the CodeCommit action for their change. [Service authorization](https://docs.aws.amazon.com/service-authorization/latest/reference/list_codestar-notifications.html)

After signing in, open the local Scalar reference at `/_simulator/docs/codestar-notifications.html`. The request editor sends the selected operation to the current simulator. Copy a real returned rule ARN into the describe, update and delete requests.

## Configure delivery

1. Create a Standard topic and one or more Standard SQS queues in the repository region. Use the native SNS Subscribe operation with protocol `sqs` and each queue ARN.
2. Use SNS SetTopicAttributes with AttributeName `Policy`. The supported service-policy statement grants Principal `{"Service":"codestar-notifications.amazonaws.com"}` action `SNS:Publish` on the exact topic ARN. An applicable explicit Deny overrides an Allow. GetTopicAttributes returns the stored policy. For the SQS part of this local exercise, grant the rule creator `sqs:SendMessage` on each destination queue. [AWS target topic policy](https://docs.aws.amazon.com/dtconsole/latest/userguide/set-up-sns.html)
3. Create a rule with the local repository ARN, detail type `BASIC`, and zero to ten distinct SNS targets. Select `codecommit-repository-branches-and-tags-created`, `codecommit-repository-branches-and-tags-updated`, or both. The first PutFile on an empty repository and CreateBranch produce branch-creation events. PutFile advancing an existing branch produces a branch-update event with its previous tip. Tag events and other AWS event sources are outside this exercise. [AWS CodeCommit events](https://docs.aws.amazon.com/codecommit/latest/userguide/monitoring-events.html)
4. Use SQS ReceiveMessage. Decode the SNS `Message` string to read the repository ID and ARN, branch name, new commit ID, rule ARN and event time. Configuration, failed Git mutations and pre-existing history produce no deliveries.

## Persistence and limits

The repository transaction commits the new branch tip and each matching target's pending record together. A worker checks the original topic generation, subscription IDs and queue generations, then reloads the rule creator's policy in the delivery transaction. It commits native SNS and SQS writes, a delivery receipt and pending-record removal together. A temporarily denied service or queue grant schedules a retry after two seconds and the underlying Git change remains available. Restarting the standalone executable resumes pending delivery. Replacing a rule, topic, subscription, queue or repository cancels stale delivery. A queue re-created under the same name requires fresh SNS subscriptions. Deleting and re-creating a participant cannot revive delivery authority registered by the previous participant.

The local quotas are 256 rules per team, ten rules per repository, ten targets per rule, one to twenty existing SQS subscribers per configured target, 2,048 pending target records per team, 64 KiB per request body, and ten filters and 100 results per list request. A Git mutation that cannot reserve enough pending records returns HTTP 503 ServiceUnavailable and keeps its previous tip. [CodeCommit common errors](https://docs.aws.amazon.com/codecommit/latest/APIReference/CommonErrors.html) Clearing or draining the backlog frees capacity. Use DeleteNotificationRule, native SNS/SQS cleanup and DeleteRepository to remove exercise resources. Tags, FULL enrichment, Chatbot, FIFO, HTTP delivery and additional notification-rule operations are outside the implemented scope.
