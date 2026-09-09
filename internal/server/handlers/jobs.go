package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/kevinbiebuyck/zarf-api/internal/jobs"
)

func (h *Handlers) jobList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": h.jobs.List()})
}

func (h *Handlers) jobGet(w http.ResponseWriter, r *http.Request) {
	job, ok := h.jobs.Get(r.PathValue("id"))
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
func (h *Handlers) respondJob(w http.ResponseWriter, r *http.Request, job *jobs.Job, acceptedCode int) {
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
