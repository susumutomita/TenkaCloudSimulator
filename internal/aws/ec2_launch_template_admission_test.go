package aws

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"net/url"
	"reflect"
	"testing"
)

// This trusted component boundary is the prerequisite for future PCS admission;
// it is not a claim that today's PCS metadata create path invokes it.
func TestEC2LaunchTemplateCurrentAdmissionLifetime(t *testing.T) {
	f := newEC2VPCFixture(t)
	peer := newEC2VPCFixture(t)
	group := launchTemplateNativeSeed(t, f)
	launchTemplateNativeSeed(t, peer)
	makeTemplate := func(name, sg string) ec2LaunchTemplateWire {
		t.Helper()
		body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {name}, "LaunchTemplateData.SecurityGroupId.1": {sg}}), 200, "")
		var out struct {
			Template ec2LaunchTemplateWire `xml:"launchTemplate"`
		}
		if err := xml.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out.Template
	}
	check := func(host ec2VPCFixture, region, id string) (ec2LaunchTemplate, error) {
		t.Helper()
		tx, err := host.store.Database().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return admitEC2LaunchTemplate(context.Background(), tx, region, id)
	}
	original := makeTemplate("current-admission", group)
	saved, err := check(f, "us-east-1", original.ID)
	if err != nil || saved.ID != original.ID || !reflect.DeepEqual(saved.SecurityGroups, []string{group}) {
		t.Fatalf("fresh native admission: %v", err)
	}
	ownerBefore, peerBefore := launchTemplateFrame(t, f.store.Database()), launchTemplateFrame(t, peer.store.Database())
	if _, err = check(peer, "us-east-1", original.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign process admitted physical template: %v", err)
	}
	if _, err = check(f, "us-west-2", original.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign region admitted physical template: %v", err)
	}
	if launchTemplateFrame(t, f.store.Database()) != ownerBefore || launchTemplateFrame(t, peer.store.Database()) != peerBefore {
		t.Fatal("read-only refused admission changed stores")
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteSecurityGroup"}, "GroupId": {group}}), 200, "")
	body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"CreateSecurityGroup"}, "VpcId": {saved.VPCID}, "GroupName": {"template-web"}, "GroupDescription": {"Template web traffic"}}), 200, "")
	var replacement struct {
		ID string `xml:"groupId"`
	}
	if err = xml.Unmarshal([]byte(body), &replacement); err != nil || replacement.ID == group {
		t.Fatalf("replacement group identity: %v", err)
	}
	if _, err = check(f, "us-east-1", original.ID); err == nil {
		t.Fatal("old SG binding adopted same-name replacement")
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplateVersions"}, "LaunchTemplateId": {original.ID}}), 200, "")
	fresh := makeTemplate("new-dependency", replacement.ID)
	if _, err = check(f, "us-east-1", fresh.ID); err != nil {
		t.Fatalf("new saved physical dependency not admitted: %v", err)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteLaunchTemplate"}, "LaunchTemplateId": {fresh.ID}}), 200, "")
	if _, err = check(f, "us-east-1", fresh.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retired physical template admitted: %v", err)
	}
	if launchTemplateFrame(t, peer.store.Database()) != peerBefore {
		t.Fatal("lifetime operations changed peer")
	}
}
