package appengine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
)

type serviceRecord struct {
	ID, ShardBy string
	Allocations map[string]int // thousandths
}

func validServiceID(id string) bool {
	return validID.MatchString(id) && !strings.Contains(id, "-dot-")
}

// Version IDs follow the official rule: lowercase letters, digits and
// hyphens; "default", "latest" and the "ah-" prefix are reserved.
func validVersionID(id string) bool {
	return validServiceID(id) && id != "default" && id != "latest" && !strings.HasPrefix(id, "ah-")
}

func loadService(tx *sql.Tx, project, id string) (serviceRecord, error) {
	s := serviceRecord{ID: id, Allocations: map[string]int{}}
	err := tx.QueryRow("SELECT shard_by FROM gcp_appengine_services WHERE project = ? AND id = ?", project, id).Scan(&s.ShardBy)
	if errors.Is(err, sql.ErrNoRows) {
		return s, notFound("Service " + id + " was not found in apps/" + project + ".")
	}
	if err != nil {
		return s, err
	}
	rows, err := tx.Query("SELECT version, share FROM gcp_appengine_allocations WHERE project = ? AND service = ?", project, id)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var version string
		var share int
		if err := rows.Scan(&version, &share); err != nil {
			return s, err
		}
		s.Allocations[version] = share
	}
	return s, rows.Err()
}

func (e *Engine) serviceResource(s serviceRecord) map[string]any {
	allocations := map[string]any{}
	for v, share := range s.Allocations {
		allocations[v] = float64(share) / allocationUnits
	}
	split := map[string]any{"allocations": allocations}
	if s.ShardBy != "" {
		split["shardBy"] = s.ShardBy
	}
	return map[string]any{"name": "apps/" + e.cfg.Project + "/services/" + s.ID, "id": s.ID, "split": split}
}

func (e *Engine) listServices(q *request) (any, error) {
	size, after, err := pageParams(q)
	if err != nil {
		return nil, err
	}
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		ids, err := strings1(tx, "SELECT id FROM gcp_appengine_services WHERE project = ?", e.cfg.Project)
		if err != nil {
			return nil, err
		}
		items, token := page(ids, func(id string) string { return id }, size, after)
		out := []any{}
		for _, id := range items {
			s, err := loadService(tx, e.cfg.Project, id)
			if err != nil {
				return nil, err
			}
			out = append(out, e.serviceResource(s))
		}
		return listResponse("services", out, token), nil
	})
}

func strings1(tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (e *Engine) getService(q *request) (any, error) {
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		s, err := loadService(tx, e.cfg.Project, q.path["servicesId"])
		if err != nil {
			return nil, err
		}
		return e.serviceResource(s), nil
	})
}

// parseSplit converts official fractional allocations to thousandths. IP
// splits take at most two decimal places and cookie or random splits three,
// and the allocations must add up to exactly 1.
func parseSplit(raw json.RawMessage) (string, map[string]int, error) {
	var split map[string]json.RawMessage
	if err := json.Unmarshal(raw, &split); err != nil || split == nil {
		return "", nil, invalid("split must be a TrafficSplit object.")
	}
	shardBy := ""
	var allocations map[string]float64
	for name, value := range split {
		switch name {
		case "shardBy":
			if err := json.Unmarshal(value, &shardBy); err != nil {
				return "", nil, invalid("split.shardBy must be a string.")
			}
		case "allocations":
			if err := json.Unmarshal(value, &allocations); err != nil {
				return "", nil, invalid("split.allocations must map version IDs to numbers.")
			}
		default:
			return "", nil, invalid("Unknown field split." + name + ".")
		}
	}
	switch shardBy {
	case "UNSPECIFIED":
		shardBy = ""
	case "", "IP", "COOKIE", "RANDOM":
	default:
		return "", nil, invalid("split.shardBy must be UNSPECIFIED, IP, COOKIE or RANDOM.")
	}
	if len(allocations) == 0 {
		return "", nil, invalid("split.allocations must name at least one version.")
	}
	step := 1
	if shardBy == "" || shardBy == "IP" {
		step = 10
	}
	out, total := map[string]int{}, 0
	for version, fraction := range allocations {
		units := math.Round(fraction * allocationUnits)
		if fraction <= 0 || fraction > 1 || math.Abs(units-fraction*allocationUnits) > 1e-6 || int(units)%step != 0 {
			if step == 10 {
				return "", nil, invalid("Each allocation must be in (0, 1] with at most two decimal places for IP splits.")
			}
			return "", nil, invalid("Each allocation must be in (0, 1] with at most three decimal places.")
		}
		out[version] = int(units)
		total += int(units)
	}
	if total != allocationUnits {
		return "", nil, invalid("split.allocations must add up to 1.")
	}
	return shardBy, out, nil
}

