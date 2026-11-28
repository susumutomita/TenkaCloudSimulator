package catalog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"
)

func gitCall(h *harness, repo, op string, args map[string]any) reply {
	input := map[string]any{"repositoryName": repo}
	maps.Copy(input, args)
	return h.json1("codecommit", op, input)
}

func TestCodeCommitGitObjectsAndBranches(t *testing.T) {
	h := newHarness(t, codeCommit)
	h.now = time.Unix(1700000000, 0)
	call := func(op string, input map[string]any) map[string]any {
		r := gitCall(h, "local", op, input)
		expect(t, r, 200, "")
		return r.body
	}
	call("CreateRepository", nil)
	if branches := call("ListBranches", nil)["branches"].([]any); len(branches) != 0 {
		t.Fatal(branches)
	}
	first := map[string]any{"branchName": "main", "filePath": "/dir/item.bin", "fileContent": "ZXhhbXBsZQD/Cg==", "name": "Fixture Author", "email": "author@example.invalid", "commitMessage": "Initial\n"}
	a := call("PutFile", first)
	// Independent git hash-object, mktree -z and commit-tree reference vectors.
	const aID = "1922d31c6bbb6249cd98484ae7d6cb416cfb3407"
	const bID = "82cb0e81967a11263112b9519b5a371b4f61f8f8"
	if a["blobId"] != "c813051dc99e3a5db2f0bc7d6aa442f1dd08ffc0" || a["treeId"] != "3a797aff2e53ab325f2b11e9187a7ac3f10f3578" || a["commitId"] != aID {
		t.Fatal(a)
	}
	if repo := call("GetRepository", nil)["repositoryMetadata"].(map[string]any); repo["defaultBranch"] != "main" {
		t.Fatal(repo)
	}
	second := maps.Clone(first)
	maps.Copy(second, map[string]any{"filePath": "dir.txt", "fileContent": "cmVhZCBtZQo=", "fileMode": "EXECUTABLE", "parentCommitId": aID, "commitMessage": "Update\n"})
	b := call("PutFile", second)
	if b["blobId"] != "d9b401251bb36c51ca5c56c2ffc8a24a78ff20ae" || b["treeId"] != "45ba2d44fcfaa7226be57de6593384f54eb45978" || b["commitId"] != bID {
		t.Fatal(b)
	}
	revision := call("GetCommit", map[string]any{"commitId": bID})["commit"].(map[string]any)
	if revision["message"] != "Update\n" || !reflect.DeepEqual(revision["parents"], []any{aID}) || revision["treeId"] != b["treeId"] || revision["author"].(map[string]any)["date"] != "1700000000 +0000" || !reflect.DeepEqual(revision["author"], revision["committer"]) {
		t.Fatal(revision)
	}
	call("CreateBranch", map[string]any{"branchName": "release/one", "commitId": aID})
	fork := maps.Clone(first)
	maps.Copy(fork, map[string]any{"parentCommitId": aID, "branchName": "release/one", "fileContent": ""})
	updated := call("PutFile", fork)
	file := func(ref, path string) map[string]any {
		args := map[string]any{"filePath": path}
		if ref != "" {
			args["commitSpecifier"] = ref
		}
		return call("GetFile", args)
	}
	if current := file("refs/heads/release/one", "dir/item.bin"); current["fileContent"] != "" || current["fileSize"] != json.Number("0") || current["commitId"] != updated["commitId"] {
		t.Fatal(current)
	}
	for _, ref := range []string{"", aID, "main"} {
		f := file(ref, "/dir/item.bin")
		if f["fileContent"] != first["fileContent"] || f["fileSize"] != json.Number("10") || f["filePath"] != "dir/item.bin" || f["fileMode"] != "NORMAL" {
			t.Fatal(f)
		}
	}
	if f := file("", "dir.txt"); f["fileMode"] != "EXECUTABLE" || f["fileContent"] != second["fileContent"] {
		t.Fatal(f)
	}
	if ref := call("GetBranch", map[string]any{"branchName": "main"})["branch"].(map[string]any); ref["commitId"] != bID {
		t.Fatal(ref)
	}
	// Reusing the repository name and ARN creates a new object lifetime.
	call("DeleteRepository", nil)
	call("CreateRepository", nil)
	expect(t, gitCall(h, "local", "GetCommit", map[string]any{"commitId": aID}), 400, "CommitIdDoesNotExistException")
	for _, table := range []string{"aws_codecommit_objects", "aws_codecommit_branches"} {
		var count int
		if err := h.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
}

func TestCodeCommitErrorsIsolationAndPagination(t *testing.T) {
	h := newHarness(t, codeCommit)
	for _, repo := range []string{"one", "two"} {
		expect(t, gitCall(h, repo, "CreateRepository", nil), 200, "")
	}
	put := map[string]any{"branchName": "first", "filePath": "d/file", "fileContent": "YQ=="}
	initial := gitCall(h, "one", "PutFile", put)
	expect(t, initial, 200, "")
	id := initial.body["commitId"].(string)
	missing := strings.Repeat("a", 40)
	for _, tc := range []struct {
		op, code string
		args     map[string]any
	}{
		{"PutFile", "ParentCommitIdRequiredException", put},
		{"PutFile", "InvalidParentCommitIdException", map[string]any{"parentCommitId": "bad"}},
		{"PutFile", "ParentCommitDoesNotExistException", map[string]any{"parentCommitId": missing}},
		{"PutFile", "BranchDoesNotExistException", map[string]any{"branchName": "missing", "parentCommitId": id}},
		{"PutFile", "SameFileContentException", map[string]any{"parentCommitId": id}},
		{"PutFile", "FileNameConflictsWithDirectoryNameException", map[string]any{"parentCommitId": id, "filePath": "d"}},
		{"PutFile", "DirectoryNameConflictsWithFileNameException", map[string]any{"parentCommitId": id, "filePath": "d/file/sub"}},
		{"PutFile", "InvalidPathException", map[string]any{"parentCommitId": id, "filePath": "d/../foreign"}},
		{"PutFile", "InvalidFileModeException", map[string]any{"parentCommitId": id, "fileMode": "SYMLINK"}},
		{"GetFile", "CommitDoesNotExistException", map[string]any{"commitSpecifier": missing, "filePath": "file"}},
		{"GetFile", "FileDoesNotExistException", map[string]any{"filePath": "d"}},
		{"GetCommit", "CommitIdDoesNotExistException", map[string]any{"commitId": missing}},
		{"CreateBranch", "CommitDoesNotExistException", map[string]any{"branchName": "new", "commitId": missing}},
		{"CreateBranch", "BranchNameExistsException", map[string]any{"branchName": "first", "commitId": id}},
		{"GetBranch", "BranchDoesNotExistException", map[string]any{"branchName": "missing"}},
	} {
		args := map[string]any{}
		if tc.op == "PutFile" {
			maps.Copy(args, put)
		}
		maps.Copy(args, tc.args)
		expect(t, gitCall(h, "one", tc.op, args), 400, tc.code)
	}
	expect(t, gitCall(h, "two", "GetCommit", map[string]any{"commitId": id}), 400, "CommitIdDoesNotExistException")
	arn := "arn:aws:codecommit:us-east-1:000000000000:one"
	for op, args := range map[string]map[string]any{
		"PutFile": put, "GetFile": {"filePath": "d/file"}, "GetCommit": {"commitId": id},
		"GetBranch": {"branchName": "first"}, "ListBranches": nil, "CreateBranch": {"branchName": "another", "commitId": id},
	} {
		key := "codecommit:" + op + " " + arn
		h.policy.deny[key] = true
		expect(t, gitCall(h, "one", op, args), 403, "AccessDeniedException")
		delete(h.policy.deny, key)
	}
	for n := 1; n < gitMaxBranches; n++ {
		expect(t, gitCall(h, "one", "CreateBranch", map[string]any{"commitId": id, "branchName": fmt.Sprintf("extra-%02d", n)}), 200, "")
	}
	overflow := gitCall(h, "one", "CreateBranch", map[string]any{"commitId": id, "branchName": "overflow"})
	if overflow.status < 400 {
		t.Fatal(overflow)
	}
	var all []any
	args := map[string]any{}
	var token any
	for {
		page := gitCall(h, "one", "ListBranches", args)
		expect(t, page, 200, "")
		all = append(all, page.body["branches"].([]any)...)
		if page.body["nextToken"] == nil {
			break
		}
		args["nextToken"] = page.body["nextToken"]
		token = args["nextToken"]
	}
	if len(all) != gitMaxBranches || all[0] != "first" {
		t.Fatal(all)
	}
	expect(t, gitCall(h, "two", "ListBranches", map[string]any{"nextToken": token}), 400, "InvalidContinuationTokenException")
	body := bytes.Repeat([]byte{'a'}, gitMaxFileBytes)
	put["parentCommitId"] = id
	put["fileContent"] = base64.StdEncoding.EncodeToString(append(body, 0))
	expect(t, gitCall(h, "one", "PutFile", put), 400, "FileContentSizeLimitExceededException")
	put["fileContent"] = base64.StdEncoding.EncodeToString(body)
	expect(t, gitCall(h, "one", "PutFile", put), 200, "")
	// The original parent remains valid history, but is stale on this branch.
	expect(t, gitCall(h, "one", "PutFile", put), 400, "ParentCommitIdOutdatedException")
}
