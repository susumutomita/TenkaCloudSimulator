# Local CloudFront distributions and saved S3 content

This is a bounded part of [issue #238](https://github.com/susumutomita/TenkaCloudSimulator/issues/238). The existing [VPC origin configuration](cloudfront-vpc-origin.md) remains separate. This workflow does not fetch an ALB, VPC origin, arbitrary URL or external S3 endpoint. It reads saved objects from this process's own SQLite database. It adds no networking, TLS endpoint, DNS, global edge locations or live CDN.

## Native API

The global CloudFront REST/XML version is `2020-05-31`; use the normal local SDK endpoint and dummy `TCSIM` credentials with the existing local token. Sign CloudFront control requests for `us-east-1`. Temporary STS credentials are not added by this increment.

| Operation | Native method and path | Authorization |
| --- | --- | --- |
| [CreateDistribution](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateDistribution.html) | `POST /2020-05-31/distribution`, 201 with Distribution, ETag and Location | `cloudfront:CreateDistribution` on `*` |
| [GetDistribution](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetDistribution.html) | `GET /2020-05-31/distribution/{Id}`, 200 with Distribution and ETag | `cloudfront:GetDistribution` on the exact distribution ARN |
| [GetDistributionConfig](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetDistributionConfig.html) | `GET /2020-05-31/distribution/{Id}/config`, 200 with DistributionConfig and ETag | `cloudfront:GetDistributionConfig` on the ARN |
| [UpdateDistribution](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateDistribution.html) | `PUT /2020-05-31/distribution/{Id}/config`, complete config and current If-Match, 200 with new ETag | `cloudfront:UpdateDistribution` on the ARN |
| [DeleteDistribution](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteDistribution.html) | `DELETE /2020-05-31/distribution/{Id}`, current If-Match, 204 | `cloudfront:DeleteDistribution` on the ARN |
| [ListDistributions](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListDistributions.html) | `GET /2020-05-31/distribution`, optional Marker and MaxItems | `cloudfront:ListDistributions` on `*` |
| [CreateInvalidation](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateInvalidation.html) | `POST /2020-05-31/distribution/{Id}/invalidation`, 201 with Invalidation and Location | `cloudfront:CreateInvalidation` on the distribution ARN |
| [GetInvalidation](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetInvalidation.html) | `GET /2020-05-31/distribution/{Id}/invalidation/{InvalidationId}`, 200 | `cloudfront:GetInvalidation` on the distribution ARN |

ARNs are `arn:aws:cloudfront::000000000000:distribution/{Id}`. The [official IAM action table](https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazoncloudfront.html) defines the resource support above. An explicit Deny wins. Management does not grant S3 object permission.

The accepted configuration has the five required top-level fields: CallerReference, Origins, DefaultCacheBehavior, Comment and Enabled. It requires exactly one Origin with Id, DomainName=`{existing-bucket}.s3.amazonaws.com`, and S3OriginConfig with an empty OriginAccessIdentity. The domain is only a validated identifier for a saved local bucket; the simulator never resolves or connects to it.

The default cache behavior targets that origin, uses ViewerProtocolPolicy=`allow-all`, AllowedMethods and CachedMethods containing exactly GET and HEAD, and ForwardedValues with QueryString=false and Cookies.Forward=`none`. Supply integer MinTTL, DefaultTTL and MaxTTL with `0 <= MinTTL <= DefaultTTL <= MaxTTL <= 86400`. The direct local S3 snapshot has no origin Cache-Control headers: **DefaultTTL determines its server-owned cache lifetime**. This is not the full AWS origin-header interpretation of MinTTL/MaxTTL. Zero DefaultTTL bypasses storage in the cache. Comment and CallerReference have at most 128 UTF-8 bytes; CallerReference is nonempty and immutable. A reused distribution CallerReference returns DistributionAlreadyExists.

There are at most 100 distributions. Lists sort IDs ascending; MaxItems defaults to 25 and accepts 1–50, with IsTruncated and NextMarker when another page exists. Each page reauthorizes. A list is not a frozen snapshot across later creates/deletes. Updates complete synchronously with Status=`Deployed`, produce a new ETag, and evict this distribution's cache. Missing If-Match returns InvalidIfMatchVersion (400); an old value returns PreconditionFailed (412). Disable before deleting; otherwise DistributionNotDisabled (409). Deletion removes its retained invalidations and cached bytes.

An invalidation supplies nonempty CallerReference and matching Paths.Quantity/Items, 1–100 distinct paths. A path is an exact supported object path such as `/assets/message.txt`, or `/*`. A successful transaction evicts only this distribution's matching cache entries and records Status=`Completed`. Reusing a caller reference with the same ordered paths returns the existing batch without performing another eviction; different paths return InvalidationBatchAlreadyExists. Up to 1,000 batches per distribution are retained until its deletion. This local retained-record limit is not a claim about AWS in-progress concurrency quotas.

Unknown XML fields, repeated scalar fields, mismatched quantities, unsupported methods and options refuse before a mutation. Missing resources use NoSuchDistribution, NoSuchOrigin or NoSuchInvalidation (404). Requests are limited to 32 KiB. Aliases, custom/VPC/ALB origins, origin paths, ordered cache behaviors, origin groups, default root objects, managed cache-policy IDs, forwarded query/cookies, certificates, signing/OAC/OAI, Lambda@Edge/functions, tags, logging, WAF and distribution CloudFormation are unsupported. The Console reads distributions and saved content and creates invalidations; distribution create/configuration update/delete remain SDK/API-reference operations. The existing VPC origin CloudFormation type and ALB placement work are unchanged.

## Read actual saved bytes

The native Distribution.DomainName is a **simulator-only placeholder** `{Id}.cloudfront.invalid`. Never request that hostname. The authenticated, simulator-only delivery route is:

```text
GET  /_simulator/cloudfront/distributions/{Id}/objects/{key}
HEAD /_simulator/cloudfront/distributions/{Id}/objects/{key}
```

This route is not an invented AWS CloudFront wire API or IAM action. It uses the normal local token/browser session and one fresh shared identity-policy snapshot. Every hit and miss requires both `cloudfront:GetDistribution` on the exact distribution ARN and `s3:GetObject` on `arn:aws:s3:::{bucket}/{key}`. A cached result cannot retain a revoked permission. Cache keys include the distribution revision, selected principal and exact object key. Processes/databases/token authorities remain separate per team.

Keys contain 1–1,024 ASCII bytes: letters, digits, `.`, `_`, `-` and `/`, start with a letter or digit, and have no empty, `.` or `..` segments. Queries, percent-encoded paths, range and conditional requests are unsupported. Objects are at most 1 MiB. Content-Type, ETag, Content-Length and bytes come from the same saved S3 object snapshot; HEAD omits the body. Both methods force attachment download, a sandbox/content-isolation CSP and nosniff, so saved content is not an active Console page. X-Cache reports a local Hit or Miss, and Age is seconds since that cache fill. Responses remain `Cache-Control: no-store` so browser caching cannot bypass later server authorization. The internal TTL cache persists with the database, with at most 128 entries and 32 MiB of bytes across the process; capacity exhaustion is an explicit 503. Expired entries are reclaimed on a miss. A clock earlier than a cache fill also invalidates that entry.

S3 changes do not replace unexpired cached bytes until TTL expiry, invalidation or distribution update. Disabled distributions refuse delivery. Every read checks that the origin bucket still exists and its stored creation timestamp and region match the captured lifetime fingerprint; a missing or ordinarily deleted/recreated bucket refuses even a cache hit. An explicit authorized distribution update rebinds the current bucket fingerprint and clears old bytes. The existing S3 creation timestamp is wall-clock RFC 3339 nanoseconds, **not a monotonic generation**: this fingerprint does not prove ABA resistance if a clock rollback reproduces exactly the same timestamp and region. No stronger bucket identity or new core lifecycle is introduced.

## Verification and remaining scope

The [official SDK fixture](../test/e2e/cloudfront_local_content.py) uses pinned boto3/botocore, two owned CLI processes, real native S3 writes, real distribution/cache/invalidation reads, permission revocation, restart, peer readback and native cleanup before deleting temporary databases. Its ordinary SDK configuration guards are preserved. The [served Scalar reference](/_simulator/docs/cloudfront-local.html) uses the same native state and normal browser session; examples are static inputs and require their local bucket to exist first. It lists the eight native management operations and separately labels the existing simulator-only local content GET. The [opt-in browser fixture](../test/e2e/cloudfront_local_scalar.py) performs two native Sends (GetDistribution and CreateInvalidation), then one explicitly simulator-only content Send and a real response download. The changed bytes are not inserted into an AWS management response. Actual browser Send evidence is separate from serving this document.

Focused native tests cover cache snapshots, TTL, HEAD, invalidation idempotency, disabled/ETag rules, read denials, origin lifetime fingerprints, restart, peer isolation, malformed XML and transaction failure/cancellation. The finite CloudFrontLocalCache model abstracts one distribution/object/cache line, two actors and four transitions; it does not prove HTTP, authentication, concrete quotas, SQL or Go refinement. The Console workflow covers distribution selection, saved-content downloads and invalidation. Use the native API to create, reconfigure or delete a distribution. The local origin supports saved S3 objects only.

## Console: distributions, saved content and invalidation

Open **Services → CloudFront → Distributions**. This global view reads 25 native distribution summaries per page. Use **Next page**, **Previous page** or **Refresh**; select an ID to read its actual origin, cache TTLs and current ETag. This view does not create a distribution or edit its configuration.

Enter an existing supported object key and choose **Read local content**. The preview is escaped text, limited to the first 64 KiB. **Download saved bytes** downloads all returned bytes as an inert attachment, even when the stored content type is HTML. The original native attachment/CSP/nosniff protections are checked before content is accepted. The local cache result, age and object ETag are displayed; the placeholder `.cloudfront.invalid` hostname is never opened.

Choose **Create invalidation**, confirm `/*` for the selected distribution and submit. The displayed caller reference identifies that exact request. The Console shows only the actual saved receipt returned by the native operation; **Refresh invalidation** uses its real ID. After an uncertain response, **Replay same invalidation** asks for confirmation and sends the same caller reference and paths. It does not automatically retry or invent an ID. Read the object again after completion to observe current saved S3 bytes.

Native IAM still authorizes every operation, including both distribution and S3 permissions on cached reads. Changing identity, region, route or distribution clears previous content, downloads and receipts and aborts old reads. This is a bounded EN/JA Cloudscape view, not an external CDN, origin connection or distribution configuration editor.