func (e *Engine) patchService(q *request) (any, error) {
	if q.param("migrateTraffic") == "true" {
		return nil, invalid("Gradual traffic migration (migrateTraffic=true) is not supported by the local App Engine simulator; traffic moves immediately.")
	}
	paths, err := mask(q, "split")
	if err != nil {
		return nil, err
	}
	body, err := e.patchBody(q.body, "Service", paths)
	if err != nil {
		return nil, err
	}
	raw, ok := body["split"]
	if !ok {
		return nil, invalid("The update mask names split, but the request has no split.")
	}
	shardBy, allocations, err := parseSplit(raw)
	if err != nil {
		return nil, err
	}
	id := q.path["servicesId"]
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		s, err := loadService(tx, e.cfg.Project, id)
		if err != nil {
			return nil, err
		}
		for version := range allocations {
			var n int
			if err := tx.QueryRow("SELECT count(*) FROM gcp_appengine_versions WHERE project = ? AND service = ? AND id = ?", e.cfg.Project, id, version).Scan(&n); err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, invalid("Version " + version + " does not exist in service " + id + ".")
			}
		}
		if _, err := tx.Exec("DELETE FROM gcp_appengine_allocations WHERE project = ? AND service = ?", e.cfg.Project, id); err != nil {
			return nil, err
		}
		for version, share := range allocations {
			if _, err := tx.Exec("INSERT INTO gcp_appengine_allocations(project, service, version, share) VALUES(?, ?, ?, ?)", e.cfg.Project, id, version, share); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec("UPDATE gcp_appengine_services SET shard_by = ? WHERE project = ? AND id = ?", shardBy, e.cfg.Project, id); err != nil {
			return nil, err
		}
		s.ShardBy, s.Allocations = shardBy, allocations
		return e.operation(tx, q.subject, "Services.UpdateService", "apps/"+e.cfg.Project+"/services/"+id, typed("appengine.v1.Service", e.serviceResource(s)))
	})
}

func (e *Engine) deleteService(q *request) (any, error) {
	id := q.path["servicesId"]
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		if _, err := loadService(tx, e.cfg.Project, id); err != nil {
			return nil, err
		}
		if id == "default" {
			return nil, failed("The default service cannot be deleted. Delete other services, or deploy a new default version instead.")
		}
		// Children first: allocations, then versions, then the service.
		for _, statement := range []string{
			"DELETE FROM gcp_appengine_allocations WHERE project = ? AND service = ?",
			"DELETE FROM gcp_appengine_versions WHERE project = ? AND service = ?",
			"DELETE FROM gcp_appengine_services WHERE project = ? AND id = ?",
		} {
			if _, err := tx.Exec(statement, e.cfg.Project, id); err != nil {
				return nil, err
			}
		}
		return e.operation(tx, q.subject, "Services.DeleteService", "apps/"+e.cfg.Project+"/services/"+id, typed("protobuf.Empty", nil))
	})
}

type version struct {
	Service, ID, Runtime, ServingStatus, Created, CreatedBy string
	Environment                                             map[string]string
	Deployment                                              json.RawMessage
	Code                                                    []byte
}

const versionColumns = "service, id, runtime, serving_status, env_variables, deployment, code, created, created_by"

