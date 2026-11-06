package iaas

import (
	"context"
	"fmt"
	"strconv"
	"testing"
)

func TestDiskHTTPCommunitySDKNoIconDefaultIsMetadataEquivalent(t *testing.T) {
	store, db, _ := fixture(t)
	handler := testHandler(t, store)
	for _, icon := range []string{`null`, `{}`, `{"ID":0}`, `{"ID":0,"Tags":[]}`} {
		body := `{"Disk":{"Name":"neutral","Description":"unchanged","Plan":{"ID":4},"SizeMB":20480,"Icon":` + icon + `}}`
		result := resultDisk(t, diskCall(t, handler, "POST", diskPath, body, 201))
		if _, exists := result["Icon"]; exists {
			t.Fatal("invented Icon resource projection")
		}
		id, err := strconv.ParseInt(result["ID"].(string), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := store.Get(context.Background(), id)
		if err != nil || saved.Name != "neutral" || saved.Description != "unchanged" || saved.PlanID != 4 || saved.SizeMB != 20480 || saved.Connection != "virtio" {
			t.Fatal("neutral SDK default changed actual stored Disk metadata", err)
		}
		before := frame(t, db)
		// An explicit no-icon update is a true no-op, not a new Icon capability.
		updated := resultDisk(t, diskCall(t, handler, "PUT", diskPath+"/"+result["ID"].(string), `{"Disk":{"Icon":`+icon+`}}`, 200))
		if updated["Name"] != "neutral" || updated["Description"] != "unchanged" || frame(t, db) != before {
			t.Fatal("no-icon update changed saved graph")
		}
	}
}

func TestDiskHTTPCommunitySDKIconLookalikesRefuseWholeGraph(t *testing.T) {
	store, db, _ := fixture(t)
	handler := testHandler(t, store)
	kept := mustCreate(t, store, "retained")
	before := frame(t, db)
	for _, icon := range []string{
		`{"ID":"0"}`, `{"ID":1}`, `{"ID":false}`, `{"ID":null}`,
		`{"ID":0.0}`, `{"ID":0e0}`, `{"ID":-0}`, `{"Tags":[]}`,
		`{"ID":0,"Tags":null}`, `{"ID":0,"Tags":{}}`, `{"ID":0,"Tags":["real"]}`,
		`{"ID":0,"Name":"real"}`, `{"id":0}`, `{"ID":0,"tags":[]}`,
	} {
		t.Run(icon, func(t *testing.T) {
			create := `{"Disk":{"Name":"not-created","Plan":{"ID":4},"SizeMB":20480,"Icon":` + icon + `}}`
			diskCall(t, handler, "POST", diskPath, create, 501)
			diskCall(t, handler, "PUT", fmt.Sprintf("%s/%d", diskPath, kept.ID), `{"Disk":{"Name":"not-renamed","Icon":`+icon+`}}`, 501)
			if frame(t, db) != before {
				t.Fatal("unsupported Icon changed resources or allocator")
			}
		})
	}
	// Refusal never burns an ID or prevents a corrected actual request.
	repaired := resultDisk(t, diskCall(t, handler, "POST", diskPath, `{"Disk":{"Name":"repaired","Plan":{"ID":4},"SizeMB":20480,"Icon":{"ID":0,"Tags":[]}}}`, 201))
	if repaired["ID"] != strconv.FormatInt(kept.ID+1, 10) {
		t.Fatal("refused request allocated an ID")
	}
}
