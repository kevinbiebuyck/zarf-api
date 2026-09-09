package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/zarf-dev/zarf/src/pkg/logger"
	"github.com/zarf-dev/zarf/src/pkg/packager/layout"
	"github.com/zarf-dev/zarf/src/pkg/state"

	"github.com/kevinbiebuyck/zarf-api/internal/jobs"
	"github.com/kevinbiebuyck/zarf-api/internal/zarfz"
)

// --- meta ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := zarfz.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("cluster unreachable: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.cfg.Version})
}

// --- local package store ---

// handlePackageImport imports a package in a single request. The request body
// is the raw package tarball (.tar.zst / .tar).
func (s *Server) handlePackageImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	validate := q.Get("validate") != "false"
	verify, err := parseVerify(q.Get("verify"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pkg, err := s.store.ImportReader(r.Context(), r.Body, q.Get("fileName"), q.Get("shasum"), validate, verify)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pkg)
}

func (s *Server) handlePackageList(w http.ResponseWriter, r *http.Request) {
	pkgs, err := s.store.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": pkgs})
}

func (s *Server) handlePackageGet(w http.ResponseWriter, r *http.Request) {
	pkg, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (s *Server) handlePackageDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- chunked uploads ---

type createUploadRequest struct {
	FileNameHint string `json:"fileName,omitempty"`
	SHASum256    string `json:"sha256,omitempty"`
}

func (s *Server) handleUploadCreate(w http.ResponseWriter, r *http.Request) {
	var req createUploadRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	count, err := s.store.CountUploads(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if count >= s.cfg.MaxUploadSessions {
		writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many in-flight uploads (%d)", count))
		return
	}
	u, err := s.store.CreateUpload(r.Context(), req.FileNameHint, req.SHASum256)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUploadGet(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUpload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid chunk index: %w", err))
		return
	}
	u, err := s.store.PutChunk(r.Context(), r.PathValue("id"), index, r.Body)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	validate := q.Get("validate") != "false"
	verify, err := parseVerify(q.Get("verify"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pkg, err := s.store.CompleteUpload(r.Context(), r.PathValue("id"), validate, verify)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pkg)
}

func (s *Server) handleUploadAbort(w http.ResponseWriter, r *http.Request) {
	if err := s.store.AbortUpload(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- cluster operations ---

func (s *Server) handlePackageDeploy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req zarfz.DeployRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}

	// Default source: the stored package. An explicit source in the request
	// (oci://, https://, ...) overrides it, mirroring the CLI's PACKAGE_SOURCE.
	if req.Source == "" {
		path, err := s.store.Path(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		req.Source = path
	}
	if req.PublicKeyPath == "" {
		req.PublicKeyPath = s.cfg.PublicKeyPath
	}

	job := s.jobs.Create(jobs.KindDeploy, id)
	go s.runDeploy(job, req)
	s.respondJob(w, r, job, http.StatusAccepted)
}

func (s *Server) runDeploy(job *jobs.Job, req zarfz.DeployRequest) {
	log := job.Logger(s.logger)
	ctx := logger.WithContext(context.Background(), log)

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()

	job.Start()
	log.Info("deploy started", "source", req.Source, "components", req.Components)
	res, err := zarfz.Deploy(ctx, req)
	if err != nil {
		log.Error("deploy failed", "error", err)
		job.Fail(err)
		return
	}

	// Summarize like the CLI does: deployed components + connect strings.
	type component struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	result := struct {
		Components     []component          `json:"components"`
		ConnectStrings state.ConnectStrings `json:"connectStrings,omitempty"`
	}{Components: []component{}}
	connectStrings := state.ConnectStrings{}
	for _, c := range res.DeployedComponents {
		result.Components = append(result.Components, component{Name: c.Name, Status: string(c.Status)})
		for _, chart := range c.InstalledCharts {
			for k, v := range chart.ConnectStrings {
				connectStrings[k] = v
			}
		}
	}
	if len(connectStrings) > 0 {
		result.ConnectStrings = connectStrings
	}

	log.Info("deploy succeeded", "components", len(result.Components))
	job.Succeed(result)
}

func (s *Server) handleDeploymentList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	deployed, err := zarfz.ListDeployed(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": deployed})
}

func (s *Server) handleDeploymentGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	pkg, err := zarfz.GetDeployed(ctx, r.PathValue("name"), r.URL.Query().Get("namespaceOverride"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (s *Server) handleDeploymentRemove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req zarfz.RemoveRequest
	if r.Method == http.MethodPost {
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
			return
		}
	}
	// Query parameters work for both DELETE and POST.
	q := r.URL.Query()
	if v := q.Get("components"); v != "" {
		req.Components = v
	}
	if v := q.Get("namespaceOverride"); v != "" {
		req.NamespaceOverride = v
	}
	if v := q.Get("timeout"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid timeout: %w", err))
			return
		}
		req.Timeout = zarfz.Duration(d)
	}
	if q.Get("skipVersionCheck") == "true" {
		req.SkipVersionCheck = true
	}

	job := s.jobs.Create(jobs.KindRemove, name)
	go s.runRemove(job, name, req)
	s.respondJob(w, r, job, http.StatusAccepted)
}

func (s *Server) runRemove(job *jobs.Job, name string, req zarfz.RemoveRequest) {
	log := job.Logger(s.logger)
	ctx := logger.WithContext(context.Background(), log)

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()

	job.Start()
	log.Info("remove started", "package", name, "components", req.Components)
	if err := zarfz.Remove(ctx, name, req); err != nil {
		log.Error("remove failed", "error", err)
		job.Fail(err)
		return
	}
	log.Info("remove succeeded", "package", name)
	job.Succeed(map[string]string{"removed": name})
}

// --- jobs ---

func (s *Server) handleJobList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobs.List()})
}

func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("job %q not found", r.PathValue("id")))
		return
	}
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	writeJSON(w, http.StatusOK, map[string]any{
		"job":  job,
		"logs": job.Logs(tail),
	})
}

// respondJob writes the job as JSON. With ?wait=true it blocks until the job
// finishes and returns 200 (succeeded) or 500 (failed) instead of 202.
func (s *Server) respondJob(w http.ResponseWriter, r *http.Request, job *jobs.Job, acceptedCode int) {
	if r.URL.Query().Get("wait") != "true" {
		writeJSON(w, acceptedCode, job)
		return
	}
	select {
	case <-job.Done():
	case <-r.Context().Done():
		return // client gave up; job keeps running and can be polled
	}
	if job.Status == jobs.StatusFailed {
		writeJSON(w, http.StatusInternalServerError, job)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func parseVerify(mode string) (layout.VerificationStrategy, error) {
	switch mode {
	case "", "if-possible":
		return layout.VerifyIfPossible, nil
	case "never":
		return layout.VerifyNever, nil
	case "always":
		return layout.VerifyAlways, nil
	default:
		return layout.VerifyIfPossible, fmt.Errorf("invalid verify value %q (must be never, if-possible, or always)", mode)
	}
}
