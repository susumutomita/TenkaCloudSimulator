package guestvm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qmp"
)

func TestPreparedControlBindsDeploymentUUID(t *testing.T) {
	requireLinux(t)
	for _, tc := range []struct {
		name             string
		match, recovered bool
	}{
		{"matching", true, false}, {"wrong-vm", false, false}, {"recovered-matching", true, true}, {"recovered-wrong-vm", false, true},
	} {
		match := tc.match
		t.Run(tc.name, func(t *testing.T) {
			b, layout := testBundle(t, imageHeader())
			p, err := Prepare(context.Background(), shortDir(t), b, layout, acquire(), []byte("x"))
			if err != nil {
				t.Fatal(err)
			}
			openControl := p.OpenControl
			if tc.recovered {
				r, err := InspectPrepared(context.Background(), p.Directory(), p.Record())
				if err != nil {
					t.Fatal(err)
				}
				openControl = r.OpenControl
			}
			client, peer := net.Pipe()
			defer peer.Close()
			defer client.Close()
			if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				encoder := json.NewEncoder(peer)
				decoder := json.NewDecoder(bufio.NewReader(peer))
				if err := encoder.Encode(map[string]any{"QMP": map[string]any{"version": map[string]any{"qemu": map[string]int{"major": 10, "minor": 0, "micro": 0}, "package": ""}, "capabilities": []string{}}}); err != nil {
					done <- err
					return
				}
				for _, command := range []string{"qmp_capabilities", "query-uuid"} {
					var request struct {
						Execute string `json:"execute"`
						ID      uint64 `json:"id"`
					}
					if err := decoder.Decode(&request); err != nil {
						done <- err
						return
					}
					if request.Execute != command {
						done <- errors.New("unexpected control request")
						return
					}
					result := map[string]any{}
					if command == "query-uuid" {
						uuid := p.Record().UUID
						if !match {
							uuid = "00000000-0000-4000-8000-000000000000"
						}
						result["UUID"] = uuid
					}
					if err := encoder.Encode(map[string]any{"return": result, "id": request.ID}); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			control, err := openControl(ctx, client, qmp.Options{})
			if match {
				if err != nil {
					t.Fatal(err)
				}
				control.Close()
			} else if !errors.Is(err, qmp.ErrIdentity) || control != nil {
				t.Fatalf("wrong VM accepted: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
