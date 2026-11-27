package aws

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// This optional smoke invokes the real pinned EC2 SDK through numeric
// loopback into the configured Query gateway. The compiled simulator CLI
// and embedded console have their own separate acceptance contract.
func TestEC2NetworkACLAssociationSDKLoopback(t *testing.T) {
	path := os.Getenv("TCSIM_TEST_BOTO3_PYTHONPATH")
	if path == "" {
		t.Skip("explicit pinned local Boto3 path not set")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("expected an absolute pinned local package path")
	}
	host := filepath.Join(t.TempDir(), "network-acl-sdk")
	if err := os.Mkdir(host, 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(host, "state.sqlite")
	receipt := filepath.Join(host, "fixture.json")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"initial", "restart"} {
		store, stacks := openNetworkACLState(t, db)
		gateway := &Gateway{Stacks: stacks, Identities: core.NewIdentities(store)}
		server := httptest.NewServer(gateway)
		if !strings.HasPrefix(server.URL, "http://127.0.0.1:") {
			server.Close()
			t.Fatal("SDK fixture is not on numeric loopback")
		}
		cmd := exec.CommandContext(t.Context(), python, "-B", "-c", aclAssociationSDKScript, server.URL, receipt, phase)
		cmd.Dir = host
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + host, "PYTHONPATH=" + path,
			"AWS_EC2_METADATA_DISABLED=true", "AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null"}
		output, err := cmd.CombinedOutput()
		server.Close()
		if closeErr := store.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatalf("SDK %s failed: %v\n%s", phase, err, output)
		}
	}
}

const aclAssociationSDKScript = `
from pathlib import Path
from urllib.parse import urlparse
import json
import sys
import boto3, botocore
from botocore.config import Config
from botocore.exceptions import ClientError

endpoint, receipt, phase = sys.argv[1:]
address = urlparse(endpoint)
assert address.scheme == "http" and address.hostname == "127.0.0.1" and address.port
assert boto3.__version__ == "1.42.0" and botocore.__version__ == "1.41.6"
def client(region):
    return boto3.client("ec2", endpoint_url=endpoint, region_name=region,
        aws_access_key_id="TCSIM_ACL_SDK_FIXTURE", aws_secret_access_key="dummy-local-only",
        config=Config(retries={"max_attempts": 0}, connect_timeout=1, read_timeout=4, proxies={}))
def scoped(client, subnet, acl, default):
    [row] = client.describe_network_acls(Filters=[{"Name": "association.subnet-id", "Values": [subnet]}])["NetworkAcls"]
    assert row["NetworkAclId"] == acl and row["IsDefault"] == default
    [binding] = [a for a in row["Associations"] if a["SubnetId"] == subnet]
    assert binding["NetworkAclId"] == acl
    return binding["NetworkAclAssociationId"]
def denied(function, code, **params):
    try: function(**params)
    except ClientError as error: assert error.response["Error"]["Code"] == code
    else: raise AssertionError(f"expected {code} from fixture")

def replay_create(client, vpc, acl, subnet, association):
    [readback] = client.describe_network_acls(NetworkAclIds=[acl])["NetworkAcls"]
    before = client.describe_network_acls()["NetworkAcls"]
    result = client.create_network_acl(VpcId=vpc, ClientToken="fixture-sdk-create-replay")
    assert result["ClientToken"] == "fixture-sdk-create-replay" and result["NetworkAcl"] == readback
    assert result["NetworkAcl"]["Associations"] == [
        {"NetworkAclAssociationId": association, "NetworkAclId": acl, "SubnetId": subnet}]
    assert client.describe_network_acls()["NetworkAcls"] == before

ec2 = client("us-east-1")
if phase == "initial":
    vpc = ec2.create_vpc(CidrBlock="10.97.0.0/16")["Vpc"]["VpcId"]
    subnet = ec2.create_subnet(VpcId=vpc, CidrBlock="10.97.1.0/24")["Subnet"]["SubnetId"]
    [default] = ec2.describe_network_acls(Filters=[{"Name": "vpc-id", "Values": [vpc]}])["NetworkAcls"]
    assert default["IsDefault"]
    d = default["NetworkAclId"]
    old = scoped(ec2, subnet, d, True)
    acl = ec2.create_network_acl(VpcId=vpc, ClientToken="fixture-sdk-create-replay")["NetworkAcl"]["NetworkAclId"]
    other = client("ap-northeast-1")
    denied(other.replace_network_acl_association, "InvalidAssociationID.NotFound", AssociationId=old, NetworkAclId=acl)
    new = ec2.replace_network_acl_association(AssociationId=old, NetworkAclId=acl)["NewAssociationId"]
    assert new != old and scoped(ec2, subnet, acl, False) == new
    replay_create(ec2, vpc, acl, subnet, new)
    denied(ec2.delete_network_acl, "DependencyViolation", NetworkAclId=acl)
    Path(receipt).write_text(json.dumps([vpc, subnet, d, old, acl, new]))
elif phase == "restart":
    vpc, subnet, d, old, acl, new = json.loads(Path(receipt).read_text())
    assert scoped(ec2, subnet, acl, False) == new
    replay_create(ec2, vpc, acl, subnet, new)
    assert ec2.replace_network_acl_association(AssociationId=old, NetworkAclId=acl)["NewAssociationId"] == new
    current = ec2.replace_network_acl_association(AssociationId=new, NetworkAclId=d)["NewAssociationId"]
    assert current not in (old, new) and scoped(ec2, subnet, d, True) == current
    ec2.delete_network_acl(NetworkAclId=acl)
    denied(ec2.create_network_acl, "InvalidNetworkAclID.NotFound", VpcId=vpc, ClientToken="fixture-sdk-create-replay")
    ec2.delete_subnet(SubnetId=subnet)
    ec2.delete_vpc(VpcId=vpc)
    assert not ec2.describe_network_acls()["NetworkAcls"]
else: raise AssertionError("unrecognized phase")
`
