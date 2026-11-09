"""Exact pinned SSM IAM policy and original IAM fragment against two local CLIs.

Not full Battle acceptance: no VM, agent, SSM session/command or real AWS.
Registration and execution are coordinated by the integration owner.
"""
import hashlib
import json
import tempfile
from contextlib import ExitStack
from pathlib import Path

from check import REPORTS, running
from iam_managed import client, error, forbidden_host_token, participant
from iam_instance_profiles import TRUST

ARN = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
NAME = "AmazonSSMManagedInstanceCore"
READER, CALLER = "BuiltinReader", "BuiltinOperator"
PARAMETER = "/practice/builtin"
PHASE = "startup"


def policy(action="*", effect="Allow"):
    return {"Version": "2012-10-17", "Statement": [{"Effect": effect, "Action": action, "Resource": "*"}]}


def fragment():
    path = Path(__file__).resolve().parents[2] / "internal/aws/testdata/hello-world-battle-iam-fragment.yaml"
    body = path.read_text()
    resources = body.split("Resources:\n", 1)[1].split("# End of the unchanged IAM resource fragment.", 1)[0]
    assert hashlib.sha256(resources.encode()).hexdigest() == "bd39fb7c3ec71a1167662ee1ab98e56713304ef4c47dccd8142019fa272723c9"
    # Outputs are an acceptance wrapper, not changes to the original resources.
    return body + "Outputs:\n  Role: {Value: !Ref InstanceRole}\n  Profile: {Value: !Ref InstanceProfile}\n  ProfileArn: {Value: !GetAtt InstanceProfile.Arn}\n"


def verify_pin(iam):
    meta = iam.get_policy(PolicyArn=ARN)["Policy"]
    assert meta["PolicyName"] == NAME and meta["DefaultVersionId"] == "v2"
    version = iam.get_policy_version(PolicyArn=ARN, VersionId="v2")["PolicyVersion"]
    assert version["VersionId"] == "v2" and version["IsDefaultVersion"]
    document = version["Document"]
    encoded = json.dumps(document, separators=(",", ":"))
    assert hashlib.sha256(encoded.encode()).hexdigest() == "b49d8e0a366be4eb7ea97da2ef96ad84a9a563ef6efa926c40c581c1e074b993"
    assert [v["VersionId"] for v in iam.list_policy_versions(PolicyArn=ARN)["Versions"]] == ["v2"]
    error("NoSuchEntity", lambda: iam.get_policy_version(PolicyArn=ARN, VersionId="v1"))
    error("UnmodifiableEntity", lambda: iam.create_policy_version(PolicyArn=ARN, PolicyDocument=json.dumps(policy()), SetAsDefault=True))
    error("UnmodifiableEntity", lambda: iam.set_default_policy_version(PolicyArn=ARN, VersionId="v2"))
    error("UnmodifiableEntity", lambda: iam.delete_policy_version(PolicyArn=ARN, VersionId="v2"))
    error("UnmodifiableEntity", lambda: iam.delete_policy(PolicyArn=ARN))
    assert iam.list_policies(Scope="Local")["Policies"] == []
    error("NotImplemented", lambda: iam.list_policies(Scope="AWS"))


