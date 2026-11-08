"""Actual native IAM profile SDK lifecycle across two isolated local CLIs.

No EC2 guest, SSM session, browser, AWS connection or runtime-credential claim.
Uses only fresh dummy local credentials and private temporary SQLite files.
"""
import json
import tempfile
from contextlib import ExitStack
from pathlib import Path

from check import REPORTS, running
from iam_managed import client, error, forbidden_host_token, participant

TRUST = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "sts:AssumeRole", "Principal": {"Service": "ec2.amazonaws.com"}}]}
PROFILE = "BattleInstanceProfile"
ROLE = "BattleInstanceRole"
CALLER = "ProfileOperator"
PHASE = "startup"


def policy(actions, resource):
    return {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": actions, "Resource": resource}]}


def run():
    global PHASE
    checks = []
    with tempfile.TemporaryDirectory(prefix="iam-instance-profile-owned-") as directory:
        databases = [str(Path(directory) / f"team-{i}.db") for i in range(2)]
        saved = []
        with ExitStack() as stack:
            runtimes = [stack.enter_context(running(db=database, authenticated=True)) for database in databases]
            for endpoint, operator in runtimes:
                iam = client("iam", endpoint, operator)
                PHASE = "native-profile-role-create"
                role = iam.create_role(RoleName=ROLE, AssumeRolePolicyDocument=json.dumps(TRUST))["Role"]
                iam.create_role(RoleName=CALLER, AssumeRolePolicyDocument=json.dumps(TRUST))
                profile = iam.create_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"]
                assert profile["Path"] == "/" and profile["Roles"] == []
                assert profile["InstanceProfileId"].startswith("AIPA")
                assert profile["Arn"] == "arn:aws:iam::000000000000:instance-profile/" + PROFILE
                assert iam.get_instance_profile(InstanceProfileName=PROFILE.lower())["InstanceProfile"] == profile
                error("EntityAlreadyExists", lambda: iam.create_instance_profile(InstanceProfileName=PROFILE.upper()))
                PHASE = "bounded-fields-and-pagination"
                error("NotImplemented", lambda: iam.create_instance_profile(InstanceProfileName="Nested", Path="/nested/"))
                error("NotImplemented", lambda: iam.create_instance_profile(InstanceProfileName="Tagged", Tags=[{"Key": "x", "Value": "y"}]))
                extra = iam.create_instance_profile(InstanceProfileName="ZebraProfile")["InstanceProfile"]
                first = iam.list_instance_profiles(MaxItems=1)
                assert first["IsTruncated"] and first["InstanceProfiles"] == [profile]
                second = iam.list_instance_profiles(Marker=first["Marker"], MaxItems=1)
                assert not second["IsTruncated"] and second["InstanceProfiles"] == [extra]
                PHASE = "caller-action-and-PassRole-denial"
                token = participant(endpoint, operator, CALLER)
                limited = client("iam", endpoint, token)
                error("AccessDenied", lambda: limited.add_role_to_instance_profile(InstanceProfileName=PROFILE, RoleName=ROLE))
                iam.put_role_policy(RoleName=CALLER, PolicyName="ProfileMutation", PolicyDocument=json.dumps(policy("iam:AddRoleToInstanceProfile", profile["Arn"])))
                error("AccessDenied", lambda: limited.add_role_to_instance_profile(InstanceProfileName=PROFILE.lower(), RoleName=ROLE.lower()))
                assert iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"]["Roles"] == []
                iam.put_role_policy(RoleName=CALLER, PolicyName="PassSpecificRole", PolicyDocument=json.dumps(policy("iam:PassRole", role["Arn"])))
                limited.add_role_to_instance_profile(InstanceProfileName=PROFILE.lower(), RoleName=ROLE.lower())
                attached = iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"]
                assert attached["Roles"] == [role]
                assert iam.list_instance_profiles_for_role(RoleName=ROLE.lower())["InstanceProfiles"] == [attached]
                iam.put_role_policy(RoleName=CALLER, PolicyName="ExplicitPassRoleDeny", PolicyDocument=json.dumps({"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Action": "iam:PassRole", "Resource": role["Arn"]}]}))
                error("AccessDenied", lambda: limited.add_role_to_instance_profile(InstanceProfileName=PROFILE, RoleName=ROLE))
                assert iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"] == attached
                PHASE = "dependency-and-one-role-refusal"
                other = iam.create_role(RoleName="OtherInstanceRole", AssumeRolePolicyDocument=json.dumps(TRUST))["Role"]
                error("LimitExceeded", lambda: iam.add_role_to_instance_profile(InstanceProfileName=PROFILE, RoleName=other["RoleName"]))
                error("DeleteConflict", lambda: iam.delete_instance_profile(InstanceProfileName=PROFILE))
                error("DeleteConflict", lambda: iam.delete_role(RoleName=ROLE))
                assert iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"] == attached
                iam.add_role_to_instance_profile(InstanceProfileName="ZebraProfile", RoleName=ROLE)
                pages = iam.list_instance_profiles_for_role(RoleName=ROLE, MaxItems=1)
                assert pages["IsTruncated"] and pages["InstanceProfiles"][0]["InstanceProfileName"] == PROFILE
                assert iam.list_instance_profiles_for_role(RoleName=ROLE, Marker=pages["Marker"])["InstanceProfiles"][0]["InstanceProfileName"] == "ZebraProfile"
                iam.remove_role_from_instance_profile(InstanceProfileName="ZebraProfile", RoleName=ROLE)
                iam.delete_instance_profile(InstanceProfileName="ZebraProfile")
                iam.delete_role(RoleName=other["RoleName"])
                saved.append({"profile": attached, "role": role, "operator": operator, "participant": token})
            PHASE = "cross-team-token-isolation"
            assert saved[0]["profile"]["Arn"] == saved[1]["profile"]["Arn"]
            assert saved[0]["profile"]["InstanceProfileId"] != saved[1]["profile"]["InstanceProfileId"]
            forbidden_host_token(runtimes[1][0], saved[0]["operator"])
            forbidden_host_token(runtimes[0][0], saved[1]["participant"])
        checks.extend(["native-seven-operations", "case-canonical-ARNs", "profile-and-role-pagination", "explicit-unsupported-fields", "action-and-PassRole-denial", "dependency-guards", "one-role-limit", "cross-team-authority"])
        with ExitStack() as stack:
            runtimes = [stack.enter_context(running(db=database, authenticated=True)) for database in databases]
            for index, (endpoint, operator) in enumerate(runtimes):
                PHASE = "restart-persistence"
                iam = client("iam", endpoint, operator)
                assert iam.get_instance_profile(InstanceProfileName=PROFILE)["InstanceProfile"] == saved[index]["profile"]
                forbidden_host_token(endpoint, saved[index]["participant"])
                PHASE = "remove-delete-recreate-cleanup"
                iam.remove_role_from_instance_profile(InstanceProfileName=PROFILE, RoleName=ROLE)
                assert iam.list_instance_profiles_for_role(RoleName=ROLE)["InstanceProfiles"] == []
                iam.delete_instance_profile(InstanceProfileName=PROFILE)
                error("NoSuchEntity", lambda: iam.get_instance_profile(InstanceProfileName=PROFILE))
                fresh = iam.create_instance_profile(InstanceProfileName=PROFILE.upper())["InstanceProfile"]
                assert fresh["InstanceProfileId"] != saved[index]["profile"]["InstanceProfileId"] and fresh["Roles"] == []
                iam.delete_instance_profile(InstanceProfileName=PROFILE)
                iam.delete_role(RoleName=ROLE)
                for name in ["ProfileMutation", "PassSpecificRole", "ExplicitPassRoleDeny"]:
                    iam.delete_role_policy(RoleName=CALLER, PolicyName=name)
                iam.delete_role(RoleName=CALLER)
                assert iam.list_instance_profiles()["InstanceProfiles"] == []
                assert iam.list_roles()["Roles"] == []
        checks.extend(["two-process-restart-persistence", "stale-token-refusal", "fresh-profile-incarnation", "full-cleanup"])
    return {"status": "passed", "checks": checks, "actualAWSComparison": False, "guestExecution": False, "sessionManager": False}


if __name__ == "__main__":
    try:
        result = run()
    except Exception:
        print("IAM instance-profile acceptance failed during " + PHASE)
        raise
    (REPORTS / "iam-instance-profiles-sdk.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))
