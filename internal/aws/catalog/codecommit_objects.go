package catalog

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	gitMaxFileBytes = 64 << 10
	gitMaxObjects   = 512
	gitMaxBytes     = 32 << 20
	gitMaxBranches  = 32
)

const gitSchema = `
CREATE TABLE IF NOT EXISTS aws_codecommit_objects (
 repository TEXT NOT NULL REFERENCES aws_catalog_resources(arn) ON DELETE CASCADE,
 oid TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('blob','tree','commit')),
 body BLOB NOT NULL, PRIMARY KEY(repository,oid)
);
CREATE TABLE IF NOT EXISTS aws_codecommit_branches (
 seq INTEGER PRIMARY KEY, repository TEXT NOT NULL REFERENCES aws_catalog_resources(arn) ON DELETE CASCADE,
 name TEXT NOT NULL, tip TEXT NOT NULL, UNIQUE(repository,name),
 FOREIGN KEY(repository,tip) REFERENCES aws_codecommit_objects(repository,oid)
);`

type gitStore struct {
	ctx  context.Context
	tx   *sql.Tx
	repo string
}

func gitOID(kind string, body []byte) string {
	// Git SHA-1 object format, selected for wire compatibility with CodeCommit.
	h := sha1.New()
	fmt.Fprintf(h, "%s %d\x00", kind, len(body))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func validGitOID(oid string) bool {
	_, err := hex.DecodeString(oid)
	return len(oid) == 40 && oid == strings.ToLower(oid) && err == nil
}

var errGitStorage = errors.New("stored CodeCommit object or reference is inconsistent")

func (g gitStore) object(kind, oid string) ([]byte, error) {
	var storedKind string
	var body []byte
	err := g.tx.QueryRowContext(g.ctx, "SELECT kind,body FROM aws_codecommit_objects WHERE repository=? AND oid=?", g.repo, oid).Scan(&storedKind, &body)
	if err != nil {
		return nil, err
	}
	if kind != storedKind {
		return nil, sql.ErrNoRows
	}
	if gitOID(kind, body) != oid {
		return nil, errGitStorage
	}
	return body, nil
}

func (g gitStore) save(kind string, body []byte) (string, error) {
	oid := gitOID(kind, body)
	// A content collision or malformed existing row must never be overwritten.
	current, err := g.object(kind, oid)
	if err == nil {
		if !bytes.Equal(current, body) {
			return "", errGitStorage
		}
		return oid, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if body == nil {
		body = []byte{}
	}
	if err := exactlyOne(g.tx.ExecContext(g.ctx, "INSERT INTO aws_codecommit_objects(repository,oid,kind,body) VALUES(?,?,?,?)", g.repo, oid, kind, body)); err != nil {
		return "", err
	}
	return oid, nil
}

type gitEntry struct{ name, mode, oid string }

func (e gitEntry) sortName() string {
	if e.mode == "40000" {
		return e.name + "/"
	}
	return e.name
}

func (g gitStore) tree(oid string) ([]gitEntry, error) {
	if oid == "" {
		return nil, nil
	}
	body, err := g.object("tree", oid)
	if err != nil {
		return nil, errGitStorage
	}
	var entries []gitEntry
	for len(body) > 0 {
		header, tail, ok := bytes.Cut(body, []byte{0})
		mode, name, space := strings.Cut(string(header), " ")
		if !ok || !space || len(tail) < sha1.Size || name == "" || strings.Contains(name, "/") || (mode != "100644" && mode != "100755" && mode != "40000") {
			return nil, errGitStorage
		}
		entry := gitEntry{name, mode, hex.EncodeToString(tail[:sha1.Size])}
		if len(entries) > 0 && entries[len(entries)-1].sortName() >= entry.sortName() {
			return nil, errGitStorage
		}
		entries = append(entries, entry)
		body = tail[sha1.Size:]
	}
	return entries, nil
}

func (g gitStore) putPath(root string, parts []string, mode, blob string) (string, error) {
	entries, err := g.tree(root)
	if err != nil {
		return "", err
	}
	at := len(entries)
	for i, e := range entries {
		if e.name == parts[0] {
			at = i
			break
		}
	}
	exists := at < len(entries)
	var previous gitEntry
	if exists {
		previous = entries[at]
	}
	next := gitEntry{name: parts[0], mode: mode, oid: blob}
	if len(parts) > 1 {
		if exists && previous.mode != "40000" {
			return "", Failure("DirectoryNameConflictsWithFileNameException", "A path component is already a file.")
		}
		next.mode = "40000"
		next.oid, err = g.putPath(previous.oid, parts[1:], mode, blob)
		if err != nil {
			return "", err
		}
	} else {
		if previous.mode == "40000" {
			return "", Failure("FileNameConflictsWithDirectoryNameException", "A directory already has that name.")
		}
		if next.mode == "" {
			next.mode = previous.mode
		}
		if next.mode == "" {
			next.mode = "100644"
		}
		if exists && next == previous {
			return "", Failure("SameFileContentException", "The content and file mode are unchanged.")
		}
	}
	if exists {
		entries[at] = next
	} else {
		entries = append(entries, next)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sortName() < entries[j].sortName() })
	var body bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&body, "%s %s\x00", e.mode, e.name)
		id, _ := hex.DecodeString(e.oid)
		body.Write(id)
	}
	return g.save("tree", body.Bytes())
}

