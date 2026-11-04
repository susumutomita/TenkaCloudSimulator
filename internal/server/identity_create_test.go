package server

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestIdentityConditionalCreatePreservesExistingPolicy(t *testing.T) {
	s := instance(t)
	path := "/_simulator/v1/iam/principals/executor/policy"
	allow := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`
	deny := strings.Replace(allow, "Allow", "Deny", 1)
	headers := map[string]string{"If-None-Match": "*"}
	status(t, s, "PUT", path, allow, 200, headers)
	status(t, s, "PUT", path, deny, 412, headers)
	_, body := call(t, s, "GET", path, "", nil)
	if !strings.Contains(string(body), `"Effect":"Allow"`) {
		t.Fatal("create replaced an existing execution role", string(body))
	}
	status(t, s, "PUT", path, deny, 400, map[string]string{"If-None-Match": `"other"`})
	status(t, s, "PUT", path, deny, 403, map[string]string{"If-None-Match": "*", "X-Simulator-Principal": "executor"})
	status(t, s, "PUT", path, deny, 200, nil)
	_, body = call(t, s, "GET", path, "", nil)
	if !strings.Contains(string(body), `"Effect":"Deny"`) {
		t.Fatal("explicit edit did not replace the role policy")
	}

	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for n := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			policy := strings.Replace(allow, "s3:GetObject", fmt.Sprintf("s3:Action%d", n), 1)
			response, _ := call(t, s, "PUT", "/_simulator/v1/iam/principals/concurrent/policy", policy, headers)
			codes <- response.StatusCode
		}(n)
	}
	wg.Wait()
	close(codes)
	created, conflicts := 0, 0
	for code := range codes {
		if code == 200 {
			created++
		} else if code == 412 {
			conflicts++
		} else {
			t.Errorf("unexpected concurrent create status %d", code)
		}
	}
	if created != 1 || conflicts != 7 {
		t.Fatalf("created=%d, conflicts=%d", created, conflicts)
	}
}
