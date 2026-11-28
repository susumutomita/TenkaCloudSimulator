package catalog

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// These operations model field-specific exceptions for omitted inputs. An
// explicitly supplied empty blob remains valid Git content.
var gitRequiredErrors = map[string]string{
	"repositoryName": "RepositoryNameRequiredException",
	"branchName":     "BranchNameRequiredException",
	"commitId":       "CommitIdRequiredException",
	"filePath":       "PathRequiredException",
	"fileContent":    "FileContentRequiredException",
}

// The official decoder has already checked concrete wire types. An omitted
// optional string is absent from the input map and reads as its zero value.
func gitString(value any) string {
	text, _ := value.(string)
	return text
}

func gitPath(path string) ([]string, error) {
	if path == "" {
		return nil, Failure("PathRequiredException", "A file path is required.")
	}
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")
	if len(path) > 256 || len(parts) > 8 || strings.ContainsRune(path, '\\') || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return nil, Failure("InvalidPathException", "Provide a repository path of at most 256 bytes and eight components.")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return nil, Failure("InvalidPathException", "The file path has an invalid component.")
		}
	}
	return parts, nil
}

func validGitBranch(name string) bool {
	if name == "" || name == "@" || name == "HEAD" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "refs/") || validGitOID(name) || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, " ~^:?*[\\") || len(name) > 256 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func gitBranchName(name string) error {
	if name == "" {
		return Failure("BranchNameRequiredException", "A branch name is required.")
	}
	if !validGitBranch(name) {
		return Failure("InvalidBranchNameException", "The branch name is invalid.")
	}
	return nil
}

func (g gitStore) tip(name string) (string, error) {
	var id string
	err := g.tx.QueryRowContext(g.ctx, "SELECT tip FROM aws_codecommit_branches WHERE repository=? AND name=?", g.repo, name).Scan(&id)
	return id, err
}

func (g gitStore) createRef(name, id string) error {
	var count, conflicts int
	// Git disallows a ref and its directory prefix coexisting, such as
	// refs/heads/release and refs/heads/release/v1. Compare literal names.
	if err := g.tx.QueryRowContext(g.ctx, `SELECT count(*),
		count(CASE WHEN name=? OR instr(name,? || '/')=1 OR instr(?,name || '/')=1 THEN 1 END)
		FROM aws_codecommit_branches WHERE repository=?`, name, name, name, g.repo).Scan(&count, &conflicts); err != nil {
		return err
	}
	if conflicts != 0 {
		return Failure("BranchNameExistsException", "The branch name conflicts with an existing branch.")
	}
	if count >= gitMaxBranches {
		return &fault{kind: "limit", message: "The local repository supports at most 32 branches."}
	}
	return exactlyOne(g.tx.ExecContext(g.ctx, "INSERT INTO aws_codecommit_branches(repository,name,tip) VALUES(?,?,?)", g.repo, name, id))
}

