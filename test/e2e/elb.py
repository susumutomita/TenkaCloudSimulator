"""Elastic Load Balancing v2 native SDK acceptance against authenticated CLI processes.

Application load balancer -> HTTP listener -> Lambda target group -> saved-role
Python Lambda, with periodic health checks, participant IAM, two separate team
processes, restart persistence and cleanup. No AWS endpoint, EC2/VPC or network
placement is involved; subnets are recorded syntactic references.
"""
import io
import json
import sys
import tempfile
import time
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from check import running, boto3, Config, ClientError

SUBNETS = ["subnet-0a1b2c3d4e5f60718", "subnet-1a2b3c4d"]


def client(service, endpoint, token):
    c = boto3.client(service, endpoint_url=endpoint, region_name="us-east-1",
        aws_access_key_id="TCSIMLOCAL", aws_secret_access_key="dummy",
        config=Config(retries={"max_attempts": 0}))
    c.meta.events.register("before-sign." + c.meta.service_model.service_name,
        lambda request, **kwargs: request.headers.add_header("X-Simulator-Token", token))
    def local_only(request, **kwargs):
        assert request.url.startswith(endpoint + "/"), "SDK escaped the local endpoint"
    c.meta.events.register("before-send." + c.meta.service_model.service_name, local_only)
    return c


def error(code, call):
    try:
        call()
    except ClientError as exc:
        assert exc.response["Error"]["Code"] == code, exc.response["Error"]
    else:
        raise AssertionError("Expected " + code)


def http(endpoint, token, path, method="GET", body=None):
    request = urllib.request.Request(endpoint + path, data=body, method=method,
        headers={"X-Simulator-Token": token, "Cookie": "console=must-not-reach-guest"})
    try:
        with urllib.request.urlopen(request, timeout=40) as response:
            return response.status, dict(response.headers), response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, dict(exc.headers), exc.read()


def control(endpoint, token, path, body):
    request = urllib.request.Request(endpoint + path, data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "X-Simulator-Token": token},
        method="POST" if path.endswith("/access") else "PUT")
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def app(health_status):
    code = io.BytesIO()
    with zipfile.ZipFile(code, "w") as archive:
        archive.writestr("app.py", f"""import json, boto3
def handler(event, context):
    if event['path'] == '/health':
        return {{'statusCode': {health_status}, 'isBase64Encoded': False, 'headers': {{}}}}
    if event['path'] == '/fail':
        raise RuntimeError('guest failure')
    team = boto3.client('ssm').get_parameter(Name='/elb/team')['Parameter']['Value']
    return {{'statusCode': 200, 'isBase64Encoded': False, 'headers': {{'X-App-Team': team}},
             'body': json.dumps({{'path': event['path'], 'query': event['queryStringParameters'],
                                 'headers': sorted(event['headers']), 'group': event['requestContext']['elb']['targetGroupArn']}})}}
""")
    return code.getvalue()


def wait_health(elb, group, state, reason=None, seconds=40):
    deadline = time.monotonic() + seconds
    while True:
        health = elb.describe_target_health(TargetGroupArn=group)["TargetHealthDescriptions"][0]["TargetHealth"]
        if health["State"] == state and health.get("Reason") == reason:
            return health
        if time.monotonic() > deadline:
            raise AssertionError(f"health stayed {health}, expected {state}/{reason}")
        time.sleep(0.5)


