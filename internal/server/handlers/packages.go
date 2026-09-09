package handlers

import (
	"fmt"
	"net/http"

	"github.com/zarf-dev/zarf/src/pkg/packager/layout"
)

// packageImport imports a package in a single request. The request body is
// the raw package tarball (.tar.zst / .tar).
func (h *Handlers) packageImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	validate := q.Get("validate") != "false"
	verify, err := parseVerify(q.Get("verify"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pkg, err := h.store.ImportReader(r.Context(), r.Body, q.Get("fileName"), q.Get("shasum"), validate, verify)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pkg)
}

func (h *Handlers) packageList(w http.ResponseWriter, r *http.Request) {
	pkgs, err := h.store.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": pkgs})
}

func (h *Handlers) packageGet(w http.ResponseWriter, r *http.Request) {
	pkg, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (h *Handlers) packageDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
