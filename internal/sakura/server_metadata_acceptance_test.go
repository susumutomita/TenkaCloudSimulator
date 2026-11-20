package sakura_test

// Independent Finder oracle for #423. Simulator-only metadata, not VM support.
// Source: Sakura server manual and SDK d8e98d8762e049e3ffd40bc70667cf9ad20029c0.
// The fixed payload below was captured through its in-process transport.
// HTTP 201/200/404 and strict refusals here are the proposed local contract,
// not an assertion that a real Sakura service was queried. No SDK dependency.
import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	common "github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

const serverMetadataPath = "/cloud/zone/is1a/api/cloud/1.1/server"
const serverMetadataCreate = `{"Server":{"Name":"metadata-fixture","Description":"before","Tags":[],"Icon":{"ID":0,"Tags":[]},"InterfaceDriver":"virtio","ServerPlan":{"CPU":2,"MemoryMB":4096,"Commitment":"standard","ConfidentialVM":false},"PrivateHost":{"ID":0,"Description":"","Tags":[]},"WaitDiskMigration":false}}`
const serverMetadataUpdate = `{"Server":{"Name":"renamed","Description":"","Tags":[],"Icon":{"ID":0,"Tags":[]},"InterfaceDriver":"virtio","PrivateHost":{"ID":0,"Description":"","Tags":[]},"WaitDiskMigration":false}}`

