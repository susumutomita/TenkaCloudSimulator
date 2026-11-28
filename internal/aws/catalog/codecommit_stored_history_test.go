package catalog

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// Stored objects can have a correct Git SHA-1 envelope and still contain an
// unsupported tree or a malformed commit. Exercise the actual persisted bytes.
func TestCodeCommitMalformedStoredHistory(t *testing.T) {
	for _, fault := range []string{"commit-separator", "tree-id", "parent-id", "author-header", "author-email", "committer", "tree-mode", "tree-order"} {
		t.Run(fault, func(t *testing.T) {
			ownerPath := filepath.Join(t.TempDir(), "owner.db")
			host, owner := openGitTeam(t, ownerPath)
			peerHost, peer := openGitTeam(t, filepath.Join(t.TempDir(), "peer.db"))
			put := map[string]any{"branchName": "main", "filePath": "original", "fileContent": "YQ=="}
			var arn, healthy, tree, blob string
			for _, team := range []*harness{owner, peer} {
				repo := gitCall(team, "project", "CreateRepository", nil)
				expect(t, repo, 200, "")
				created := gitCall(team, "project", "PutFile", put)
				expect(t, created, 200, "")
				if team == owner {
					arn = repo.body["repositoryMetadata"].(map[string]any)["Arn"].(string)
					healthy = created.body["commitId"].(string)
					tree = created.body["treeId"].(string)
					blob = created.body["blobId"].(string)
				}
			}
			expect(t, gitCall(owner, "sibling", "CreateRepository", nil), 200, "")
			expect(t, gitCall(owner, "sibling", "PutFile", put), 200, "")
			before := codeCommitSnapshot(t, owner.db)
			peerBefore := codeCommitSnapshot(t, peer.db)

			var fixtures []string
			insertObject := func(kind string, body []byte) string {
				t.Helper()
				digest := sha1.New()
				fmt.Fprintf(digest, "%s %d\x00", kind, len(body))
				digest.Write(body)
				id := hex.EncodeToString(digest.Sum(nil))
				if _, err := owner.db.Exec("INSERT INTO aws_codecommit_objects(repository,oid,kind,body) VALUES(?,?,?,?)", arn, id, kind, body); err != nil {
					t.Fatal(err)
				}
				fixtures = append(fixtures, id)
				return id
			}
			identity := "local <local@example.invalid> 1700000000 +0000"
			treeFault := fault == "tree-mode" || fault == "tree-order"
			if treeFault {
				oid, err := hex.DecodeString(blob)
				if err != nil {
					t.Fatal(err)
				}
				var entries bytes.Buffer
				if fault == "tree-mode" {
					entries.WriteString("160000 link\x00")
					entries.Write(oid)
				} else {
					for _, name := range []string{"z", "a"} {
						fmt.Fprintf(&entries, "100644 %s\x00", name)
						entries.Write(oid)
					}
				}
				tree = insertObject("tree", entries.Bytes())
			}
			header := "tree " + tree + "\nparent " + healthy + "\nauthor " + identity + "\ncommitter " + identity + "\n\nheld\n"
			switch fault {
			case "commit-separator":
				header = strings.Replace(header, "\n\n", "\n", 1)
			case "tree-id":
				header = strings.Replace(header, "tree "+tree, "tree incomplete", 1)
			case "parent-id":
				header = strings.Replace(header, "parent "+healthy, "parent incomplete", 1)
			case "author-header":
				header = strings.Replace(header, "author ", "writer ", 1)
			case "author-email":
				header = strings.Replace(header, "example.invalid> ", "example.invalid ", 1)
			case "committer":
				header = strings.Replace(header, "committer local", "committer other", 1)
			}
			broken := insertObject("commit", []byte(header))
			setTip := func(previous, next string) {
				t.Helper()
				if err := exactlyOne(owner.db.Exec("UPDATE aws_codecommit_branches SET tip=? WHERE repository=? AND name='main' AND tip=?", next, arn, previous)); err != nil {
					t.Fatal(err)
				}
			}
			setTip(healthy, broken)
			damaged := codeCommitSnapshot(t, owner.db)

			// This temporary trigger observes the actual candidate blob before the
			// tree traversal fails. The observation lives outside the transaction.
			staged := 0
			conn, err := owner.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = conn.Raw(func(raw any) error {
				return raw.(sqlitedriver.Conn).Raw().CreateFunction("observe_git_blob", 4, 0, func(ctx sqlite3.Context, args ...sqlite3.Value) {
					if args[0].Text() == arn && args[1].Text() == "blob" && args[2].Text() == "ba01f6b05bdbb386b35f4d086e268c5d422cafb9" && bytes.Equal(args[3].Blob(nil), []byte{0, 255}) {
						staged++
					}
					ctx.ResultNull()
				})
			})
			if err == nil {
				_, err = conn.ExecContext(t.Context(), `CREATE TEMP TRIGGER observe_git_blob_insert AFTER INSERT ON aws_codecommit_objects
BEGIN SELECT observe_git_blob(NEW.repository,NEW.kind,NEW.oid,NEW.body); END`)
			}
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			refuse := func(op string, input map[string]any) {
				t.Helper()
				r := gitCall(owner, "project", op, input)
				if r.status != 500 {
					t.Fatalf("%s returned %d", op, r.status)
				}
				for _, key := range []string{"commit", "fileContent", "blobId", "treeId", "commitId", "branch"} {
					if _, ok := r.body[key]; ok {
						t.Fatalf("%s exposed %s", op, key)
					}
				}
				if !slices.Equal(damaged, codeCommitSnapshot(t, owner.db)) || !slices.Equal(peerBefore, codeCommitSnapshot(t, peer.db)) {
					t.Fatalf("%s changed durable state", op)
				}
			}
			if !treeFault {
				refuse("GetCommit", map[string]any{"commitId": broken})
				refuse("CreateBranch", map[string]any{"branchName": "unpublished", "commitId": broken})
			}
			refuse("GetFile", map[string]any{"filePath": "original"})
			change := map[string]any{"branchName": "main", "filePath": "new", "fileContent": "AP8=", "parentCommitId": broken}
			refuse("PutFile", change)
			if treeFault && staged != 1 || !treeFault && staged != 0 {
				t.Fatalf("observed %d staged blobs", staged)
			}

			if err := host.Close(); err != nil {
				t.Fatal(err)
			}
			_, owner = openGitTeam(t, ownerPath)
			if !slices.Equal(damaged, codeCommitSnapshot(t, owner.db)) {
				t.Fatal("damaged rows changed on reopen")
			}
			historical := gitCall(owner, "project", "GetFile", map[string]any{"commitSpecifier": healthy, "filePath": "original"})
			expect(t, historical, 200, "")
			if historical.body["fileContent"] != "YQ==" || historical.body["blobId"] != blob {
				t.Fatal(historical)
			}
			setTip(broken, healthy)
			for _, id := range fixtures {
				if err := exactlyOne(owner.db.Exec("DELETE FROM aws_codecommit_objects WHERE repository=? AND oid=?", arn, id)); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(before, codeCommitSnapshot(t, owner.db)) {
				t.Fatal("fixture repair changed other stored state")
			}
			change["parentCommitId"] = healthy
			retry := gitCall(owner, "project", "PutFile", change)
			expect(t, retry, 200, "")
			saved := gitCall(owner, "project", "GetFile", map[string]any{"commitSpecifier": retry.body["commitId"], "filePath": "new"})
			expect(t, saved, 200, "")
			if saved.body["fileContent"] != "AP8=" || saved.body["blobId"] != retry.body["blobId"] || !slices.Equal(peerBefore, codeCommitSnapshot(t, peerHost.Database())) {
				t.Fatal("invalid saved retry or peer state")
			}
		})
	}
}
