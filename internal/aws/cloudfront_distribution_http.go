package aws

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func CloudFrontCapabilities() map[string]any {
	return map[string]any{
		"operations": []string{"CreateVpcOrigin", "GetVpcOrigin", "UpdateVpcOrigin", "DeleteVpcOrigin", "CreateDistribution", "GetDistribution", "GetDistributionConfig", "UpdateDistribution", "DeleteDistribution", "ListDistributions", "CreateInvalidation", "GetInvalidation"},
		"protocol":   "REST/XML 2020-05-31", "originTarget": "internal application load balancer (VPC origin configuration only)",
		"originLimit": 50, "cloudFormation": true, "cloudFormationTypes": []string{"AWS::CloudFront::VpcOrigin"},
		"dataPlane": true, "vpcOriginDataPlane": false, "distributionConsole": false, "distributionCloudFormation": false,
		"distributionOrigin": "one existing local S3 bucket", "distributionLimit": 100,
		"delivery": cfDeliveryPath, "reference": "/_simulator/docs/cloudfront-local.html",
		"cache":               "saved local object snapshots; DefaultTTL only; fresh distribution and S3 object authorization on every read",
		"externalOriginFetch": false,
	}
}

func isCFDistribution(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, cfDeliveryPath) {
		return true
	}
	if r.URL.Path != cfDistributionPath && !strings.HasPrefix(r.URL.Path, cfDistributionPath+"/") {
		return false
	}
	if service := r.Header.Get("X-Simulator-Service"); service != "" {
		return service == "cloudfront"
	}
	return elbSigningService(r.Header.Get("Authorization")) == "cloudfront"
}
func cfDistributionAction(r *http.Request) (action, id, invalidation string, err error) {
	bad := cfInvalid("Unsupported CloudFront distribution route, method or parameters.")
	if r.URL.RawPath != "" || r.URL.ForceQuery || r.Header.Get("X-Amz-Target") != "" {
		return "", "", "", bad
	}
	if r.URL.Path == cfDistributionPath {
		switch r.Method {
		case "POST":
			action = "CreateDistribution"
		case "GET":
			action = "ListDistributions"
		default:
			return "", "", "", bad
		}
	} else {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, cfDistributionPath+"/"), "/")
		if len(parts) < 1 || !cfDistributionID.MatchString(parts[0]) {
			return "", "", "", bad
		}
		id = parts[0]
		switch {
		case len(parts) == 1 && r.Method == "GET":
			action = "GetDistribution"
		case len(parts) == 1 && r.Method == "DELETE":
			action = "DeleteDistribution"
		case len(parts) == 2 && parts[1] == "config" && r.Method == "GET":
			action = "GetDistributionConfig"
		case len(parts) == 2 && parts[1] == "config" && r.Method == "PUT":
			action = "UpdateDistribution"
		case len(parts) == 2 && parts[1] == "invalidation" && r.Method == "POST":
			action = "CreateInvalidation"
		case len(parts) == 3 && parts[1] == "invalidation" && cfInvalidationID.MatchString(parts[2]) && r.Method == "GET":
			action = "GetInvalidation"
			invalidation = parts[2]
		default:
			return "", "", "", bad
		}
	}
	if action != "ListDistributions" && r.URL.RawQuery != "" {
		return "", "", "", bad
	}
	return
}