func scanVersion(row interface{ Scan(...any) error }) (version, error) {
	var v version
	var env, deployment string
	if err := row.Scan(&v.Service, &v.ID, &v.Runtime, &v.ServingStatus, &env, &deployment, &v.Code, &v.Created, &v.CreatedBy); err != nil {
		return v, err
	}
	v.Deployment = json.RawMessage(deployment)
	return v, json.Unmarshal([]byte(env), &v.Environment)
}

func loadVersion(tx *sql.Tx, project, service, id string) (version, error) {
	v, err := scanVersion(tx.QueryRow("SELECT "+versionColumns+" FROM gcp_appengine_versions WHERE project = ? AND service = ? AND id = ?", project, service, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, notFound("Version " + id + " was not found in apps/" + project + "/services/" + service + ".")
	}
	return v, err
}

func (e *Engine) versionURL(location, service, id string) string {
	l, _ := findLocation(location)
	return "https://" + id + "-dot-" + service + "-dot-" + e.cfg.Project + "." + l.RegionID + ".r.appspot.com"
}

func (e *Engine) versionResource(a application, v version, full bool) map[string]any {
	out := map[string]any{"name": "apps/" + e.cfg.Project + "/services/" + v.Service + "/versions/" + v.ID, "id": v.ID, "runtime": v.Runtime,
		"env": "standard", "servingStatus": v.ServingStatus, "createTime": v.Created, "createdBy": v.CreatedBy, "versionUrl": e.versionURL(a.Location, v.Service, v.ID)}
	if full {
		out["deployment"] = v.Deployment
		env := map[string]any{}
		for k, value := range v.Environment {
			env[k] = value
		}
		out["envVariables"] = env
	}
	return out
}

func viewParam(q *request) (bool, error) {
	switch q.param("view") {
	case "", "BASIC":
		return false, nil
	case "FULL":
		return true, nil
	}
	return false, invalid("view must be BASIC or FULL.")
}

func (e *Engine) listVersions(q *request) (any, error) {
	full, err := viewParam(q)
	if err != nil {
		return nil, err
	}
	size, after, err := pageParams(q)
	if err != nil {
		return nil, err
	}
	service := q.path["servicesId"]
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		a, err := loadApplication(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		if _, err := loadService(tx, e.cfg.Project, service); err != nil {
			return nil, err
		}
		rows, err := tx.Query("SELECT "+versionColumns+" FROM gcp_appengine_versions WHERE project = ? AND service = ? AND id > ? ORDER BY id LIMIT ?", e.cfg.Project, service, after, size+1)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out, last := []any{}, ""
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return nil, err
			}
			if len(out) == size {
				return listResponse("versions", out, pageToken(last)), nil
			}
			out, last = append(out, e.versionResource(a, v, full)), v.ID
		}
		return listResponse("versions", out, ""), rows.Err()
	})
}

func (e *Engine) getVersion(q *request) (any, error) {
	full, err := viewParam(q)
	if err != nil {
		return nil, err
	}
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		a, err := loadApplication(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		if _, err := loadService(tx, e.cfg.Project, q.path["servicesId"]); err != nil {
			return nil, err
		}
		v, err := loadVersion(tx, e.cfg.Project, q.path["servicesId"], q.path["versionsId"])
		if err != nil {
			return nil, err
		}
		return e.versionResource(a, v, full), nil
	})
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// reservedEnvironment names are set by the runtime, as App Engine sets them.
func reservedEnvironment(name string) bool {
	return strings.HasPrefix(name, "GAE_") || strings.HasPrefix(name, "X_GOOGLE_") || name == "GOOGLE_CLOUD_PROJECT" || name == "PORT"
}

