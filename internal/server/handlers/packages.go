package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/zarf-dev/zarf/src/pkg/packager/layout"

	"github.com/kevinbiebuyck/zarf-api/internal/store"
	"github.com/kevinbiebuyck/zarf-api/internal/zarfz"
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

// packageDefinition returns the package's zarf.yaml definition (variables,
// components) so clients can render deploy forms. Equivalent to
// `zarf package inspect definition`.
func (h *Handlers) packageDefinition(w http.ResponseWriter, r *http.Request) {
	path, err := h.store.Path(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	def, err := zarfz.GetDefinition(r.Context(), path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// packageConfigSchema returns the JSON Schema describing the package's
// install-time configuration surface, so clients can render a generated
// config form. The native zarf mechanism — a values.schema.json produced by
// `zarf package create` from the zarf.yaml `values.schema` field — is
// preferred; the API-specific config.schema.json convention (see
// tools/addschema) is the fallback. The X-Zarf-Schema-Target response header
// tells the client where collected values go: "values" (package values
// document, validated by zarf at deploy time) or "overrides" (per-chart helm
// values overrides). 404 when the package has neither file.
func (h *Handlers) packageConfigSchema(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	schema, err := h.store.ReadPackageRootFile(r.Context(), id, "values.schema.json")
	target := "values"
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeStoreError(w, err)
			return
		}
		schema, err = h.store.ReadPackageRootFile(r.Context(), id, "config.schema.json")
		target = "overrides"
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Zarf-Schema-Target", target)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(schema)
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
