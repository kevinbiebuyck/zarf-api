// Package server exposes zarf package operations over an HTTP JSON API.
// Route handlers live in the handlers subpackage, one file per resource.
package server

import (
	"log/slog"
	"net/http"

	"github.com/kevinbiebuyck/zarf-api/internal/config"
	"github.com/kevinbiebuyck/zarf-api/internal/jobs"
	"github.com/kevinbiebuyck/zarf-api/internal/server/handlers"
	"github.com/kevinbiebuyck/zarf-api/internal/store"
)

// Server wires the HTTP API to the package store, the job manager and zarf.
type Server struct {
	logger   *slog.Logger
	handlers *handlers.Handlers
}

// New returns a configured Server.
func New(cfg config.Config, st *store.Store, jm *jobs.Manager, logger *slog.Logger) *Server {
	return &Server{logger: logger, handlers: handlers.New(cfg, st, jm, logger)}
}

// Handler returns the root http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.handlers.Register(mux)
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
