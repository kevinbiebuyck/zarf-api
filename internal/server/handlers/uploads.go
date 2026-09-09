package handlers

import (
	"fmt"
	"net/http"
	"strconv"
)

type createUploadRequest struct {
	FileNameHint string `json:"fileName,omitempty"`
	SHASum256    string `json:"sha256,omitempty"`
}

func (h *Handlers) uploadCreate(w http.ResponseWriter, r *http.Request) {
	var req createUploadRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	count, err := h.store.CountUploads(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if count >= h.cfg.MaxUploadSessions {
		writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many in-flight uploads (%d)", count))
		return
	}
	u, err := h.store.CreateUpload(r.Context(), req.FileNameHint, req.SHASum256)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (h *Handlers) uploadGet(w http.ResponseWriter, r *http.Request) {
	u, err := h.store.GetUpload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *Handlers) uploadChunk(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid chunk index: %w", err))
		return
	}
	u, err := h.store.PutChunk(r.Context(), r.PathValue("id"), index, r.Body)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *Handlers) uploadComplete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	validate := q.Get("validate") != "false"
	verify, err := parseVerify(q.Get("verify"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pkg, err := h.store.CompleteUpload(r.Context(), r.PathValue("id"), validate, verify)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pkg)
}

func (h *Handlers) uploadAbort(w http.ResponseWriter, r *http.Request) {
	if err := h.store.AbortUpload(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