with tempfile.TemporaryDirectory(prefix="elb-sdk-") as directory:
    first_db = Path(directory) / "first.db"
    with running(db=first_db, authenticated=True) as (first, token), \
         running(db=Path(directory) / "second.db", authenticated=True) as (second, token2):
        elb, lambdas, iam, ssm = (client(name, first, token) for name in ("elbv2", "lambda", "iam", "ssm"))
        other = client("elbv2", second, token2)
        role = iam.create_role(RoleName="alb-target-role", AssumeRolePolicyDocument=json.dumps({"Version": "2012-10-17",
            "Statement": [{"Effect": "Allow", "Action": "sts:AssumeRole", "Principal": {"Service": "lambda.amazonaws.com"}}]}))["Role"]
        iam.put_role_policy(RoleName="alb-target-role", PolicyName="ReadTeam", PolicyDocument=json.dumps({"Version": "2012-10-17",
            "Statement": [{"Effect": "Allow", "Action": "ssm:GetParameter", "Resource": "arn:aws:ssm:us-east-1:000000000000:parameter/elb/team"}]}))
        ssm.put_parameter(Name="/elb/team", Value="first team", Type="String")
        lambdas.create_function(FunctionName="alb-app", Runtime="python3.12", Role=role["Arn"], Handler="app.handler", Timeout=10, Code={"ZipFile": app(200)})

        lb = elb.create_load_balancer(Name="team-web", Subnets=SUBNETS)["LoadBalancers"][0]
        assert lb["State"]["Code"] == "active" and lb["Type"] == "application" and lb["DNSName"].endswith(".elb.simulator.invalid")
        assert elb.create_load_balancer(Name="team-web", Subnets=list(reversed(SUBNETS)))["LoadBalancers"][0]["LoadBalancerArn"] == lb["LoadBalancerArn"]
        error("DuplicateLoadBalancerName", lambda: elb.create_load_balancer(Name="team-web", Subnets=SUBNETS, Scheme="internal"))
        error("InvalidConfigurationRequest", lambda: elb.create_load_balancer(Name="network", Subnets=SUBNETS, Type="network"))
        error("InvalidConfigurationRequest", lambda: elb.create_target_group(Name="instances", Protocol="HTTP", Port=80, VpcId="vpc-12345678"))
        group = elb.create_target_group(Name="team-fn", TargetType="lambda", HealthCheckEnabled=True, HealthCheckPath="/health",
            HealthCheckIntervalSeconds=5, HealthCheckTimeoutSeconds=2, HealthyThresholdCount=2, UnhealthyThresholdCount=2)["TargetGroups"][0]
        tg = group["TargetGroupArn"]
        assert group["TargetType"] == "lambda" and group["Matcher"]["HttpCode"] == "200"
        error("AccessDenied", lambda: elb.register_targets(TargetGroupArn=tg, Targets=[{"Id": lambdas.get_function_configuration(FunctionName="alb-app")["FunctionArn"]}]))
        lambdas.add_permission(FunctionName="alb-app", StatementId="alb", Action="lambda:InvokeFunction", Principal="elasticloadbalancing.amazonaws.com", SourceArn=tg)
        function_arn = lambdas.get_function_configuration(FunctionName="alb-app")["FunctionArn"]
        elb.register_targets(TargetGroupArn=tg, Targets=[{"Id": function_arn}])
        assert elb.describe_target_health(TargetGroupArn=tg)["TargetHealthDescriptions"][0]["TargetHealth"]["Reason"] == "Target.NotInUse"
        listener = elb.create_listener(LoadBalancerArn=lb["LoadBalancerArn"], Protocol="HTTP", Port=80,
            DefaultActions=[{"Type": "forward", "TargetGroupArn": tg}])["Listeners"][0]
        elb.create_listener(LoadBalancerArn=lb["LoadBalancerArn"], Protocol="HTTP", Port=8080,
            DefaultActions=[{"Type": "fixed-response", "FixedResponseConfig": {"StatusCode": "503", "ContentType": "text/plain", "MessageBody": "maintenance"}}])
        error("UnsupportedProtocol", lambda: elb.create_listener(LoadBalancerArn=lb["LoadBalancerArn"], Protocol="HTTPS", Port=443,
            DefaultActions=[{"Type": "forward", "TargetGroupArn": tg}], Certificates=[{"CertificateArn": "arn:aws:acm:us-east-1:000000000000:certificate/x"}]))
        error("ResourceInUse", lambda: elb.delete_target_group(TargetGroupArn=tg))
        pages = elb.get_paginator("describe_listeners").paginate(LoadBalancerArn=lb["LoadBalancerArn"], PaginationConfig={"PageSize": 1})
        assert [item["Port"] for page in pages for item in page["Listeners"]] == [80, 8080]
        assert elb.describe_target_groups(LoadBalancerArn=lb["LoadBalancerArn"])["TargetGroups"][0]["LoadBalancerArns"] == [lb["LoadBalancerArn"]]

        # Data plane: the saved execution role reads SSM inside the guest.
        status, headers, body = http(first, token, "/_elb/us-east-1/team-web/80/orders?id=7&id=8")
        event = json.loads(body)
        assert status == 200 and headers["X-App-Team"] == "first team" and headers["Content-Type"] == "application/octet-stream", (status, headers, body)
        assert event["path"] == "/orders" and event["query"] == {"id": "8"} and event["group"] == tg
        assert "cookie" not in event["headers"] and "x-simulator-token" not in event["headers"] and "x-forwarded-for" in event["headers"]
        assert http(first, token, "/_elb/us-east-1/team-web/80/fail")[0] == 502
        assert http(first, token, "/_elb/us-east-1/team-web/8080/")[:3:2] == (503, b"maintenance")
        assert http(first, token, "/_elb/us-east-1/team-web/81/")[0] == 404
        assert http(second, token2, "/_elb/us-east-1/team-web/80/")[0] == 404

        # Real-time periodic health checks: healthy, then failing codes.
        wait_health(elb, tg, "healthy")
        lambdas.update_function_code(FunctionName="alb-app", ZipFile=app(500))
        unhealthy = wait_health(elb, tg, "unhealthy", "Target.ResponseCodeMismatch")
        assert unhealthy["Description"] == "Health checks failed with these codes: [500]", unhealthy
        assert http(first, token, "/_elb/us-east-1/team-web/80/still-routed")[0] == 200
        elb.modify_target_group(TargetGroupArn=tg, HealthCheckEnabled=False)
        assert elb.describe_target_health(TargetGroupArn=tg)["TargetHealthDescriptions"][0]["TargetHealth"]["State"] == "unavailable"

        # Participant identity: explicit describe only, mutation denied.
        policy = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": ["elasticloadbalancing:DescribeLoadBalancers", "elasticloadbalancing:DescribeTargetHealth"], "Resource": "*"}]}
        control(first, token, "/_simulator/v1/iam/principals/alb-reader/policy", policy)
        participant = control(first, token, "/_simulator/v1/access", {"Principal": "alb-reader"})["AccessToken"]
        limited = client("elbv2", first, participant)
        assert limited.describe_load_balancers()["LoadBalancers"][0]["LoadBalancerName"] == "team-web"
        error("AccessDenied", lambda: limited.delete_load_balancer(LoadBalancerArn=lb["LoadBalancerArn"]))
        error("AccessDenied", lambda: limited.describe_listeners(LoadBalancerArn=lb["LoadBalancerArn"]))
        error("AccessDenied", lambda: limited.deregister_targets(TargetGroupArn=tg, Targets=[{"Id": function_arn}]))
        assert other.describe_load_balancers()["LoadBalancers"] == [] and other.describe_target_groups()["TargetGroups"] == []
        try:
            urllib.request.urlopen(urllib.request.Request(second + "/_simulator/health", headers={"X-Simulator-Token": token}), timeout=5)
        except urllib.error.HTTPError as exc:
            assert exc.code == 401
        else:
            raise AssertionError("Token crossed team authorities")
        caps = json.load(urllib.request.urlopen(urllib.request.Request(first + "/_simulator/v1/capabilities", headers={"X-Simulator-Token": token}), timeout=5))
        assert len(caps["services"]["elasticloadbalancing"]["operations"]) == 13
        for c in (elb, lambdas, iam, ssm, other, limited):
            c.close()

    with running(db=first_db, authenticated=True) as (restarted, new_token):
        elb, lambdas = client("elbv2", restarted, new_token), client("lambda", restarted, new_token)
        assert elb.describe_listeners(ListenerArns=[listener["ListenerArn"]])["Listeners"][0]["Port"] == 80
        assert http(restarted, new_token, "/_elb/us-east-1/team-web/80/after-restart")[0] == 200
        elb.delete_listener(ListenerArn=listener["ListenerArn"])
        error("ListenerNotFound", lambda: elb.delete_listener(ListenerArn=listener["ListenerArn"]))
        elb.deregister_targets(TargetGroupArn=tg, Targets=[{"Id": function_arn}])
        elb.delete_target_group(TargetGroupArn=tg)
        elb.delete_load_balancer(LoadBalancerArn=lb["LoadBalancerArn"])
        assert elb.describe_load_balancers()["LoadBalancers"] == [] and elb.describe_target_groups()["TargetGroups"] == []
        assert http(restarted, new_token, "/_elb/us-east-1/team-web/80/")[0] == 404
        lambdas.delete_function(FunctionName="alb-app")
        elb.close()
        lambdas.close()

print(json.dumps({"elbNativeSDK": True, "lambdaTargetDataPlane": True, "periodicHealthChecks": True,
    "twoAuthenticatedProcesses": True, "actualCLIReopen": True, "participantIAM": True,
    "teardown": "DELETED", "ec2Networking": False, "realAWS": False}))
