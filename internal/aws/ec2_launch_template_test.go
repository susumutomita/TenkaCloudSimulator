package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func launchTemplateNativeSeed(t *testing.T, f ec2VPCFixture) string {
	t.Helper()
	vpc := f.create("", url.Values{"CidrBlock": {"10.90.0.0/16"}})
	body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"CreateSecurityGroup"}, "VpcId": {vpc.ID}, "GroupName": {"template-web"}, "GroupDescription": {"Template web traffic"}}), 200, "")
	var result struct {
		ID string `xml:"groupId"`
	}
	if err := xml.Unmarshal([]byte(body), &result); err != nil || result.ID == "" {
		t.Fatalf("native group: %v %s", err, body)
	}
	return result.ID
}
func TestEC2LaunchTemplateNativeLifecycle(t *testing.T) {
	f := newEC2VPCFixture(t)
	group := launchTemplateNativeSeed(t, f)
	create := url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {"pcs-web"}, "ClientToken": {"owned-create-1"}, "VersionDescription": {"Security groups only"}, "LaunchTemplateData.SecurityGroupId.1": {group}}
	body := f.expect(f.call("", "us-east-1", nil, create), 200, "")
	var created struct {
		XMLName  xml.Name
		Template ec2LaunchTemplateWire `xml:"launchTemplate"`
	}
	if err := xml.Unmarshal([]byte(body), &created); err != nil || created.XMLName.Space != "http://ec2.amazonaws.com/doc/2016-11-15/" || !ec2LaunchTemplateID.MatchString(created.Template.ID) || created.Template.Default != 1 || created.Template.Latest != 1 {
		t.Fatalf("create: %v %s", err, body)
	}
	replay := f.expect(f.call("", "us-east-1", nil, create), 200, "")
	var again struct {
		Template ec2LaunchTemplateWire `xml:"launchTemplate"`
	}
	_ = xml.Unmarshal([]byte(replay), &again)
	if !reflect.DeepEqual(created.Template, again.Template) {
		t.Fatalf("replay changed immutable receipt")
	}
	versions := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplateVersions"}, "LaunchTemplateId": {created.Template.ID}, "LaunchTemplateVersion.1": {"$Latest"}}), 200, "")
	var saved struct {
		Versions []ec2LaunchTemplateVersionWire `xml:"launchTemplateVersionSet>item"`
	}
	if err := xml.Unmarshal([]byte(versions), &saved); err != nil || len(saved.Versions) != 1 || !reflect.DeepEqual(saved.Versions[0].Groups, []string{group}) || saved.Versions[0].Description != "Security groups only" {
		t.Fatalf("version: %v %s", err, versions)
	}
	changed, _ := url.ParseQuery(create.Encode())
	changed.Set("VersionDescription", "Different request")
	f.expect(f.call("", "us-east-1", nil, changed), 400, "IdempotentParameterMismatch")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteLaunchTemplate"}, "LaunchTemplateId": {created.Template.ID}}), 200, "")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplates"}, "LaunchTemplateId.1": {created.Template.ID}}), 400, "InvalidLaunchTemplateId.NotFound")
	f.expect(f.call("", "us-east-1", nil, create), 200, "")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplates"}, "LaunchTemplateId.1": {created.Template.ID}}), 400, "InvalidLaunchTemplateId.NotFound")
	create.Del("ClientToken")
	reborn := f.expect(f.call("", "us-east-1", nil, create), 200, "")
	if strings.Contains(reborn, created.Template.ID) {
		t.Fatal("same-name recreation reused retired identity")
	}
}

func TestEC2LaunchTemplateStrictProfile(t *testing.T) {
	f := newEC2VPCFixture(t)
	group := launchTemplateNativeSeed(t, f)
	for _, field := range []string{"LaunchTemplateData.UserData", "LaunchTemplateData.IamInstanceProfile.Arn", "LaunchTemplateData.NetworkInterface.1.SubnetId", "TagSpecification.1.ResourceType", "MaxResults"} {
		form := url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {"unsupported"}, "LaunchTemplateData.SecurityGroupId.1": {group}, field: {"unsupported"}}
		f.expect(f.call("", "us-east-1", nil, form), 400, "UnsupportedOperation")
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {"missing-sg"}, "LaunchTemplateData.SecurityGroupId.1": {"sg-00000000000000000"}}), 400, "InvalidGroup.NotFound")
	body := f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplates"}}), 200, "")
	if strings.Contains(body, "<launchTemplateId>") {
		t.Fatal("refused requests persisted templates")
	}
}