func (q *request) codeCommitContent() (map[string]any, error) {
	name := gitString(q.input["repositoryName"])
	if name == "" {
		return nil, Failure("RepositoryNameRequiredException", "A repository name is required.")
	}
	arn := q.formatARN(q.resource(), "", name, nil)
	// The policy evaluator uses the provider database, so capture the repository
	// lifetime outside the transaction. Never reveal the lookup outcome before
	// authorization, and never adopt a replacement created while it was running.
	var repositoryID string
	var sequence int64
	observed := q.e.store.db.QueryRowContext(q.ctx,
		"SELECT id,seq FROM aws_catalog_resources WHERE service='codecommit' AND arn=?", arn).Scan(&repositoryID, &sequence)
	if err := q.authorize(arn); err != nil {
		return nil, err
	}
	if errors.Is(observed, sql.ErrNoRows) {
		return nil, Failure("RepositoryDoesNotExistException", "The repository does not exist.")
	}
	if observed != nil {
		return nil, observed
	}
	tx, err := q.e.store.db.BeginTx(q.ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := findARN(tx, "codecommit", arn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Failure("RepositoryDoesNotExistException", "The repository does not exist.")
	}
	if err != nil {
		return nil, err
	}
	if r.ID != repositoryID || r.Seq != sequence {
		return nil, Failure("RepositoryDoesNotExistException", "The original repository no longer exists.")
	}
	if r.Region != q.region || r.Kind != "repository" || r.Name != name {
		return nil, errGitStorage
	}
	if gitString(r.Document["kmsKeyId"]) != "" {
		return nil, &fault{kind: "unsupported", message: "Git content is unavailable for a customer-key repository."}
	}
	g := gitStore{q.ctx, tx, arn}
	var result map[string]any
	switch q.op.Name {
	case "PutFile":
		result, err = q.gitPut(g, r)
	case "CreateBranch":
		err = q.gitCreateBranch(g)
	case "ListBranches":
		result, err = q.gitBranches(g, r)
	case "GetBranch":
		branch := gitString(q.input["branchName"])
		if err = gitBranchName(branch); err != nil {
			return nil, err
		}
		var id string
		id, err = g.tip(branch)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Failure("BranchDoesNotExistException", "The branch does not exist.")
		}
		result = map[string]any{"branch": map[string]any{"branchName": branch, "commitId": id}}
	case "GetFile":
		result, err = q.gitGetFile(g, r)
	case "GetCommit":
		id := gitString(q.input["commitId"])
		if err = gitCommitID(id); err != nil {
			return nil, err
		}
		var c gitCommit
		c, err = g.commit(id, "CommitIdDoesNotExistException")
		result = map[string]any{"commit": c.output(id)}
	default:
		return nil, errGitStorage
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func gitCommitID(id string) error {
	if id == "" {
		return Failure("CommitIdRequiredException", "A commit ID is required.")
	}
	if !validGitOID(id) {
		return Failure("InvalidCommitIdException", "A full 40-character lowercase commit ID is required.")
	}
	return nil
}

func (q *request) gitCreateBranch(g gitStore) error {
	name, id := gitString(q.input["branchName"]), gitString(q.input["commitId"])
	if err := gitBranchName(name); err != nil {
		return err
	}
	if err := gitCommitID(id); err != nil {
		return err
	}
	if _, err := g.commit(id, "CommitDoesNotExistException"); err != nil {
		return err
	}
	return g.createRef(name, id)
}

func (q *request) gitBranches(g gitStore, r row) (map[string]any, error) {
	const size = 10
	scope := r.ID + "\x00" + q.principal
	after := int64(0)
	if token, present := q.input["nextToken"]; present {
		var valid bool
		after, valid = pageAfter(gitString(token), q.op.Name, q.region, scope)
		if !valid || after < 1 {
			return nil, Failure("InvalidContinuationTokenException", "The branch continuation is invalid.")
		}
		var exists int
		if err := g.tx.QueryRowContext(g.ctx, "SELECT count(*) FROM aws_codecommit_branches WHERE repository=? AND seq=?", g.repo, after).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 1 {
			return nil, Failure("InvalidContinuationTokenException", "The branch continuation is no longer valid.")
		}
	}
	rows, err := g.tx.QueryContext(g.ctx, "SELECT seq,name FROM aws_codecommit_branches WHERE repository=? AND seq>? ORDER BY seq LIMIT ?", g.repo, after, size+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	var last int64
	out := map[string]any{}
	for rows.Next() {
		var seq int64
		var name string
		if err := rows.Scan(&seq, &name); err != nil {
			return nil, err
		}
		if len(names) == size {
			out["nextToken"] = pageToken(q.op.Name, q.region, scope, last)
			break
		}
		names = append(names, name)
		last = seq
	}
	out["branches"] = names
	return out, rows.Err()
}

func (q *request) gitGetFile(g gitStore, r row) (map[string]any, error) {
	parts, err := gitPath(gitString(q.input["filePath"]))
	if err != nil {
		return nil, err
	}
	spec, provided := q.input["commitSpecifier"]
	ref := gitString(spec)
	if !provided {
		ref = gitString(r.Document["defaultBranch"])
	}
	id := ref
	if !validGitOID(ref) {
		branch := strings.TrimPrefix(ref, "refs/heads/")
		if ref == "" && !provided {
			return nil, Failure("CommitDoesNotExistException", "The repository is empty.")
		}
		if !validGitBranch(branch) {
			return nil, Failure("InvalidCommitException", "The commit specifier is invalid.")
		}
		id, err = g.tip(branch)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Failure("CommitDoesNotExistException", "The branch or commit does not exist.")
		}
		if err != nil {
			return nil, err
		}
	}
	c, err := g.commit(id, "CommitDoesNotExistException")
	if err != nil {
		return nil, err
	}
	entry, err := g.file(c.tree, parts)
	if err != nil {
		return nil, err
	}
	body, err := g.object("blob", entry.oid)
	if err != nil {
		return nil, errGitStorage
	}
	mode := "NORMAL"
	if entry.mode == "100755" {
		mode = "EXECUTABLE"
	}
	return map[string]any{"commitId": id, "blobId": entry.oid, "filePath": strings.Join(parts, "/"), "fileMode": mode, "fileSize": len(body), "fileContent": base64.StdEncoding.EncodeToString(body)}, nil
}

func (q *request) gitPut(g gitStore, r row) (map[string]any, error) {
	branch := gitString(q.input["branchName"])
	if err := gitBranchName(branch); err != nil {
		return nil, err
	}
	parts, err := gitPath(gitString(q.input["filePath"]))
	if err != nil {
		return nil, err
	}
	content, ok := q.input["fileContent"].([]byte)
	if !ok {
		return nil, Failure("FileContentRequiredException", "File content is required.")
	}
	if len(content) > gitMaxFileBytes {
		return nil, Failure("FileContentSizeLimitExceededException", "Local file content is limited to 64 KiB.")
	}
	mode := ""
	if raw, present := q.input["fileMode"]; present {
		switch raw {
		case "NORMAL":
			mode = "100644"
		case "EXECUTABLE":
			mode = "100755"
		default:
			return nil, Failure("InvalidFileModeException", "Only regular and executable files are supported.")
		}
	}
	c := gitCommit{name: "Local contributor", email: "local@simulator.invalid", date: gitDate(q.now().Unix()), message: gitString(q.input["commitMessage"])}
	if value, present := q.input["name"]; present {
		c.name = gitString(value)
	}
	if value, present := q.input["email"]; present {
		c.email = gitString(value)
	}
	if utf8.RuneCountInString(c.name) > 256 || strings.TrimSpace(c.name) == "" || strings.ContainsAny(c.name, "<>") || strings.IndexFunc(c.name, unicode.IsControl) >= 0 {
		return nil, Failure("NameLengthExceededException", "Provide a nonempty Git author name of at most 256 characters without header delimiters.")
	}
	if len(c.email) > 256 || !strings.Contains(c.email, "@") || strings.ContainsAny(c.email, "<> ") || strings.IndexFunc(c.email, unicode.IsControl) >= 0 {
		return nil, Failure("InvalidEmailException", "Provide a bounded Git author email without header delimiters.")
	}
	if len(c.message) > 4096 || strings.ContainsRune(c.message, 0) {
		return nil, Failure("CommitMessageLengthExceededException", "The commit message is limited to 4096 bytes without NUL.")
	}
	previous, err := g.tip(branch)
	first := errors.Is(err, sql.ErrNoRows)
	if err != nil && !first {
		return nil, err
	}
	if first && gitString(r.Document["defaultBranch"]) != "" {
		return nil, Failure("BranchDoesNotExistException", "The branch does not exist.")
	}
	id, present := q.input["parentCommitId"]
	base := ""
	if present {
		c.parent = gitString(id)
		if !validGitOID(c.parent) {
			return nil, Failure("InvalidParentCommitIdException", "A full 40-character lowercase parent commit ID is required.")
		}
		parent, err := g.commit(c.parent, "ParentCommitDoesNotExistException")
		if err != nil {
			return nil, err
		}
		if previous != c.parent {
			return nil, Failure("ParentCommitIdOutdatedException", "The supplied parent is not the current branch tip.")
		}
		base = parent.tree
	} else if !first {
		return nil, Failure("ParentCommitIdRequiredException", "Supply the current branch tip as parentCommitId.")
	}
	blob, err := g.save("blob", content)
	if err != nil {
		return nil, err
	}
	c.tree, err = g.putPath(base, parts, mode, blob)
	if err != nil {
		return nil, err
	}
	commit, err := g.save("commit", c.body())
	if err != nil {
		return nil, err
	}
	if first {
		err = g.createRef(branch, commit)
		r.Document["defaultBranch"] = branch
	} else {
		err = exactlyOne(g.tx.ExecContext(g.ctx, "UPDATE aws_codecommit_branches SET tip=? WHERE repository=? AND name=? AND tip=?", commit, g.repo, branch, previous))
	}
	if err != nil {
		return nil, err
	}
	r.Document["lastModifiedDate"] = q.now().Format("2006-01-02T15:04:05.000Z")
	if err = updateRow(g.tx, r); err != nil {
		return nil, err
	}
	if err = g.checkBudget(); err != nil {
		return nil, err
	}
	return map[string]any{"commitId": commit, "treeId": c.tree, "blobId": blob}, nil
}