def verify_same_name_namespace(iam, reader, value):
    # Different permissions make accidental namespace adoption observable.
    document = policy("ssm:GetParametersByPath")
    customer = iam.create_policy(PolicyName=NAME, PolicyDocument=json.dumps(document))["Policy"]
    customer_arn = "arn:aws:iam::000000000000:policy/" + NAME
    assert customer["Arn"] == customer_arn and customer_arn != ARN
    iam.attach_role_policy(RoleName=READER, PolicyArn=customer_arn)
    seen, markers, request = [], set(), {"RoleName": READER, "MaxItems": 1}
    for page_number in range(2):
        page = iam.list_attached_role_policies(**request)
        assert len(page["AttachedPolicies"]) == 1, "Missing or oversized same-name page"
        item = page["AttachedPolicies"][0]
        assert item["PolicyName"] == NAME
        assert item["PolicyArn"] in {ARN, customer_arn} and item["PolicyArn"] not in seen, "Unexpected or duplicate policy ARN"
        seen.append(item["PolicyArn"])
        if page_number == 0:
            marker = page.get("Marker")
            assert page["IsTruncated"] and marker and marker not in markers, "Missing or repeated continuation marker"
            markers.add(marker)
            request["Marker"] = marker
        else:
            assert not page["IsTruncated"] and not page.get("Marker"), "Unexpected additional page"
    assert len(seen) == 2 and set(seen) == {ARN, customer_arn}, "Pagination skipped an exact policy ARN"
    local = iam.list_policies(Scope="Local", MaxItems=1)
    assert [p["Arn"] for p in local["Policies"]] == [customer_arn]
    assert not local["IsTruncated"] and not local.get("Marker")
    assert reader.get_parameter(Name=PARAMETER)["Parameter"]["Value"] == value
    assert reader.get_parameters_by_path(Path="/practice")["Parameters"][0]["Value"] == value
    iam.detach_role_policy(RoleName=READER, PolicyArn=ARN)
    assert iam.list_attached_role_policies(RoleName=READER)["AttachedPolicies"] == [{"PolicyName": NAME, "PolicyArn": customer_arn}]
    assert iam.get_policy_version(PolicyArn=customer_arn, VersionId="v1")["PolicyVersion"]["Document"] == document
    assert iam.get_policy(PolicyArn=customer_arn)["Policy"]["AttachmentCount"] == 1
    assert iam.get_policy(PolicyArn=ARN)["Policy"]["AttachmentCount"] == 0
    error("AccessDeniedException", lambda: reader.get_parameter(Name=PARAMETER))
    assert reader.get_parameters_by_path(Path="/practice")["Parameters"][0]["Value"] == value
    iam.attach_role_policy(RoleName=READER, PolicyArn=ARN)
    iam.detach_role_policy(RoleName=READER, PolicyArn=customer_arn)
    assert iam.list_attached_role_policies(RoleName=READER)["AttachedPolicies"] == [{"PolicyName": NAME, "PolicyArn": ARN}]
    assert iam.get_policy(PolicyArn=customer_arn)["Policy"]["AttachmentCount"] == 0
    assert reader.get_parameter(Name=PARAMETER)["Parameter"]["Value"] == value
    error("AccessDeniedException", lambda: reader.get_parameters_by_path(Path="/practice"))
    iam.delete_policy(PolicyArn=customer_arn)
    error("NoSuchEntity", lambda: iam.get_policy(PolicyArn=customer_arn))
    assert iam.get_policy(PolicyArn=ARN)["Policy"]["AttachmentCount"] == 1
    verify_pin(iam)
    assert reader.get_parameter(Name=PARAMETER)["Parameter"]["Value"] == value
    error("AccessDeniedException", lambda: reader.get_parameters_by_path(Path="/practice"))