func serverMetadataCall(t *testing.T, h http.Handler, method, path, body string, a *common.Actor, status int) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(method, path, body, a))
	if w.Code != status {
		t.Fatalf("%s %s: status %d want %d", method, path, w.Code, status)
	}
	var v map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
	}
	return v
}
func serverMetadataRow(v map[string]any, id, name, description string) error {
	row, ok := v["Server"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing Server")
	}
	got := fmt.Sprint(row["ID"])
	n, e := strconv.ParseUint(got, 10, 64)
	if e != nil || n == 0 || strconv.FormatUint(n, 10) != got || (id != "" && id != got) {
		return fmt.Errorf("wrong identity")
	}
	if row["Name"] != name || row["Description"] != description {
		return fmt.Errorf("wrong metadata")
	}
	p, ok := row["ServerPlan"].(map[string]any)
	if !ok || p["CPU"] != float64(2) || p["MemoryMB"] != float64(4096) {
		return fmt.Errorf("wrong fixture plan")
	}
	if _, exists := p["ID"]; exists {
		return fmt.Errorf("invented plan ID")
	}
	for _, k := range []string{"Availability", "Instance", "HostName", "Disks", "Interfaces", "IPAddress", "MACAddress"} {
		if _, exists := row[k]; exists {
			return fmt.Errorf("physical claim %s", k)
		}
	}
	return nil
}
func serverMetadataRequire(t *testing.T, v map[string]any, id, name, desc string) string {
	t.Helper()
	if e := serverMetadataRow(v, id, name, desc); e != nil {
		t.Fatal(e)
	}
	return fmt.Sprint(v["Server"].(map[string]any)["ID"])
}
func serverMetadataList(t *testing.T, h http.Handler, a *common.Actor, expected map[string][2]string) map[string]any {
	t.Helper()
	v := serverMetadataCall(t, h, "GET", serverMetadataPath+`?{"Count":10,"From":0}`, "", a, 200)
	rows, ok := v["Servers"].([]any)
	if !ok || len(rows) != len(expected) || v["Count"] != float64(len(expected)) || v["Total"] != float64(len(expected)) || v["From"] != float64(0) {
		t.Fatal("wrong list cardinality/metadata")
	}
	seen := map[string]bool{}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("bad row")
		}
		id := fmt.Sprint(row["ID"])
		want, ok := expected[id]
		if !ok || seen[id] {
			t.Fatal("unexpected/duplicate row")
		}
		seen[id] = true
		serverMetadataRequire(t, map[string]any{"Server": row}, id, want[0], want[1])
	}
	return v
}
func TestSakuraServerMetadataAcceptanceCRUD(t *testing.T) {
	_, db := database(t)
	h := construct(t, db, config)
	a := actor()
	// Existing Disk is a persisted sentinel, not a substitute Server implementation.
	call(t, h, request("POST", nativePath, `{"Disk":{"Name":"disk-sentinel","Plan":{"ID":4},"SizeMB":20480}}`, &a), 201)
	diskBefore := frame(t, db)
	id := serverMetadataRequire(t, serverMetadataCall(t, h, "POST", serverMetadataPath, serverMetadataCreate, &a, 201), "", "metadata-fixture", "before")
	serverMetadataRequire(t, serverMetadataCall(t, h, "GET", serverMetadataPath+"/"+id, "", &a, 200), id, "metadata-fixture", "before")
	serverMetadataList(t, h, &a, map[string][2]string{id: {"metadata-fixture", "before"}})
	serverMetadataRequire(t, serverMetadataCall(t, h, "PUT", serverMetadataPath+"/"+id, serverMetadataUpdate, &a, 200), id, "renamed", "")
	// Raw partial PUT is distinct from SDK's explicit empty description.
	serverMetadataCall(t, h, "PUT", serverMetadataPath+"/"+id, `{"Server":{"Description":"kept"}}`, &a, 200)
	serverMetadataCall(t, h, "PUT", serverMetadataPath+"/"+id, `{"Server":{"Name":"partial"}}`, &a, 200)
	h = construct(t, db, config) // handler reconstruction; NOT a process/database reopen proof
	serverMetadataRequire(t, serverMetadataCall(t, h, "GET", serverMetadataPath+"/"+id, "", &a, 200), id, "partial", "kept")
	serverMetadataList(t, h, &a, map[string][2]string{id: {"partial", "kept"}})
	serverMetadataCall(t, h, "DELETE", serverMetadataPath+"/"+id, "", &a, 200)
	serverMetadataCall(t, h, "GET", serverMetadataPath+"/"+id, "", &a, 404)
	serverMetadataList(t, h, &a, map[string][2]string{}) // before any cleanup
	newID := serverMetadataRequire(t, serverMetadataCall(t, h, "POST", serverMetadataPath, serverMetadataCreate, &a, 201), "", "metadata-fixture", "before")
	serverMetadataCall(t, h, "GET", serverMetadataPath+"/"+id, "", &a, 404) // recreation must not resurrect the old row
	if newID == id {
		t.Fatal("deleted identity reused")
	}
	if frame(t, db) != diskBefore {
		t.Fatal("Server mutated Disk sentinel")
	}
}
func TestSakuraServerMetadataAcceptanceRefusals(t *testing.T) {
	_, db := database(t)
	h := construct(t, db, config)
	a := actor()
	id := serverMetadataRequire(t, serverMetadataCall(t, h, "POST", serverMetadataPath, serverMetadataCreate, &a, 201), "", "metadata-fixture", "before")
	expected := map[string][2]string{id: {"metadata-fixture", "before"}}
	before := serverMetadataList(t, h, &a, expected)
	resourceBefore := serverMetadataCall(t, h, "GET", serverMetadataPath+"/"+id, "", &a, 200)
	diskBefore := frame(t, db)
	cases := []struct {
		name, method, path, body string
		actor                    *common.Actor
		badBasic                 bool
	}{
		{"missing actor", "DELETE", serverMetadataPath + "/" + id, "", nil, false},
		{"other owner", "DELETE", serverMetadataPath + "/" + id, "", &common.Actor{CredentialIdentity: "other", ExerciseIdentity: "other", CredentialRole: common.CredentialParticipant}, false},
		{"missing actor GET", "GET", serverMetadataPath + "/" + id, "", nil, false},
		{"missing actor list", "GET", serverMetadataPath + `?{"Count":10,"From":0}`, "", nil, false},
		{"missing actor PUT", "PUT", serverMetadataPath + "/" + id, serverMetadataUpdate, nil, false},
		{"other owner GET", "GET", serverMetadataPath + "/" + id, "", &common.Actor{CredentialIdentity: "other", ExerciseIdentity: "other", CredentialRole: common.CredentialParticipant}, false},
		{"other owner list", "GET", serverMetadataPath + `?{"Count":10,"From":0}`, "", &common.Actor{CredentialIdentity: "other", ExerciseIdentity: "other", CredentialRole: common.CredentialParticipant}, false},
		{"other owner PUT", "PUT", serverMetadataPath + "/" + id, serverMetadataUpdate, &common.Actor{CredentialIdentity: "other", ExerciseIdentity: "other", CredentialRole: common.CredentialParticipant}, false},
		{"PUT unknown", "PUT", serverMetadataPath + "/" + id, `{"Server":{"Name":"changed","Unexpected":true}}`, &a, false},
		{"PUT icon", "PUT", serverMetadataPath + "/" + id, `{"Server":{"Name":"changed","Icon":{"ID":99}}}`, &a, false},
		{"PUT private host", "PUT", serverMetadataPath + "/" + id, `{"Server":{"Name":"changed","PrivateHost":{"ID":99}}}`, &a, false},
		{"PUT plan mutation", "PUT", serverMetadataPath + "/" + id, `{"Server":{"Name":"changed","ServerPlan":{"CPU":4,"MemoryMB":8192}}}`, &a, false},
		{"bad marker", "DELETE", serverMetadataPath + "/" + id, "", &a, true},
		{"other zone", "DELETE", strings.Replace(serverMetadataPath, "is1a", "tk1a", 1) + "/" + id, "", &a, false},
		{"encoded delimiter", "DELETE", strings.Replace(serverMetadataPath, "/cloud/zone", "/cloud%2Fzone", 1) + "/" + id, "", &a, false},
		{"noncanonical id", "DELETE", serverMetadataPath + "/0" + id, "", &a, false},
		{"attached delete", "DELETE", serverMetadataPath + "/" + id, `{"WithDisk":[1]}`, &a, false},
	}
	for name, body := range map[string]string{
		"wrong cpu":            strings.Replace(serverMetadataCreate, `"CPU":2`, `"CPU":4`, 1),
		"wrong memory":         strings.Replace(serverMetadataCreate, `"MemoryMB":4096`, `"MemoryMB":8192`, 1),
		"plan id":              strings.Replace(serverMetadataCreate, `"CPU":2`, `"ID":100,"CPU":2`, 1),
		"private host":         strings.Replace(serverMetadataCreate, `"PrivateHost":{"ID":0`, `"PrivateHost":{"ID":99`, 1),
		"icon":                 strings.Replace(serverMetadataCreate, `"Icon":{"ID":0`, `"Icon":{"ID":99`, 1),
		"nic":                  strings.Replace(serverMetadataCreate, `"Name":"metadata-fixture"`, `"ConnectedSwitches":[{"ID":1}],"Name":"metadata-fixture"`, 1),
		"gpu":                  strings.Replace(serverMetadataCreate, `"CPU":2`, `"GPU":1,"CPU":2`, 1),
		"unknown":              `{"Server":{"Name":"x","Unexpected":true}}`,
		"duplicate":            `{"Server":{"Name":"a","Name":"b","ServerPlan":{"CPU":2,"MemoryMB":4096}}}`,
		"null":                 `{"Server":null}`,
		"empty name":           strings.Replace(serverMetadataCreate, "metadata-fixture", "", 1),
		"oversize name":        strings.Replace(serverMetadataCreate, "metadata-fixture", strings.Repeat("x", 65), 1),
		"oversize description": strings.Replace(serverMetadataCreate, `"before"`, `"`+strings.Repeat("x", 513)+`"`, 1),
	} {
		cases = append(cases, struct {
			name, method, path, body string
			actor                    *common.Actor
			badBasic                 bool
		}{name, "POST", serverMetadataPath, body, &a, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := request(tc.method, tc.path, tc.body, tc.actor)
			r.Header.Set("X-Simulator-Principal", "learner")
			if tc.badBasic {
				r.SetBasicAuth("wrong", "wrong")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if (tc.actor == nil || tc.actor.ExerciseIdentity != config.Owner) && w.Code != http.StatusForbidden {
				t.Fatalf("owner denial status %d want 403", w.Code)
			}
			if strings.HasPrefix(tc.name, "PUT ") && w.Code != http.StatusBadRequest {
				t.Fatalf("invalid PUT status %d want 400", w.Code)
			}
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("rejection status %d", w.Code)
			}
			var rejected map[string]any
			if e := json.Unmarshal(w.Body.Bytes(), &rejected); e != nil {
				t.Fatal(e)
			}
			for _, k := range []string{"Server", "Servers", "AccessToken"} {
				if _, ok := rejected[k]; ok {
					t.Fatal("refusal leaked data")
				}
			}
			if got := serverMetadataList(t, h, &a, expected); !reflect.DeepEqual(got, before) {
				t.Fatal("refusal mutated complete listing")
			}
			after := serverMetadataCall(t, h, "GET", serverMetadataPath+"/"+id, "", &a, 200)
			serverMetadataRequire(t, after, id, "metadata-fixture", "before")
			if !reflect.DeepEqual(after, resourceBefore) {
				t.Fatal("refusal changed resource envelope")
			}
			if frame(t, db) != diskBefore {
				t.Fatal("refusal mutated Disk")
			}
		})
	}
}
func TestSakuraServerMetadataComparatorControls(t *testing.T) {
	good := `{"Server":{"ID":"1","Name":"x","Description":"y","ServerPlan":{"CPU":2,"MemoryMB":4096}}}`
	for _, tc := range []struct {
		body  string
		valid bool
	}{{good, true}, {`{}`, false}, {strings.Replace(good, `"CPU":2`, `"CPU":4`, 1), false}, {strings.Replace(good, `"Name":"x"`, `"Name":"wrong"`, 1), false}, {strings.Replace(good, `"ID":"1"`, `"ID":"2"`, 1), false}, {strings.Replace(good, `"Name":"x"`, `"Availability":"available","Name":"x"`, 1), false}} {
		var v map[string]any
		if e := json.Unmarshal([]byte(tc.body), &v); e != nil {
			t.Fatal(e)
		}
		if valid := serverMetadataRow(v, "1", "x", "y") == nil; valid != tc.valid {
			t.Fatal("comparator control failed")
		}
	}
}
