// Package main is the entrypoint for the zarf-api server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"helm.sh/helm/v4/pkg/kube"

	zconfig "github.com/zarf-dev/zarf/src/config"
	"github.com/zarf-dev/zarf/src/pkg/cluster"
	zlogger "github.com/zarf-dev/zarf/src/pkg/logger"

	"github.com/kevinbiebuyck/zarf-api/internal/config"
	"github.com/kevinbiebuyck/zarf-api/internal/jobs"
	"github.com/kevinbiebuyck/zarf-api/internal/server"
	"github.com/kevinbiebuyck/zarf-api/internal/store"
)

// version is set via -ldflags "-X main.version=..." at build time.
var version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if cfg.Version == "0.1.0-dev" {
		cfg.Version = version
	}

	// Configure zarf's process-wide settings the same way the CLI does.
	zconfig.CommonOptions.CachePath = cfg.CacheDir
	zconfig.CommonOptions.TempDirectory = cfg.TempDir
	zconfig.CLIVersion = "zarf-api/" + cfg.Version
	// Ensure the field manager is set to Zarf during any Helm SDK actions.
	kube.ManagedFieldsManager = cluster.FieldManagerName

	level, err := zlogger.ParseLevel(cfg.LogLevel)
	if err != nil {
		return err
	}
	log, err := zlogger.New(zlogger.Config{
		Level:       level,
		Format:      zlogger.Format(cfg.LogFormat),
		Destination: zlogger.DestinationDefault,
		Color:       zlogger.Color(false),
	})
	if err != nil {
		return err
	}
	zlogger.SetDefault(log)

	for _, dir := range []string{cfg.CacheDir, cfg.TempDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("unable to create %s: %w", dir, err)
		}
	}

	st, err := store.Open(cfg.PackagesDir, cfg.UploadsDir)
	if err != nil {
		return err
	}

	srv := server.New(cfg, st, jobs.NewManager(cfg.MaxJobs, cfg.JobLogLines), log)
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: srv.Handler(),
		// Package uploads and deploys are long-running; don't cap them.
		ReadTimeout:       0,
		ReadHeaderTimeout: 30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("zarf-api listening", "port", cfg.Port, "dataDir", cfg.DataDir, "version", cfg.Version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}