func parseEnvironment(raw json.RawMessage) (map[string]string, error) {
	env := map[string]string{}
	if raw == nil {
		return env, nil
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, invalid("envVariables must map names to strings.")
	}
	size := 0
	for name, value := range env {
		if !envName.MatchString(name) || reservedEnvironment(name) || strings.ContainsRune(value, 0) {
			return nil, invalid("Environment variable " + name + " is invalid or reserved (GAE_*, X_GOOGLE_*, GOOGLE_CLOUD_PROJECT and PORT are set by the runtime).")
		}
		size += len(name) + len(value)
	}
	if len(env) > maxEnvironmentVars || size > 4096 {
		return nil, invalid("envVariables is limited locally to 32 variables and 4 KiB.")
	}
	return env, nil
}

func (e *Engine) createVersion(q *request) (any, error) {
	service := q.path["servicesId"]
	if !validServiceID(service) {
		return nil, invalid("Service IDs use lowercase letters, digits and inner hyphens, at most 63 characters, without -dot-.")
	}
	body, err := e.fields(q.body, "Version", "id", "runtime", "env", "servingStatus", "deployment", "envVariables")
	if err != nil {
		return nil, err
	}
	text := map[string]string{}
	for _, name := range []string{"id", "runtime", "env", "servingStatus"} {
		if raw, ok := body[name]; ok {
			if text[name], err = decodeString(raw, name); err != nil {
				return nil, err
			}
		}
	}
	id := text["id"]
	if !validVersionID(id) {
		return nil, invalid("Version id is required: lowercase letters, digits and inner hyphens, at most 63 characters, without -dot-; default, latest and ah- are reserved.")
	}
	if text["runtime"] != "python312" {
		return nil, invalid("runtime must be python312; the local App Engine simulator runs Python 3.12 standard-environment versions only.")
	}
	if env := text["env"]; env != "" && env != "standard" {
		return nil, invalid("Only the standard environment is supported; env must be standard.")
	}
	servingStatus := text["servingStatus"]
	if servingStatus == "" {
		servingStatus = "SERVING"
	}
	if servingStatus != "SERVING" && servingStatus != "STOPPED" {
		return nil, invalid("servingStatus must be SERVING or STOPPED.")
	}
	environment, err := parseEnvironment(body["envVariables"])
	if err != nil {
		return nil, err
	}
	raw, ok := body["deployment"]
	if !ok {
		return nil, invalid("deployment is required: a zip or files manifest of Cloud Storage objects.")
	}
	code, deployment, err := e.deploy(q.ctx, raw)
	if err != nil {
		return nil, err
	}
	envJSON, _ := json.Marshal(environment)
	out, err := e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		a, err := loadApplication(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		s, err := loadService(tx, e.cfg.Project, service)
		created := false
		if isNotFound(err) {
			var n int
			if err := tx.QueryRow("SELECT count(*) FROM gcp_appengine_services WHERE project = ?", e.cfg.Project).Scan(&n); err != nil {
				return nil, err
			}
			if service != "default" {
				if _, err := loadService(tx, e.cfg.Project, "default"); err != nil {
					if isNotFound(err) {
						return nil, failed("The first service of an application must be the default service; deploy a default version first.")
					}
					return nil, err
				}
			}
			if n >= maxServices {
				return nil, exhausted("The local App Engine simulator keeps at most 10 services per application.")
			}
			if _, err := tx.Exec("INSERT INTO gcp_appengine_services(project, id, shard_by, created) VALUES(?, ?, '', ?)", e.cfg.Project, service, timestamp(e.cfg.Now())); err != nil {
				return nil, err
			}
			s, created = serviceRecord{ID: service, Allocations: map[string]int{}}, true
		} else if err != nil {
			return nil, err
		}
		if _, err := loadVersion(tx, e.cfg.Project, service, id); err == nil {
			return nil, exists("Version " + id + " already exists in service " + service + ".")
		} else if !isNotFound(err) {
			return nil, err
		}
		var inService, total int
		if err := tx.QueryRow("SELECT count(*), coalesce(sum(service = ?), 0) FROM gcp_appengine_versions WHERE project = ?", service, e.cfg.Project).Scan(&total, &inService); err != nil {
			return nil, err
		}
		if inService >= maxVersions || total >= maxAllVersions {
			return nil, exhausted("The local App Engine simulator keeps at most 20 versions per service and 50 versions per application.")
		}
		seq, err := next(tx)
		if err != nil {
			return nil, err
		}
		v := version{Service: service, ID: id, Runtime: "python312", ServingStatus: servingStatus, Environment: environment, Deployment: deployment, Code: code,
			Created: timestamp(e.cfg.Now()), CreatedBy: q.subject}
		if _, err := tx.Exec("INSERT INTO gcp_appengine_versions(project, service, id, runtime, serving_status, env_variables, deployment, code, code_sha256, files, created, created_by, seq) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			e.cfg.Project, service, id, v.Runtime, v.ServingStatus, string(envJSON), string(deployment), code, digest(code), countFiles(code), v.Created, v.CreatedBy, seq); err != nil {
			return nil, err
		}
		// The first version of a new service receives all of its traffic;
		// later versions receive none until the split names them.
		if created {
			if _, err := tx.Exec("INSERT INTO gcp_appengine_allocations(project, service, version, share) VALUES(?, ?, ?, ?)", e.cfg.Project, service, id, allocationUnits); err != nil {
				return nil, err
			}
			s.Allocations[id] = allocationUnits
		}
		return e.operation(tx, q.subject, "Versions.CreateVersion", "apps/"+e.cfg.Project+"/services/"+service+"/versions/"+id, typed("appengine.v1.Version", e.versionResource(a, v, true)))
	})
	if err == nil {
		e.warm()
	}
	return out, err
}

