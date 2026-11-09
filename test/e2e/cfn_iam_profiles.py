"""Bounded CFN/native IAM role-profile composition against two isolated CLIs.

This is not the original Battle: no managed SSM instance policy, EC2, guest,
Session Manager, browser or real AWS is used. Inputs contain dummy values only.
Shared E2E registration belongs to the integration owner.
"""
import copy
import hashlib
import json
import tempfile
from contextlib import ExitStack
from pathlib import Path

from check import REPORTS, running
from iam_managed import client, error, forbidden_host_token, participant
from iam_instance_profiles import TRUST

ROLE, PROFILE, CALLER = "CFNNativeRole", "CFNNativeProfile", "CFNOperator"
TEMPLATE = {"Resources": {
    "Role": {"Type": "AWS::IAM::Role", "Properties": {"RoleName": ROLE, "AssumeRolePolicyDocument": TRUST}},
    "Profile": {"Type": "AWS::IAM::InstanceProfile", "Properties": {"InstanceProfileName": PROFILE, "Roles": [{"Ref": "Role"}]}}
}, "Outputs": {"ProfileArn": {"Value": {"Fn::GetAtt": ["Profile", "Arn"]}}}}
PHASE = "startup"


def run():
    global PHASE
    checks, saved = [], []
    with tempfile.TemporaryDirectory(prefix="cfn-iam-profile-owned-") as directory:
        databases = [str(Path(directory) / f"team-{i}.db") for i in range(2)]
        with ExitStack() as stack:
            runtimes = [stack.enter_context(running(db=path, authenticated=True)) for path in databases]
            for endpoint, operator in runtimes:
                iam, cfn = client("iam", endpoint, operator), client("cloudformation", endpoint, operator)
                PHASE = "canonical-PassRole-denial-and-atomic-rollback"
                iam.create_role(RoleName=CALLER, AssumeRolePolicyDocument=json.dumps(TRUST))
                grant = {"Version": "2012-10-17", "Statement": [
                    {"Effect": "Allow", "Action": ["cloudformation:*", "iam:CreateRole", "iam:CreateInstanceProfile", "iam:AddRoleToInstanceProfile"], "Resource": "*"}]}
                iam.put_role_policy(RoleName=CALLER, PolicyName="Create", PolicyDocument=json.dumps(grant))
                token = participant(endpoint, operator, CALLER)
                limited = client("cloudformation", endpoint, token)
                create = dict(StackName="native-profile", TemplateBody=json.dumps(TEMPLATE), Capabilities=["CAPABILITY_NAMED_IAM"])
                error("AccessDenied", lambda: limited.create_stack(**create))
                error("NoSuchEntity", lambda: iam.get_role(RoleName=ROLE))
                error("NoSuchEntity", lambda: iam.get_instance_profile(InstanceProfileName=PROFILE))
                grant["Statement"].append({"Effect": "Allow", "Action": "iam:PassRole", "Resource": "arn:aws:iam::000000000000:role/" + ROLE})
                iam.put_role_policy(RoleName=CALLER, PolicyName="Create", PolicyDocument=json.dumps(grant))
                PHASE = "CFN-create-native-role-profile-and-outputs"
                stack_id = limited.create_stack(**create)["StackId"]
                role = iam.get_role(RoleName=ROLE)["Role"]
                profile = iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"]
                assert role["AssumeRolePolicyDocument"] == TRUST and profile["Roles"] == [role]
                assert iam.list_role_policies(RoleName=ROLE)["PolicyNames"] == []
                assert iam.list_attached_role_policies(RoleName=ROLE)["AttachedPolicies"] == []
                outputs = cfn.describe_stacks(StackName=stack_id)["Stacks"][0]["Outputs"]
                assert {o["OutputKey"]: o["OutputValue"] for o in outputs} == {"ProfileArn": profile["Arn"]}
                events = cfn.describe_stack_events(StackName=stack_id)["StackEvents"]
                assert events[0]["ResourceStatus"] == "CREATE_COMPLETE"
                PHASE = "unchanged-supported-Hello-World-legacy-compatibility"
                original = (Path(__file__).resolve().parents[2] / "test/fixtures/hello-world.yaml").read_bytes()
                assert hashlib.sha256(original).hexdigest() == "ad8b4fcf3f1d42475c5c2e8617d3194536810cc044a6ea166a2699cac719c8bb"
                parameters = {"NamePrefix": "tc-hello-world-compat", "TenkaCloudAccountId": "000000000000", "ExternalId": "local-job-20261109", "FlagSeed": "DummySeed123"}
                legacy_id = cfn.create_stack(StackName="legacy-original", TemplateBody=original.decode(), Capabilities=["CAPABILITY_IAM"], Parameters=[{"ParameterKey": key, "ParameterValue": value} for key, value in parameters.items()])["StackId"]
                assert {r["RoleName"] for r in iam.list_roles()["Roles"]} == {ROLE, CALLER}
                cfn.delete_stack(StackName=legacy_id)
                PHASE = "unsupported-distinct-managed-policy-fails-closed"
                unsupported = copy.deepcopy(TEMPLATE)
                unsupported["Resources"]["Role"]["Properties"]["RoleName"] = "UnsupportedManagedRole"
                unsupported["Resources"]["Profile"]["Properties"]["InstanceProfileName"] = "UnsupportedManagedProfile"
                unsupported["Resources"]["Role"]["Properties"]["ManagedPolicyArns"] = ["arn:aws:iam::aws:policy/AdministratorAccess"]
                error("ValidationError", lambda: cfn.create_stack(StackName="unsupported-instance", TemplateBody=json.dumps(unsupported), Capabilities=["CAPABILITY_NAMED_IAM"]))
                error("NoSuchEntity", lambda: iam.get_role(RoleName="UnsupportedManagedRole"))
                error("NoSuchEntity", lambda: iam.get_instance_profile(InstanceProfileName="UnsupportedManagedProfile"))
                assert iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"] == profile
                saved.append({"id": stack_id, "role": role, "profile": profile, "operator": operator, "token": token})
            PHASE = "two-team-isolation"
            assert saved[0]["role"]["RoleId"] != saved[1]["role"]["RoleId"]
            assert saved[0]["profile"]["InstanceProfileId"] != saved[1]["profile"]["InstanceProfileId"]
            forbidden_host_token(runtimes[1][0], saved[0]["operator"])
            forbidden_host_token(runtimes[0][0], saved[1]["token"])
        checks.extend(["CFN-native-shared-state", "canonical-PassRole-denial", "atomic-denial", "no-implicit-managed-grants", "stack-output-and-events", "original-supported-Hello-World-legacy-regression", "cross-team-token-isolation"])
        with ExitStack() as stack:
            runtimes = [stack.enter_context(running(db=path, authenticated=True)) for path in databases]
            for index, (endpoint, operator) in enumerate(runtimes):
                iam, cfn = client("iam", endpoint, operator), client("cloudformation", endpoint, operator)
                PHASE = "restart-persistence-and-external-dependency"
                assert iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"] == saved[index]["profile"]
                forbidden_host_token(endpoint, saved[index]["token"])
                iam.create_instance_profile(InstanceProfileName="ExternalProfile")
                iam.add_role_to_instance_profile(InstanceProfileName="ExternalProfile", RoleName=ROLE)
                error("DeleteConflict", lambda: cfn.delete_stack(StackName=saved[index]["id"]))
                assert iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"] == saved[index]["profile"]
                iam.remove_role_from_instance_profile(InstanceProfileName="ExternalProfile", RoleName=ROLE)
                iam.delete_instance_profile(InstanceProfileName="ExternalProfile")
                PHASE = "reverse-cleanup-and-fresh-generations"
                cfn.delete_stack(StackName=saved[index]["id"])
                error("NoSuchEntity", lambda: iam.get_role(RoleName=ROLE))
                error("NoSuchEntity", lambda: iam.get_instance_profile(InstanceProfileName=PROFILE))
                new = cfn.create_stack(StackName="native-profile", TemplateBody=json.dumps(TEMPLATE), Capabilities=["CAPABILITY_NAMED_IAM"])["StackId"]
                assert iam.get_role(RoleName=ROLE)["Role"]["RoleId"] != saved[index]["role"]["RoleId"]
                assert iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"]["InstanceProfileId"] != saved[index]["profile"]["InstanceProfileId"]
                error("ValidationError", lambda: cfn.delete_stack(StackName=saved[index]["id"]))
                cfn.delete_stack(StackName=new)
                iam.delete_role_policy(RoleName=CALLER, PolicyName="Create")
                iam.delete_role(RoleName=CALLER)
                assert iam.list_instance_profiles()["InstanceProfiles"] == []
                assert iam.list_roles()["Roles"] == []
        checks.extend(["two-CLI-restart", "external-dependency-rollback", "reverse-cleanup", "fresh-native-generations", "stale-stack-id-refusal", "complete-owned-cleanup"])
    return {"status": "passed", "checks": checks, "originalBattle": False, "guestExecution": False, "sessionManager": False, "actualAWSComparison": False}


if __name__ == "__main__":
    try:
        result = run()
    except Exception:
        print("CFN IAM profile acceptance failed during " + PHASE)
        raise
    (REPORTS / "cfn-iam-profiles-sdk.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))
