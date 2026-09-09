// Package server exposes zarf package operations over an HTTP JSON API.
package server

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

// Server wires the HTTP API to the package store, the job manager and zarf.
type Server struct {
	cfg    config.Config
	store  *store.Store
	jobs   *jobs.Manager
	logger *slog.Logger

	// clusterMu serializes cluster-mutating operations (deploy, remove).
	// Zarf's deploy machinery relies on process-global state, so running two
	// at once inside one process is not safe.
	clusterMu sync.Mutex
}

// New returns a configured Server.
func New(cfg config.Config, st *store.Store, jm *jobs.Manager, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, jobs: jm, logger: logger}
}

// Handler returns the root http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)

	mux.HandleFunc("POST /api/v1/packages", s.handlePackageImport)
	mux.HandleFunc("GET /api/v1/packages", s.handlePackageList)
	mux.HandleFunc("GET /api/v1/packages/{id}", s.handlePackageGet)
	mux.HandleFunc("DELETE /api/v1/packages/{id}", s.handlePackageDelete)
	mux.HandleFunc("POST /api/v1/packages/{id}/deploy", s.handlePackageDeploy)

	mux.HandleFunc("POST /api/v1/uploads", s.handleUploadCreate)
	mux.HandleFunc("GET /api/v1/uploads/{id}", s.handleUploadGet)
	mux.HandleFunc("PUT /api/v1/uploads/{id}/chunks/{index}", s.handleUploadChunk)
	mux.HandleFunc("POST /api/v1/uploads/{id}/complete", s.handleUploadComplete)
	mux.HandleFunc("DELETE /api/v1/uploads/{id}", s.handleUploadAbort)

	mux.HandleFunc("GET /api/v1/deployments", s.handleDeploymentList)
	mux.HandleFunc("GET /api/v1/deployments/{name}", s.handleDeploymentGet)
	mux.HandleFunc("DELETE /api/v1/deployments/{name}", s.handleDeploymentRemove)
	mux.HandleFunc("POST /api/v1/deployments/{name}/remove", s.handleDeploymentRemove)

	mux.HandleFunc("GET /api/v1/jobs", s.handleJobList)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.handleJobGet)

	return s.withLogging(mux)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Chunk pushes are too chatty for request logs.
		if r.Method != http.MethodPut {
			s.logger.Debug("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		}
		next.ServeHTTP(w, r)
	})
}

// --- helpers ---

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