func (e *Engine) patchVersion(q *request) (any, error) {
	paths, err := mask(q, "servingStatus")
	if err != nil {
		return nil, err
	}
	body, err := e.patchBody(q.body, "Version", paths)
	if err != nil {
		return nil, err
	}
	value := ""
	if raw, ok := body["servingStatus"]; ok {
		if value, err = decodeString(raw, "servingStatus"); err != nil {
			return nil, err
		}
	}
	if value != "SERVING" && value != "STOPPED" {
		return nil, invalid("servingStatus must be SERVING or STOPPED.")
	}
	service, id := q.path["servicesId"], q.path["versionsId"]
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		a, err := loadApplication(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		if _, err := loadService(tx, e.cfg.Project, service); err != nil {
			return nil, err
		}
		v, err := loadVersion(tx, e.cfg.Project, service, id)
		if err != nil {
			return nil, err
		}
		v.ServingStatus = value
		if _, err := tx.Exec("UPDATE gcp_appengine_versions SET serving_status = ? WHERE project = ? AND service = ? AND id = ?", value, e.cfg.Project, service, id); err != nil {
			return nil, err
		}
		return e.operation(tx, q.subject, "Versions.UpdateVersion", "apps/"+e.cfg.Project+"/services/"+service+"/versions/"+id, typed("appengine.v1.Version", e.versionResource(a, v, false)))
	})
}

func (e *Engine) deleteVersion(q *request) (any, error) {
	service, id := q.path["servicesId"], q.path["versionsId"]
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		s, err := loadService(tx, e.cfg.Project, service)
		if err != nil {
			return nil, err
		}
		if _, err := loadVersion(tx, e.cfg.Project, service, id); err != nil {
			return nil, err
		}
		if s.Allocations[id] > 0 {
			return nil, failed("Version " + id + " of service " + service + " receives traffic and cannot be deleted. Move its traffic to another version first, or delete the service.")
		}
		if _, err := tx.Exec("DELETE FROM gcp_appengine_versions WHERE project = ? AND service = ? AND id = ?", e.cfg.Project, service, id); err != nil {
			return nil, err
		}
		return e.operation(tx, q.subject, "Versions.DeleteVersion", "apps/"+e.cfg.Project+"/services/"+service+"/versions/"+id, typed("protobuf.Empty", nil))
	})
}

// sortedAllocations orders a split by version ID, the order the data plane
// walks when it maps a request to a version.
func sortedAllocations(allocations map[string]int) []string {
	ids := make([]string, 0, len(allocations))
	for id := range allocations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