def run():
    global PHASE
    checks, saved = [], []
    body = fragment()
    create = dict(StackName="ssm-fragment", TemplateBody=body, Capabilities=["CAPABILITY_NAMED_IAM"], Parameters=[{"ParameterKey": "NamePrefix", "ParameterValue": "tc-builtin-team"}])
    with tempfile.TemporaryDirectory(prefix="iam-ssm-builtin-owned-") as directory:
        databases = [str(Path(directory) / f"team-{i}.db") for i in range(2)]
        with ExitStack() as stack:
            runtimes = [stack.enter_context(running(db=db, authenticated=True)) for db in databases]
            for index, (endpoint, operator) in enumerate(runtimes):
                iam, ssm, cfn = [client(service, endpoint, operator) for service in ("iam", "ssm", "cloudformation")]
                PHASE = "pinned-readonly-native-document"
                verify_pin(iam)
                role = iam.create_role(RoleName=READER, AssumeRolePolicyDocument=json.dumps(TRUST))["Role"]
                token = participant(endpoint, operator, READER)
                reader = client("ssm", endpoint, token)
                value = f"dummy-team-{index}"
                ssm.put_parameter(Name=PARAMETER, Type="String", Value=value)
                PHASE = "native-permissions-deny-and-revocation"
                error("AccessDeniedException", lambda: reader.get_parameter(Name=PARAMETER))
                iam.attach_role_policy(RoleName=READER.lower(), PolicyArn=ARN)
                assert reader.get_parameter(Name=PARAMETER)["Parameter"]["Value"] == value
                assert reader.get_parameters(Names=[PARAMETER])["Parameters"][0]["Value"] == value
                error("AccessDeniedException", lambda: reader.put_parameter(Name=PARAMETER, Type="String", Value="denied", Overwrite=True))
                error("AccessDeniedException", lambda: reader.get_parameters_by_path(Path="/practice"))
                error("UnknownOperationException", lambda: reader.start_session(Target="i-0123456789abcdef0"))
                error("UnknownOperationException", lambda: reader.send_command(InstanceIds=["i-0123456789abcdef0"], DocumentName="Dummy"))
                iam.put_role_policy(RoleName=READER, PolicyName="Deny", PolicyDocument=json.dumps(policy("ssm:GetParameter", "Deny")))
                error("AccessDeniedException", lambda: reader.get_parameter(Name=PARAMETER))
                iam.delete_role_policy(RoleName=READER, PolicyName="Deny")
                iam.detach_role_policy(RoleName=READER, PolicyArn=ARN)
                error("AccessDeniedException", lambda: reader.get_parameter(Name=PARAMETER))
                iam.attach_role_policy(RoleName=READER, PolicyArn=ARN)
                PHASE = "same-display-name-distinct-policy-namespace"
                verify_same_name_namespace(iam, reader, value)
                PHASE = "fragment-attach-and-passrole-denial-atomicity"
                iam.create_role(RoleName=CALLER, AssumeRolePolicyDocument=json.dumps(TRUST))
                caller_token = participant(endpoint, operator, CALLER)
                limited = client("cloudformation", endpoint, caller_token)
                for denied in ("iam:AttachRolePolicy", "iam:PassRole"):
                    grant = policy()
                    grant["Statement"].extend(policy(denied, "Deny")["Statement"])
                    iam.put_role_policy(RoleName=CALLER, PolicyName="Control", PolicyDocument=json.dumps(grant))
                    error("AccessDenied", lambda: limited.create_stack(**create))
                    assert {r["RoleName"] for r in iam.list_roles()["Roles"]} == {READER, CALLER}
                    error("NoSuchEntity", lambda: iam.get_instance_profile(InstanceProfileName="tc-builtin-team-instance-profile"))
                iam.put_role_policy(RoleName=CALLER, PolicyName="Control", PolicyDocument=json.dumps(policy()))
                stack_id = limited.create_stack(**create)["StackId"]
                outputs = {o["OutputKey"]: o["OutputValue"] for o in cfn.describe_stacks(StackName=stack_id)["Stacks"][0]["Outputs"]}
                profile = iam.get_instance_profile(InstanceProfileName=outputs["Profile"])["InstanceProfile"]
                fragment_role = iam.get_role(RoleName=outputs["Role"])["Role"]
                assert profile["Arn"] == outputs["ProfileArn"] and profile["Roles"] == [fragment_role]
                assert iam.list_attached_role_policies(RoleName=outputs["Role"])["AttachedPolicies"] == [{"PolicyName": NAME, "PolicyArn": ARN}]
                assert iam.list_role_policies(RoleName=outputs["Role"])["PolicyNames"] == []
                PHASE = "fragment-detach-denial-preserves-stack"
                grant = policy()
                grant["Statement"].extend(policy("iam:DetachRolePolicy", "Deny")["Statement"])
                iam.put_role_policy(RoleName=CALLER, PolicyName="Control", PolicyDocument=json.dumps(grant))
                error("AccessDenied", lambda: limited.delete_stack(StackName=stack_id))
                assert iam.get_instance_profile(InstanceProfileName=outputs["Profile"])["InstanceProfile"] == profile
                assert len(iam.list_attached_role_policies(RoleName=outputs["Role"])["AttachedPolicies"]) == 1
                saved.append(dict(operator=operator, token=token, role=role, profile=profile, fragment_role=fragment_role, stack=stack_id, value=value))
            PHASE = "two-team-authority-and-data-isolation"
            assert saved[0]["role"]["RoleId"] != saved[1]["role"]["RoleId"]
            forbidden_host_token(runtimes[1][0], saved[0]["token"])
            forbidden_host_token(runtimes[0][0], saved[1]["operator"])
        checks.extend(["v2-readonly-document-roundtrip", "no-invented-v1", "no-general-AWS-catalog", "parameter-read-and-write-deny", "inline-deny", "detach-revocation", "same-name-exact-ARN-pagination", "customer-only-local-list", "independent-namespace-detach-delete", "absent-session-command-fail-closed", "original-resource-fragment", "attach-passrole-detach-denial", "two-team-isolation"])
        with ExitStack() as stack:
            runtimes = [stack.enter_context(running(db=db, authenticated=True)) for db in databases]
            for index, (endpoint, operator) in enumerate(runtimes):
                iam, ssm, cfn = [client(service, endpoint, operator) for service in ("iam", "ssm", "cloudformation")]
                PHASE = "restart-persistence-and-fresh-host-authority"
                verify_pin(iam)
                assert iam.get_role(RoleName=READER)["Role"] == saved[index]["role"]
                profile = saved[index]["profile"]
                assert iam.get_instance_profile(InstanceProfileName=profile["InstanceProfileName"])["InstanceProfile"] == profile
                forbidden_host_token(endpoint, saved[index]["token"])
                reader = client("ssm", endpoint, participant(endpoint, operator, READER))
                assert reader.get_parameter(Name=PARAMETER)["Parameter"]["Value"] == saved[index]["value"]
                PHASE = "native-and-fragment-cleanup"
                iam.detach_role_policy(RoleName=READER, PolicyArn=ARN)
                error("AccessDeniedException", lambda: reader.get_parameter(Name=PARAMETER))
                iam.delete_role(RoleName=READER)
                fresh = iam.create_role(RoleName=READER, AssumeRolePolicyDocument=json.dumps(TRUST))["Role"]
                assert fresh["RoleId"] != saved[index]["role"]["RoleId"]
                assert iam.list_attached_role_policies(RoleName=READER)["AttachedPolicies"] == []
                iam.delete_role(RoleName=READER)
                cfn.delete_stack(StackName=saved[index]["stack"])
                error("NoSuchEntity", lambda: iam.get_role(RoleName=saved[index]["fragment_role"]["RoleName"]))
                error("NoSuchEntity", lambda: iam.get_instance_profile(InstanceProfileName=profile["InstanceProfileName"]))
                assert iam.get_policy(PolicyArn=ARN)["Policy"]["AttachmentCount"] == 0
                iam.delete_role_policy(RoleName=CALLER, PolicyName="Control")
                iam.delete_role(RoleName=CALLER)
                ssm.delete_parameter(Name=PARAMETER)
                assert iam.list_roles()["Roles"] == [] and iam.list_instance_profiles()["InstanceProfiles"] == []
        checks.extend(["two-CLI-restart", "generation-recreation", "complete-owned-cleanup", "builtin-preserved-after-cleanup"])
    return {"status": "passed", "checks": checks, "originalBattle": False, "guestExecution": False, "sessionManager": False, "actualAWSComparison": False}


if __name__ == "__main__":
    try:
        result = run()
    except Exception:
        print("SSM builtin acceptance failed during " + PHASE)
        raise
    (REPORTS / "iam-ssm-builtin-sdk.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))
