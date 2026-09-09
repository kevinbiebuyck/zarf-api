package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/zarf-dev/zarf/src/pkg/logger"
	"github.com/zarf-dev/zarf/src/pkg/state"

	"github.com/kevinbiebuyck/zarf-api/internal/jobs"
	"github.com/kevinbiebuyck/zarf-api/internal/zarfz"
)

func (h *Handlers) packageDeploy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req zarfz.DeployRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}

	// Default source: the stored package. An explicit source in the request
	// (oci://, https://, ...) overrides it, mirroring the CLI's PACKAGE_SOURCE.
	if req.Source == "" {
		path, err := h.store.Path(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		req.Source = path
	}
	if req.PublicKeyPath == "" {
		req.PublicKeyPath = h.cfg.PublicKeyPath
	}

	job := h.jobs.Create(jobs.KindDeploy, id)
	go h.runDeploy(job, req)
	h.respondJob(w, r, job, http.StatusAccepted)
}

func (h *Handlers) runDeploy(job *jobs.Job, req zarfz.DeployRequest) {
	log := job.Logger(h.logger)
	ctx := logger.WithContext(context.Background(), log)

	h.clusterMu.Lock()
	defer h.clusterMu.Unlock()

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

func (h *Handlers) deploymentList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	deployed, err := zarfz.ListDeployed(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": deployed})
}

func (h *Handlers) deploymentGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	pkg, err := zarfz.GetDeployed(ctx, r.PathValue("name"), r.URL.Query().Get("namespaceOverride"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (h *Handlers) deploymentRemove(w http.ResponseWriter, r *http.Request) {
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

	job := h.jobs.Create(jobs.KindRemove, name)
	go h.runRemove(job, name, req)
	h.respondJob(w, r, job, http.StatusAccepted)
}

func (h *Handlers) runRemove(job *jobs.Job, name string, req zarfz.RemoveRequest) {
	log := job.Logger(h.logger)
	ctx := logger.WithContext(context.Background(), log)

	h.clusterMu.Lock()
	defer h.clusterMu.Unlock()

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
