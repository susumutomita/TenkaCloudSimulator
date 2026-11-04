# API Gateway REST subset

This AWS provider implements a bounded Regional REST API/Lambda proxy slice in the Go binary. It creates no public endpoint and does not require real AWS, host Python, Node, Docker or arbitrary network access at runtime. It is an independent simulator, not an AWS console or service operated by Amazon.

## Actual operations and state

Native management paths use `X-Simulator-Service: apigateway` and `X-Simulator-Region` for unsigned console/reference requests. A dummy SDK signature scope selects the same handler; access keys must start with `TCSIM`. The local host token/session is required independently by the CLI. Management IAM uses `apigateway:GET`, `POST`, `PUT` or `DELETE` and the actual `arn:aws:apigateway:REGION::/restapis/...` resource. Unsupported regions, session credentials, mixed scope/region and unknown body/query fields fail explicitly. Canonical field-name checking covers top-level management fields; the nested endpoint types field is normalized through its typed decoder.

| SDK operation | Method and path | Behavior |
|---|---|---|
| CreateRestApi | POST /restapis | Explicit REGIONAL configuration, name, description and tags |
| GetRestApis | GET /restapis | Regional list; limit/position pagination |
| GetRestApi / DeleteRestApi | GET / DELETE /restapis/{id} | API metadata / atomic removal of stages, deployments and resources |
| GetResources | GET /restapis/{id}/resources | Resource tree with method/integration state |
| CreateResource | POST /restapis/{id}/resources/{parent} | Literal, single variable or terminal nonempty greedy path |
| GetResource / DeleteResource | GET / DELETE /restapis/{id}/resources/{resource} | Read / remove mutable subtree; root cannot be deleted |
| PutMethod / GetMethod | PUT / GET /restapis/{id}/resources/{resource}/methods/{method} | NONE authorization; no API keys; GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS/ANY |
| PutIntegration | PUT .../methods/{method}/integration | AWS_PROXY, POST, unqualified local Lambda URI in the same region |
| CreateDeployment / GetDeployment | POST /deployments, GET /deployments/{deployment} under an API | Immutable snapshot; all methods must have integrations |
| CreateStage | POST /restapis/{id}/stages | New stage bound to one existing deployment |
| GetStage / DeleteStage | GET / DELETE /restapis/{id}/stages/{stage} | Read or remove a stage |

AWS wire list responses use `item`; boto3 exposes that member as `items`. Default page size is25, maximum500. Local positions are the previous page's last ID. CreateDeployment accepts a description only; use a separate CreateStage call. It does not implicitly create or modify a stage. Mutable resource/integration edits and resource deletion leave existing deployment snapshots unchanged. A more-specific resource is selected before method lookup: a missing method does not fall back to a wildcard ANY resource.

## Run without real AWS

Use the host's simulator endpoint and opaque local token. All example data below are dummy values; no real keys are needed. `boto3` is development/client tooling, not a simulator runtime dependency.

```python
import boto3
from botocore.config import Config

def client(service):
    c = boto3.client(service, endpoint_url=endpoint, region_name="us-east-1",
        aws_access_key_id="TCSIMLOCAL", aws_secret_access_key="dummy",
        config=Config(retries={"max_attempts": 0}))
    c.meta.events.register("before-sign." + service,
        lambda request, **kwargs: request.headers.add_header("X-Simulator-Token", token))
    return c

api, functions = client("apigateway"), client("lambda")
rest = api.create_rest_api(name="example-rest",
    endpointConfiguration={"types": ["REGIONAL"]})
api.put_method(restApiId=rest["id"], resourceId=rest["rootResourceId"],
    httpMethod="ANY", authorizationType="NONE")
# First create a real local Python function and its execution role in Lambda/IAM.
uri = ("arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/"
       "arn:aws:lambda:us-east-1:000000000000:function:example-handler/invocations")
api.put_integration(restApiId=rest["id"], resourceId=rest["rootResourceId"],
    httpMethod="ANY", type="AWS_PROXY", integrationHttpMethod="POST", uri=uri)
functions.add_permission(FunctionName="example-handler", StatementId="allow-api",
    Principal="apigateway.amazonaws.com", Action="lambda:InvokeFunction",
    SourceArn=f"arn:aws:execute-api:us-east-1:000000000000:{rest['id']}/prod/*/*")
deployment = api.create_deployment(restApiId=rest["id"])
api.create_stage(restApiId=rest["id"], stageName="prod", deploymentId=deployment["id"])
```

The simulator-only invocation path is `/_simulator/providers/aws/_execute-api/{region}/{apiId}/{stage}/...`. It remains behind local access authentication even when the deployed method uses NONE. Supply `X-Simulator-Token`, or use the console's same-origin authenticated session. An application Bearer value may be passed separately in Authorization; AWS session/signature credentials, simulator authority headers and all Cookie headers are not forwarded to the function. Cookies are deliberately unavailable to the guest, including explicitly supplied application cookies: cookies are shared across local ports and this transport has no separate app origin. Use explicitly supplied application Bearer data instead. Browser application hosting and anonymous public access are unavailable.