func (g gitStore) file(root string, parts []string) (gitEntry, error) {
	for i, name := range parts {
		entries, err := g.tree(root)
		if err != nil {
			return gitEntry{}, err
		}
		found := false
		for _, entry := range entries {
			if entry.name != name {
				continue
			}
			if i == len(parts)-1 && entry.mode != "40000" {
				return entry, nil
			}
			if i < len(parts)-1 && entry.mode == "40000" {
				root = entry.oid
				found = true
			}
			break
		}
		if !found {
			break
		}
	}
	return gitEntry{}, Failure("FileDoesNotExistException", "The specified commit contains no file at that path.")
}

type gitCommit struct{ tree, parent, name, email, date, message string }

func (c gitCommit) body() []byte {
	var b strings.Builder
	fmt.Fprintln(&b, "tree", c.tree)
	if c.parent != "" {
		fmt.Fprintln(&b, "parent", c.parent)
	}
	ident := c.name + " <" + c.email + "> " + c.date
	fmt.Fprintf(&b, "author %s\ncommitter %s\n\n%s", ident, ident, c.message)
	return []byte(b.String())
}

func (g gitStore) commit(oid, missing string) (gitCommit, error) {
	var c gitCommit
	body, err := g.object("commit", oid)
	if errors.Is(err, sql.ErrNoRows) {
		return c, Failure(missing, "The specified commit does not exist in this repository.")
	}
	if err != nil {
		return c, err
	}
	headers, msg, ok := strings.Cut(string(body), "\n\n")
	lines := strings.Split(headers, "\n")
	if !ok || (len(lines) != 3 && len(lines) != 4) {
		return c, errGitStorage
	}
	c.tree, _ = strings.CutPrefix(lines[0], "tree ")
	if !validGitOID(c.tree) {
		return c, errGitStorage
	}
	if len(lines) == 4 {
		c.parent, _ = strings.CutPrefix(lines[1], "parent ")
		if !validGitOID(c.parent) {
			return c, errGitStorage
		}
	}
	identity, authorOK := strings.CutPrefix(lines[len(lines)-2], "author ")
	var tail string
	c.name, tail, ok = strings.Cut(identity, " <")
	if !authorOK || !ok {
		return c, errGitStorage
	}
	c.email, c.date, ok = strings.Cut(tail, "> ")
	if !ok {
		return c, errGitStorage
	}
	c.message = msg
	if !bytes.Equal(c.body(), body) {
		return c, errGitStorage
	}
	return c, nil
}

func (c gitCommit) output(id string) map[string]any {
	parents := []string{}
	if c.parent != "" {
		parents = append(parents, c.parent)
	}
	user := map[string]string{"name": c.name, "email": c.email, "date": c.date}
	return map[string]any{"commitId": id, "treeId": c.tree, "parents": parents, "author": user, "committer": user, "message": c.message}
}

func (g gitStore) checkBudget() error {
	var count, total int64
	if err := g.tx.QueryRowContext(g.ctx, "SELECT count(*) FROM aws_codecommit_objects WHERE repository=?", g.repo).Scan(&count); err != nil {
		return err
	}
	if err := g.tx.QueryRowContext(g.ctx, "SELECT coalesce(sum(length(body)),0) FROM aws_codecommit_objects").Scan(&total); err != nil {
		return err
	}
	if count > gitMaxObjects || total > gitMaxBytes {
		return &fault{kind: "limit", message: "The local Git object budget is exhausted."}
	}
	return nil
}

func gitDate(unix int64) string { return strconv.FormatInt(unix, 10) + " +0000" }