func launchTemplateFrame(t *testing.T, db *sql.DB) string {
	t.Helper()
	result := map[string][]string{}
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err = tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = tables.Err(); err != nil {
		t.Fatal(err)
	}
	tables.Close()
	for _, table := range names {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var entries []string
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, string(raw))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		slices.Sort(entries)
		result[table] = entries
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestEC2LaunchTemplateAuthorityAndRetiredDependencies(t *testing.T) {
	f := newEC2VPCFixture(t)
	group := launchTemplateNativeSeed(t, f)
	form := url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {"authorization"}, "LaunchTemplateData.SecurityGroupId.1": {group}}
	set := func(document string) {
		t.Helper()
		policy, err := core.ParsePolicy([]byte(document))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.g.Identities.Set("learner", policy); err != nil {
			t.Fatal(err)
		}
	}
	before := launchTemplateFrame(t, f.store.Database())
	f.expect(f.call("missing", "us-east-1", nil, form), 403, "UnauthorizedOperation")
	if launchTemplateFrame(t, f.store.Database()) != before {
		t.Fatal("denied creation changed provider state")
	}
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:CreateLaunchTemplate","Resource":"arn:aws:ec2:us-east-1:000000000000:launch-template/*"}]}`)
	body := f.expect(f.call("learner", "us-east-1", nil, form), 200, "")
	var created struct {
		Template ec2LaunchTemplateWire `xml:"launchTemplate"`
	}
	if err := xml.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Template.ID
	read := url.Values{"Action": {"DescribeLaunchTemplateVersions"}, "LaunchTemplateId": {id}}
	set(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeLaunchTemplateVersions","Resource":%q}]}`, networkARN("us-east-1", "launch-template", id)))
	f.expect(f.call("learner", "us-east-1", nil, read), 403, "UnauthorizedOperation")
	set(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeLaunchTemplateVersions","Resource":"*"}]}`)
	f.expect(f.call("learner", "us-east-1", nil, read), 200, "")
	scoped := context.WithValue(context.Background(), consoleInstanceBoundaryKey{}, consoleInstanceBoundary{region: "us-east-1"})
	f.expect(f.call("", "us-east-1", scoped, read), 403, "UnauthorizedOperation")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DeleteSecurityGroup"}, "GroupId": {group}}), 200, "")
	f.expect(f.call("learner", "us-east-1", nil, read), 200, "") // historical, not current admission
	tx, err := f.store.Database().Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = readEC2LaunchTemplate(context.Background(), tx, "us-east-1", id, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admitEC2LaunchTemplate(context.Background(), tx, "us-east-1", id)
	tx.Rollback()
	if err == nil {
		t.Fatal("retired SG was admitted as current")
	}
	set(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DeleteLaunchTemplate","Resource":%q}]}`, networkARN("us-east-1", "launch-template", id)))
	f.expect(f.call("learner", "us-east-1", nil, url.Values{"Action": {"DeleteLaunchTemplate"}, "LaunchTemplateName": {"authorization"}}), 200, "")
}

func TestEC2LaunchTemplateWriteRollback(t *testing.T) {
	for _, fault := range []struct{ name, sql string }{
		{"ignored insert", `CREATE TRIGGER lt_fault BEFORE INSERT ON aws_ec2_launch_templates BEGIN SELECT RAISE(IGNORE); END`},
		{"removed insert", `CREATE TRIGGER lt_fault AFTER INSERT ON aws_ec2_launch_templates BEGIN DELETE FROM aws_ec2_launch_templates WHERE region=NEW.region AND id=NEW.id; END`},
		{"ignored receipt", `CREATE TRIGGER lt_fault BEFORE INSERT ON aws_ec2_launch_template_tokens BEGIN SELECT RAISE(IGNORE); END`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := newEC2VPCFixture(t)
			peer := newEC2VPCFixture(t)
			group := launchTemplateNativeSeed(t, f)
			launchTemplateNativeSeed(t, peer)
			before, other := launchTemplateFrame(t, f.store.Database()), launchTemplateFrame(t, peer.store.Database())
			if _, err := f.store.Database().Exec(fault.sql); err != nil {
				t.Fatal(err)
			}
			form := url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {"rollback"}, "ClientToken": {"retry-token"}, "LaunchTemplateData.SecurityGroupId.1": {group}}
			f.expect(f.call("", "us-east-1", nil, form), 500, "InternalError")
			if launchTemplateFrame(t, f.store.Database()) != before || launchTemplateFrame(t, peer.store.Database()) != other {
				t.Fatal("failed write changed owner or peer frame")
			}
			if _, err := f.store.Database().Exec(`DROP TRIGGER lt_fault`); err != nil {
				t.Fatal(err)
			}
			body := f.expect(f.call("", "us-east-1", nil, form), 200, "")
			if !strings.Contains(body, "<launchTemplateName>rollback</launchTemplateName>") || launchTemplateFrame(t, f.store.Database()) == before {
				t.Fatal("healthy retry failed to persist actual requested resource")
			}
			if launchTemplateFrame(t, peer.store.Database()) != other {
				t.Fatal("healthy retry changed peer")
			}
		})
	}
}

func TestEC2LaunchTemplateReopenAndRetirementRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.db")
	open := func() ec2VPCFixture {
		db, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		stacks, err := NewStackStore(db.Database())
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		return ec2VPCFixture{t, &Gateway{Stacks: stacks, Identities: core.NewIdentities(db)}, stacks, db}
	}
	f := open()
	t.Cleanup(func() { _ = f.store.Close() })
	group := launchTemplateNativeSeed(t, f)
	form := url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {"reopened"}, "ClientToken": {"reopen-create"}, "LaunchTemplateData.SecurityGroupId.1": {group}}
	body := f.expect(f.call("", "us-east-1", nil, form), 200, "")
	var created struct {
		Template ec2LaunchTemplateWire `xml:"launchTemplate"`
	}
	if err := xml.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	replay := f.expect(f.call("", "us-east-1", nil, form), 200, "")
	var receipt struct {
		Template ec2LaunchTemplateWire `xml:"launchTemplate"`
	}
	if err := xml.Unmarshal([]byte(replay), &receipt); err != nil || !reflect.DeepEqual(receipt, created) {
		t.Fatalf("reopened receipt differs: %v", err)
	}
	peer := newEC2VPCFixture(t)
	launchTemplateNativeSeed(t, peer)
	before, other := launchTemplateFrame(t, f.store.Database()), launchTemplateFrame(t, peer.store.Database())
	remove := url.Values{"Action": {"DeleteLaunchTemplate"}, "LaunchTemplateId": {created.Template.ID}}
	for _, sql := range []string{
		`CREATE TRIGGER lt_retire_fault BEFORE UPDATE ON aws_ec2_launch_templates BEGIN SELECT RAISE(IGNORE); END`,
		`CREATE TRIGGER lt_retire_fault AFTER UPDATE ON aws_ec2_launch_templates BEGIN UPDATE aws_ec2_launch_templates SET retired=0 WHERE region=NEW.region AND id=NEW.id; END`,
	} {
		if _, err := f.store.Database().Exec(sql); err != nil {
			t.Fatal(err)
		}
		f.expect(f.call("", "us-east-1", nil, remove), 500, "InternalError")
		if launchTemplateFrame(t, f.store.Database()) != before || launchTemplateFrame(t, peer.store.Database()) != other {
			t.Fatal("failed retirement changed owner or peer")
		}
		if _, err := f.store.Database().Exec(`DROP TRIGGER lt_retire_fault`); err != nil {
			t.Fatal(err)
		}
	}
	f.expect(f.call("", "us-east-1", nil, remove), 200, "")
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	f.expect(f.call("", "us-east-1", nil, form), 200, "")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplates"}, "LaunchTemplateId.1": {created.Template.ID}}), 400, "InvalidLaunchTemplateId.NotFound")
	if launchTemplateFrame(t, peer.store.Database()) != other {
		t.Fatal("reopen replay changed peer")
	}
}

func TestEC2LaunchTemplateRevokedReplayAndSelectedCorruption(t *testing.T) {
	f := newEC2VPCFixture(t)
	group := launchTemplateNativeSeed(t, f)
	set := func(allowed bool) {
		t.Helper()
		effect := "Deny"
		if allowed {
			effect = "Allow"
		}
		policy, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":%q,"Action":"ec2:CreateLaunchTemplate","Resource":"*"}]}`, effect)))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.g.Identities.Set("learner", policy); err != nil {
			t.Fatal(err)
		}
	}
	set(true)
	form := url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {"healthy"}, "ClientToken": {"saved-token"}, "LaunchTemplateData.SecurityGroupId.1": {group}}
	body := f.expect(f.call("learner", "us-east-1", nil, form), 200, "")
	var original struct {
		Template ec2LaunchTemplateWire `xml:"launchTemplate"`
	}
	if err := xml.Unmarshal([]byte(body), &original); err != nil {
		t.Fatal(err)
	}
	set(false)
	before := launchTemplateFrame(t, f.store.Database())
	f.expect(f.call("learner", "us-east-1", nil, form), 403, "UnauthorizedOperation")
	changed, _ := url.ParseQuery(form.Encode())
	changed.Set("VersionDescription", "changed")
	f.expect(f.call("learner", "us-east-1", nil, changed), 403, "UnauthorizedOperation")
	if launchTemplateFrame(t, f.store.Database()) != before {
		t.Fatal("revoked replay changed stored frame")
	}
	set(true)
	repaired := f.expect(f.call("learner", "us-east-1", nil, form), 200, "")
	var replay struct {
		Template ec2LaunchTemplateWire `xml:"launchTemplate"`
	}
	if err := xml.Unmarshal([]byte(repaired), &replay); err != nil || !reflect.DeepEqual(original, replay) {
		t.Fatalf("repaired replay changed receipt: %v", err)
	}
	f.expect(f.call("learner", "us-east-1", nil, changed), 400, "IdempotentParameterMismatch")
	sibling, _ := url.ParseQuery(form.Encode())
	sibling.Set("LaunchTemplateName", "corrupt-sibling")
	sibling.Del("ClientToken")
	body = f.expect(f.call("learner", "us-east-1", nil, sibling), 200, "")
	var damaged struct {
		Template ec2LaunchTemplateWire `xml:"launchTemplate"`
	}
	if err := xml.Unmarshal([]byte(body), &damaged); err != nil {
		t.Fatal(err)
	}
	var healthyRaw string
	if err := f.store.Database().QueryRow(`SELECT document FROM aws_ec2_launch_templates WHERE id=?`, damaged.Template.ID).Scan(&healthyRaw); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(healthyRaw), &document); err != nil {
		t.Fatal(err)
	}
	document["description"] = "damaged\x00metadata"
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Database().Exec(`UPDATE aws_ec2_launch_templates SET document=? WHERE id=?`, string(raw), damaged.Template.ID); err != nil {
		t.Fatal(err)
	}
	before = launchTemplateFrame(t, f.store.Database())
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplates"}, "LaunchTemplateId.1": {original.Template.ID}}), 200, "")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplateVersions"}, "LaunchTemplateName": {"healthy"}}), 200, "")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplateVersions"}, "LaunchTemplateId": {damaged.Template.ID}}), 500, "InternalError")
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplates"}}), 500, "InternalError")
	if launchTemplateFrame(t, f.store.Database()) != before {
		t.Fatal("selected reads changed corrupted frame")
	}
	if _, err = f.store.Database().Exec(`UPDATE aws_ec2_launch_templates SET document=? WHERE id=?`, healthyRaw, damaged.Template.ID); err != nil {
		t.Fatal(err)
	}
	f.expect(f.call("", "us-east-1", nil, url.Values{"Action": {"DescribeLaunchTemplateVersions"}, "LaunchTemplateId": {damaged.Template.ID}}), 200, "")
}

// A real elapsed deadline occurs only after SQLite exposes the physical new
// row inside the ordinary native write. No pre-canceled shortcut is used.
func TestEC2LaunchTemplateStagedInterruption(t *testing.T) {
	for _, trial := range []struct {
		action      string
		cancelEarly bool
	}{
		{"CreateLaunchTemplate", false}, {"DeleteLaunchTemplate", false},
		{"CreateLaunchTemplate", true}, {"DeleteLaunchTemplate", true},
	} {
		action := trial.action
		t.Run(fmt.Sprintf("%s/cancel=%t", action, trial.cancelEarly), func(t *testing.T) {
			f := newEC2VPCFixture(t)
			peer := newEC2VPCFixture(t)
			group := launchTemplateNativeSeed(t, f)
			launchTemplateNativeSeed(t, peer)
			form := url.Values{"Action": {"CreateLaunchTemplate"}, "LaunchTemplateName": {"deadline"}, "ClientToken": {"deadline-token"}, "LaunchTemplateData.SecurityGroupId.1": {group}}
			event := "INSERT"
			if action == "DeleteLaunchTemplate" {
				body := f.expect(f.call("", "us-east-1", nil, form), 200, "")
				var created struct {
					Template ec2LaunchTemplateWire `xml:"launchTemplate"`
				}
				if err := xml.Unmarshal([]byte(body), &created); err != nil {
					t.Fatal(err)
				}
				form = url.Values{"Action": {action}, "LaunchTemplateId": {created.Template.ID}}
				event = "UPDATE"
			}
			before, other := launchTemplateFrame(t, f.store.Database()), launchTemplateFrame(t, peer.store.Database())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			type observation struct {
				id, document string
				retired      int
				err          error
			}
			seen := make(chan observation, 1)
			conn, err := f.store.Database().Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = conn.Raw(func(raw any) error {
				return raw.(driver.Conn).Raw().CreateFunction("observe_lt_deadline", 3, 0, func(fn sqlite3.Context, args ...sqlite3.Value) {
					seen <- observation{args[0].Text(), args[1].Text(), args[2].Int(), ctx.Err()}
					if trial.cancelEarly {
						cancel()
					}
					<-ctx.Done()
					fn.ResultNull()
				})
			})
			if err != nil {
				conn.Close()
				t.Fatal(err)
			}
			_, err = conn.ExecContext(t.Context(), `CREATE TEMP TRIGGER lt_deadline AFTER `+event+` ON aws_ec2_launch_templates BEGIN SELECT observe_lt_deadline(NEW.id,NEW.document,NEW.retired); END`)
			if closeErr := conn.Close(); err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			began := time.Now()
			f.expect(f.call("", "us-east-1", ctx, form), 500, "InternalError")
			if trial.cancelEarly {
				if ctx.Err() != context.Canceled {
					t.Fatal("staged cancellation was not observed")
				}
			} else if ctx.Err() != context.DeadlineExceeded || time.Since(began) < 900*time.Millisecond {
				t.Fatal("real elapsed deadline was not observed")
			}
			select {
			case staged := <-seen:
				var saved ec2LaunchTemplate
				if staged.err != nil || !ec2LaunchTemplateID.MatchString(staged.id) || json.Unmarshal([]byte(staged.document), &saved) != nil || saved.ID != staged.id || saved.Name != "deadline" {
					t.Fatal("actual staged template was not observed before expiration")
				}
				want := 0
				if action == "DeleteLaunchTemplate" {
					want = 1
				}
				if staged.retired != want {
					t.Fatal("wrong staged retirement state")
				}
			default:
				t.Fatal("deadline happened before actual SQLite write")
			}
			if launchTemplateFrame(t, f.store.Database()) != before || launchTemplateFrame(t, peer.store.Database()) != other {
				t.Fatal("deadline published owner or peer changes")
			}
			if _, err = f.store.Database().Exec(`DROP TRIGGER IF EXISTS lt_deadline`); err != nil {
				t.Fatal(err)
			}
			f.expect(f.call("", "us-east-1", nil, form), 200, "")
			if launchTemplateFrame(t, f.store.Database()) == before || launchTemplateFrame(t, peer.store.Database()) != other {
				t.Fatal("repair failed to make actual scoped change")
			}
		})
	}
}