The supported requestContext contains accountId/apiId/stage/requestId/resourcePath/path/httpMethod; identity/authorizer/domain/time fields are not synthesized. REST v1 events preserve the actual path separately from the template, path variables, stage variables and duplicate query/header values. The function executes in the fixed CPython/WASI sandbox with its saved execution role and the existing bounded S3/SSM bridge. The provider does not call a user-selected URL or grant a guest-selected identity. Function code, service resource permission and execution-role trust are captured in one database transaction before execution. Integration execution has a29-second deadline; function timeout and sandbox limits still apply.

## Resource permissions and safe responses

Native Lambda AddPermission/GetPolicy/RemovePermission use the usual function `/policy` paths. They enforce the corresponding Lambda IAM action on the function ARN. Only `apigateway.amazonaws.com`, `lambda:InvokeFunction` and a SourceArn fixed to one local regional API ID/account are accepted. Stage/method/path wildcards are bounded to that API. No cross-account/public grant, SourceAccount/organization option, qualifier, Function URL or other service principal is supported. Optional resource-policy revisions protect add/remove from stale updates. Deleting a function cascades its resource policy; recreating the name cannot inherit old grants.

A successful function returns a REST proxy object with statusCode200–599, a UTF-8 string body and optional headers/multiValueHeaders. Binary media/base64 and unrecognized response fields are rejected. Invalid output, missing/revoked permission, unavailable target, invalid trust, guest error or deadline returns a sanitized502. Application status/body are preserved, but responses are forced to `application/octet-stream` attachments with restrictive CSP/nosniff/no-store. Only Content-Language, ETag and X-App-* response headers are retained; multi-value headers take precedence. Set-Cookie, Location, CORS and authority headers cannot change the operator's console origin. This deliberate download behavior differs from AWS's active browser application responses. It is necessary until separate-origin application hosting is implemented and tested.

## Limits and current omissions

One environment has at most100 APIs,100 resources per API,100 deployment snapshots per API and10 stages per API. Management bodies are at most128KiB. REST body/encoded event/response limits are256KiB; invalid UTF-8 and malformed query encodings are rejected. Resource policies have at most20 statements and20KiB. Stage variables have at most20 entries and4096 aggregate bytes, with the documented AWS variable-name/value character subset.

[CloudFormation provisioning](cloudformation-rest.md) supports the five bounded REST resource types and Lambda::Permission using the same native state. No WAF, custom resource callback, authorizer, HTTP API/v2, API key/usage plan, cache, VPC link, domain, binary media, mapping template, stage editing, method deletion is included. The existing WP workload and remaining unsupported Challenge problems remain disabled. API support alone is not original-problem scoring completion.

## Console and evidence

Official Cloudscape components implement APIs, Regional REST creation, Resources/Methods, Lambda proxy selection, deployment and stage invocation. Creating a console method adds an API/method-scoped Lambda service permission; function service access is controlled separately by its execution role. Multi-request creation can partially succeed: failures remain visible and the UI tells the operator to refresh before retrying. Console editing of an existing integration is not implemented yet; use native PutIntegration/AddPermission to complete a partial method, or delete the API and retry. API deletion leaves Lambda functions and their separately managed resource policies unchanged.

The actual authenticated AWS console was not available. Public primary guides determine names and flow; the resource table and stage lookup/test panel are still local adaptations, not a claim of pixel-equivalent AWS tree navigation or full console parity. PC/mobile browser tests exercise create→integration→scoped permission→deploy→actual answer42→delete, SDK readback, strict reflow, zero outside requests and zero page errors. API/SDK tests additionally execute saved-role SSM reads, immutable deployment after configuration edit/deletion, permission revocation, protected HTML/header handling and two actual isolated processes with identical names. Cross-team token401 and API404 are required.

Kumo MIT selected routing/event wire shapes are adapted with exact source hashes and original copyright/permission in [kumo-apigateway.json](../third_party/kumo-apigateway.json) and [KUMO-LICENSE.txt](../third_party/KUMO-LICENSE.txt). No upstream HTTP dispatcher or runtime dependency is imported.

Primary references: [REST console flow](https://docs.aws.amazon.com/apigateway/latest/developerguide/getting-started-rest-new-console.html), [proxy events/responses](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html), [GetRestApis wire contract](https://docs.aws.amazon.com/apigateway/latest/api/API_GetRestApis.html), [CreateRestApi](https://docs.aws.amazon.com/apigateway/latest/api/API_CreateRestApi.html), [PutIntegration](https://docs.aws.amazon.com/apigateway/latest/api/API_PutIntegration.html), [CreateDeployment](https://docs.aws.amazon.com/apigateway/latest/api/API_CreateDeployment.html), [Lambda AddPermission](https://docs.aws.amazon.com/lambda/latest/api/API_AddPermission.html).
