package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

var sesSinkMessageID = regexp.MustCompile(`^[a-z2-7]{32}$`)

func sesSinkInspection(engine *catalog.Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		actor, trusted := provider.ActorFromContext(r.Context())
		if !trusted || actor.CredentialRole != provider.CredentialOperator || actor.CredentialIdentity != core.Administrator || actor.ExerciseIdentity != core.Administrator || consoleScopeFromRequest(r) != nil {
			http.Error(w, "An authenticated team operator is required.", http.StatusForbidden)
			return
		}
		path := strings.Split(strings.TrimPrefix(r.URL.Path, "/_simulator/v1/ses/sink/"), "/")
		if len(path) < 2 || len(path) > 4 || path[1] != "messages" || !slices.Contains(aws.Regions, path[0]) || len(path) >= 3 && !sesSinkMessageID.MatchString(path[2]) || len(path) == 4 && path[3] != "content" {
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" || r.URL.RawPath != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			http.Error(w, "Use the canonical local receipt path without a request body.", 400)
			return
		}
		if r.Method != "GET" && !(r.Method == "DELETE" && len(path) == 3) {
			http.Error(w, "Unsupported local receipt method.", 405)
			return
		}
		var result any
		var err error
		if len(path) == 2 {
			var receipts []catalog.SESReceipt
			receipts, err = engine.SESSinkList(r.Context(), path[0])
			result = map[string]any{"messages": receipts}
		} else {
			var receipt catalog.SESReceipt
			var data []byte
			receipt, data, err = engine.SESSinkMessage(r.Context(), path[0], path[2], len(path) == 4, r.Method == "DELETE")
			if err == nil && len(path) == 4 {
				w.Header().Set("Content-Disposition", `attachment; filename="local-message.eml"`)
				w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write(data)
				return
			}
			result = receipt
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.NotFound(w, r)
		case err != nil:
			http.Error(w, "Local SES sink storage unavailable.", 500)
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
		}
	})
}
