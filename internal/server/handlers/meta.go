package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kevinbiebuyck/zarf-api/internal/zarfz"
)

func (h *Handlers) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handlers) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := zarfz.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("cluster unreachable: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handlers) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": h.cfg.Version})
}
