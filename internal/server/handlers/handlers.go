// Package handlers contains the HTTP handlers of the zarf-api server,
// grouped one file per resource. The Handlers struct holds every dependency
// the handlers need; Register wires all routes into a mux.
package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/kevinbiebuyck/zarf-api/internal/config"
	"github.com/kevinbiebuyck/zarf-api/internal/jobs"
	"github.com/kevinbiebuyck/zarf-api/internal/store"
)

// Handlers holds the dependencies shared by all HTTP handlers.
type Handlers struct {
	cfg    config.Config
	store  *store.Store
	jobs   *jobs.Manager
	logger *slog.Logger

	// clusterMu serializes cluster-mutating operations (deploy, remove).
	// Zarf's deploy machinery relies on process-global state, so running two
	// at once inside one process is not safe.
	clusterMu sync.Mutex
}

// New returns the Handlers for the given dependencies.
func New(cfg config.Config, st *store.Store, jm *jobs.Manager, logger *slog.Logger) *Handlers {
	return &Handlers{cfg: cfg, store: st, jobs: jm, logger: logger}
}

// Register wires all API routes into mux.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.HandleFunc("GET /api/v1/version", h.version)

	mux.HandleFunc("POST /api/v1/packages", h.packageImport)
	mux.HandleFunc("GET /api/v1/packages", h.packageList)
	mux.HandleFunc("GET /api/v1/packages/{id}", h.packageGet)
	mux.HandleFunc("DELETE /api/v1/packages/{id}", h.packageDelete)
	mux.HandleFunc("POST /api/v1/packages/{id}/deploy", h.packageDeploy)

	mux.HandleFunc("POST /api/v1/uploads", h.uploadCreate)
	mux.HandleFunc("GET /api/v1/uploads/{id}", h.uploadGet)
	mux.HandleFunc("PUT /api/v1/uploads/{id}/chunks/{index}", h.uploadChunk)
	mux.HandleFunc("POST /api/v1/uploads/{id}/complete", h.uploadComplete)
	mux.HandleFunc("DELETE /api/v1/uploads/{id}", h.uploadAbort)

	mux.HandleFunc("GET /api/v1/deployments", h.deploymentList)
	mux.HandleFunc("GET /api/v1/deployments/{name}", h.deploymentGet)
	mux.HandleFunc("DELETE /api/v1/deployments/{name}", h.deploymentRemove)
	mux.HandleFunc("POST /api/v1/deployments/{name}/remove", h.deploymentRemove)

	mux.HandleFunc("GET /api/v1/jobs", h.jobList)
	mux.HandleFunc("GET /api/v1/jobs/{id}", h.jobGet)
}

// --- shared helpers ---

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, errorResponse{Error: err.Error()})
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// decodeBody decodes an optional JSON request body into v. An empty body is
// not an error.
func decodeBody(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}