// Both the native API and the explicitly simulator-only viewer use one fresh
// policy snapshot. A cached object never caches permission to read it.
func (s *CloudFrontStore) serveDistribution(g *Gateway, w http.ResponseWriter, r *http.Request) {
	if s == nil || g.Identities == nil {
		cfError(w, errors.New("missing CloudFront authority"))
		return
	}
	principal, err := cfCaller(r)
	if err != nil {
		cfError(w, err)
		return
	}
	decision, err := g.Identities.DecisionSnapshot(r.Context(), principal)
	if err != nil || decision == nil {
		cfError(w, errors.New("CloudFront authority unavailable"))
		return
	}
	if strings.HasPrefix(r.URL.Path, cfDeliveryPath) {
		s.serveDistributionContent(w, r, principal, decision)
		return
	}
	action, id, invalidation, err := cfDistributionAction(r)
	if err != nil {
		cfError(w, err)
		return
	}
	resource := "*"
	if id != "" {
		resource = cfDistributionARN + id
	}
	status := decision("cloudfront:"+action, resource)
	w.Header().Set("X-Simulator-IAM-Decision", status)
	if status != "allowed" {
		cfError(w, cfFailure{403, "AccessDenied", "The selected identity cannot perform this CloudFront operation."})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil {
		cfError(w, cfInvalid("The request exceeds 32 KiB or is unreadable."))
		return
	}
	writesBody := action == "CreateDistribution" || action == "UpdateDistribution" || action == "CreateInvalidation"
	if !writesBody && len(data) != 0 {
		cfError(w, cfInvalid("This operation does not accept a body."))
		return
	}
	var config cfDistributionConfig
	var bucket string
	var batch cfInvalidationBatch
	if action == "CreateDistribution" || action == "UpdateDistribution" {
		config, bucket, err = cfParseDistribution(data)
	}
	if action == "CreateInvalidation" {
		batch, err = cfParseInvalidation(data)
	}
	if err != nil {
		cfError(w, err)
		return
	}
	limit := 25
	marker := ""
	if action == "ListDistributions" {
		query, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil {
			cfError(w, cfInvalid("Invalid list query."))
			return
		}
		for key, values := range query {
			if len(values) != 1 || (key != "Marker" && key != "MaxItems") {
				cfError(w, cfInvalid("Only Marker and MaxItems are supported."))
				return
			}
		}
		marker = query.Get("Marker")
		if marker != "" && !cfDistributionID.MatchString(marker) {
			cfError(w, cfInvalid("Invalid distribution marker."))
			return
		}
		if values, ok := query["MaxItems"]; ok {
			limit, err = strconv.Atoi(values[0])
			if err != nil || limit < 1 || limit > 50 {
				cfError(w, cfInvalid("MaxItems must be 1–50."))
				return
			}
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		cfError(w, err)
		return
	}
	defer tx.Rollback()
	var response any
	code := 200
	switch action {
	case "CreateDistribution", "UpdateDistribution":
		var row cfDistributionRow
		row, err = cfSaveDistribution(r.Context(), tx, id, r.Header.Get("If-Match"), action == "CreateDistribution", config, bucket)
		if err == nil {
			response = row.value
			w.Header().Set("ETag", row.revision)
			if action == "CreateDistribution" {
				code = 201
				w.Header().Set("Location", cfDistributionPath+"/"+row.value.Id)
			}
		}
	case "GetDistribution", "GetDistributionConfig":
		var row cfDistributionRow
		row, err = cfReadDistribution(r.Context(), tx, id)
		if err == nil {
			w.Header().Set("ETag", row.revision)
			response = row.value
			if action == "GetDistributionConfig" {
				response = row.value.DistributionConfig
			}
		}
	case "DeleteDistribution":
		err = cfDeleteDistribution(r.Context(), tx, id, r.Header.Get("If-Match"))
		code = 204
	case "ListDistributions":
		response, err = cfListDistribution(r, tx, marker, limit)
	case "CreateInvalidation":
		response, err = cfCreateInvalidation(r.Context(), tx, id, batch)
		code = 201
		if err == nil {
			w.Header().Set("Location", cfDistributionPath+"/"+id+"/invalidation/"+response.(cfInvalidation).Id)
		}
	case "GetInvalidation":
		if _, err = cfReadDistribution(r.Context(), tx, id); err == nil {
			var encoded string
			err = tx.QueryRowContext(r.Context(), "SELECT document FROM aws_cloudfront_invalidations WHERE distribution=? AND id=?", id, invalidation).Scan(&encoded)
			if errors.Is(err, sql.ErrNoRows) {
				err = cfFailure{404, "NoSuchInvalidation", "The invalidation does not exist."}
			} else if err == nil {
				var value cfInvalidation
				if json.Unmarshal([]byte(encoded), &value) != nil || value.Id != invalidation {
					err = errors.New("invalid saved invalidation")
				} else {
					value.XMLNS = cloudFrontNS
					response = value
				}
			}
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		w.Header().Del("ETag")
		w.Header().Del("Location")
		cfError(w, err)
		return
	}
	if code == 204 {
		w.WriteHeader(code)
		return
	}
	writeXML(w, code, response)
}

func (s *CloudFrontStore) serveDistributionContent(w http.ResponseWriter, r *http.Request, principal string, decision func(string, string) string) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, cfDeliveryPath), "/objects/", 2)
	if len(parts) != 2 || !cfDistributionID.MatchString(parts[0]) || !cfValidLocalKey(parts[1]) || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || (r.Method != "GET" && r.Method != "HEAD") || r.Header.Get("Range") != "" || r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
		cfError(w, cfInvalid("Use a canonical local GET/HEAD object path without queries, range or conditional headers."))
		return
	}
	if r.ContentLength > 0 {
		cfError(w, cfInvalid("Object reads do not accept a body."))
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		cfError(w, err)
		return
	}
	defer tx.Rollback()
	result, err := cfDeliver(r.Context(), tx, parts[0], principal, parts[1], decision, time.Now())
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		cfError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", result.contentType)
	w.Header().Set("ETag", result.etag)
	w.Header().Set("Content-Length", strconv.Itoa(len(result.data)))
	w.Header().Set("Age", strconv.FormatInt(result.age, 10))
	w.Header().Set("X-Cache", result.cache+" from local CloudFront simulator")
	w.WriteHeader(200)
	if r.Method == "GET" {
		_, _ = w.Write(result.data)
	}
}
