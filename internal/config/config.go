// Package config holds the runtime configuration for the zarf-api server.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config is the runtime configuration of the API server.
type Config struct {
	// Version of this API server binary.
	Version string

	// Port the HTTP server listens on.
	Port int
	// LogLevel is a zarf logger level: debug, info, warn, error.
	LogLevel string
	// LogFormat is a zarf logger format: json, console, dev, none.
	LogFormat string

	// DataDir is the root for all persistent state (package store, upload
	// sessions, zarf cache). Mount a PVC here in Kubernetes.
	DataDir string
	// TempDir overrides the directory zarf uses for package extraction during
	// load/deploy. Defaults to <DataDir>/tmp so extraction lands on the PVC
	// instead of the (usually small) container filesystem.
	TempDir string

	// PublicKeyPath is an optional default cosign public key used to verify
	// package signatures. Can be overridden per request.
	PublicKeyPath string

	// MaxUploadSessions caps concurrent chunked-upload sessions.
	MaxUploadSessions int
	// MaxJobs caps how many finished jobs are retained in memory.
	MaxJobs int
	// JobLogLines caps how many log lines are kept per job.
	JobLogLines int

	// PackagesDir is where imported package tarballs are stored.
	PackagesDir string
	// UploadsDir is where in-flight chunked upload sessions live.
	UploadsDir string
	// CacheDir is zarf's cache directory (OCI layers etc).
	CacheDir string
}

// FromEnv builds the configuration from the environment.
func FromEnv() (Config, error) {
	cfg := Config{
		Version:           envOr("ZARF_API_VERSION", "0.1.0-dev"),
		Port:              8080,
		LogLevel:          envOr("ZARF_API_LOG_LEVEL", "info"),
		LogFormat:         envOr("ZARF_API_LOG_FORMAT", "json"),
		DataDir:           envOr("ZARF_API_DATA_DIR", "./data"),
		TempDir:           os.Getenv("ZARF_API_TEMP_DIR"),
		PublicKeyPath:     os.Getenv("ZARF_API_PUBLIC_KEY_PATH"),
		MaxUploadSessions: 16,
		MaxJobs:           100,
		JobLogLines:       2000,
	}

	if v := os.Getenv("ZARF_API_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return Config{}, fmt.Errorf("invalid ZARF_API_PORT %q", v)
		}
		cfg.Port = p
	}
	if v := os.Getenv("ZARF_API_MAX_UPLOAD_SESSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("invalid ZARF_API_MAX_UPLOAD_SESSIONS %q", v)
		}
		cfg.MaxUploadSessions = n
	}

	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("unable to resolve data dir: %w", err)
	}
	cfg.DataDir = dataDir
	cfg.PackagesDir = filepath.Join(dataDir, "packages")
	cfg.UploadsDir = filepath.Join(dataDir, "uploads")
	cfg.CacheDir = filepath.Join(dataDir, "cache")
	if cfg.TempDir == "" {
		cfg.TempDir = filepath.Join(dataDir, "tmp")
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
