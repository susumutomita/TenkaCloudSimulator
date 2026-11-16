# CloudFront VPC origins

Create an internal Application Load Balancer with the simulated Elastic Load Balancing API, then pass its ARN to CloudFront. An origin and its target live in the same SQLite database. Each origin retains its ID, ARN, creation time, configuration, tags and ETag across process restarts when the simulator is started with a persistent state file.

## SDK operations

Use a CloudFront client with the simulator endpoint and a dummy `TCSIM` access key signed for `us-east-1`. Host authentication uses the same operator token as other services. The four native XML operations use the CloudFront `2020-05-31` API. Create returns the origin ID and its first ETag. Get returns the current revision. Pass that revision in `IfMatch` to update or delete; stale revisions return `PreconditionFailed`. A successful update preserves the ID, and a successful delete makes the ID unavailable to Get.

The simulator accepts up to 50 uniquely named origins. Supported endpoint configuration includes an internal simulated ALB ARN, a name for the origin, `http-only`, and explicit HTTP and HTTPS port numbers. API creation may include up to 50 distinct tags. The target ALB must exist, and it can be deleted after the origin is removed. Create requires `cloudfront:CreateVpcOrigin` on `*`, plus `cloudfront:TagResource` on the new origin ARN when tags are present. Get, update and delete check their respective IAM actions against the origin ARN.

This control plane records the CloudFront-to-ALB association. Traffic delivery, TLS termination, distribution configuration, listing, and tag-management operations are outside this slice.

AWS documents the wire format for [CreateVpcOrigin](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateVpcOrigin.html), [GetVpcOrigin](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetVpcOrigin.html), [UpdateVpcOrigin](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateVpcOrigin.html) and [DeleteVpcOrigin](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteVpcOrigin.html).

## CloudFormation

A stack can create `AWS::CloudFront::VpcOrigin` against an existing simulator-owned internal ALB. Provide its ARN, an origin name and `OriginProtocolPolicy: http-only` in `VpcOriginEndpointConfig`. Optional `HTTPPort` and `HTTPSPort` default to 80 and 443. `Ref` and `Fn::GetAtt Id` return the same ID that the native Get operation accepts. `Fn::GetAtt Arn` returns its global ARN.

Stack and origin creation share one transaction. A failed target lookup rolls back both. The simulator checks the caller's CloudFront create/get permissions and the tag permission for a tagged origin. DeleteStack checks get/delete permissions and the recorded ETag before releasing the origin; a direct update through CloudFront therefore surfaces as drift. Stack creation and deletion are supported. The simulator's CloudFormation API does not implement UpdateStack.
