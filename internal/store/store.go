// Package store manages the local on-disk package inventory and chunked
// upload sessions used to import packages over HTTP.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zarf-dev/zarf/src/api/v1alpha1"
	"github.com/zarf-dev/zarf/src/pkg/packager/layout"
)

// Package describes one imported package tarball in the local store.
type Package struct {
	// ID is the canonical package file name (e.g. zarf-package-podinfo-amd64-1.0.0.tar.zst).
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version,omitempty"`
	Architecture string   `json:"architecture,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Description string    `json:"description,omitempty"`
	YOLO        bool      `json:"yolo,omitempty"`
	Components  []string  `json:"components,omitempty"`
	Size        int64     `json:"size"`
	SHASum256   string    `json:"sha256"`
	ImportedAt  time.Time `json:"importedAt"`
}

// Upload is an in-flight chunked upload session.
type Upload struct {
	ID           string           `json:"id"`
	FileNameHint string           `json:"fileNameHint,omitempty"`
	SHASum256    string           `json:"sha256,omitempty"`
	CreatedAt    time.Time        `json:"createdAt"`
	// Chunks maps chunk index to chunk size in bytes.
	Chunks map[int]int64 `json:"chunks"`
}

// ReceivedBytes returns the total bytes received so far.
func (u Upload) ReceivedBytes() int64 {
	var n int64
	for _, size := range u.Chunks {
		n += size
	}
	return n
}

// Store persists packages and upload sessions under a data directory.
type Store struct {
	packagesDir string
	uploadsDir  string

	mu sync.Mutex // guards session.json updates
}

// ErrNotFound is returned when a package or upload does not exist.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when a package with the same file name already exists.
var ErrExists = errors.New("package already exists")

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*\.tar(\.zst)?$`)

// Open creates the store directories and returns the Store.
func Open(packagesDir, uploadsDir string) (*Store, error) {
	for _, dir := range []string{packagesDir, uploadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("unable to create %s: %w", dir, err)
		}
	}
	return &Store{packagesDir: packagesDir, uploadsDir: uploadsDir}, nil
}

// List returns all imported packages, sorted by file name.
func (s *Store) List(_ context.Context) ([]Package, error) {
	entries, err := os.ReadDir(s.packagesDir)
	if err != nil {
		return nil, err
	}
	out := []Package{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		pkg, err := s.readMeta(filepath.Join(s.packagesDir, e.Name()))
		if err != nil {
			continue // skip unreadable sidecars rather than failing the whole listing
		}
		out = append(out, pkg)
	}
	slices.SortFunc(out, func(a, b Package) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Get returns one package by ID.
func (s *Store) Get(_ context.Context, id string) (Package, error) {
	if !validID.MatchString(id) {
		return Package{}, fmt.Errorf("%w: invalid package id %q", ErrNotFound, id)
	}
	pkg, err := s.readMeta(s.metaPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return Package{}, fmt.Errorf("%w: package %q", ErrNotFound, id)
	}
	return pkg, err
}

// Path returns the on-disk path of a stored package tarball.
func (s *Store) Path(_ context.Context, id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("%w: invalid package id %q", ErrNotFound, id)
	}
	p := filepath.Join(s.packagesDir, id)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%w: package %q", ErrNotFound, id)
	}
	return p, nil
}

// Delete removes a package tarball and its metadata sidecar.
func (s *Store) Delete(_ context.Context, id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("%w: invalid package id %q", ErrNotFound, id)
	}
	p := filepath.Join(s.packagesDir, id)
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("%w: package %q", ErrNotFound, id)
	}
	if err := os.Remove(p); err != nil {
		return err
	}
	err := os.Remove(s.metaPath(id))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) metaPath(id string) string {
	return filepath.Join(s.packagesDir, id+".meta.json")
}

func (s *Store) readMeta(path string) (Package, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Package{}, err
	}
	var pkg Package
	return pkg, json.Unmarshal(b, &pkg)
}

// ImportReader streams a package tarball from r into the store.
// fileNameHint is used to name the package when validate=false; when
// validation is enabled the canonical zarf file name is used instead.
func (s *Store) ImportReader(ctx context.Context, r io.Reader, fileNameHint, shasum string, validate bool, verify layout.VerificationStrategy) (Package, error) {
	// The temp file must carry a .tar.zst suffix: zarf identifies the archive
	// format by extension when loading.
	tmp, err := os.CreateTemp(s.packagesDir, ".incoming-*.tar.zst")
	if err != nil {
		return Package{}, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		_ = tmp.Close()
		return Package{}, fmt.Errorf("unable to store uploaded package: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Package{}, err
	}
	return s.finalizeImport(ctx, tmpPath, h, fileNameHint, shasum, validate, verify)
}

// finalizeImport verifies the checksum, optionally validates the package by
// loading it with zarf itself, moves it into place and writes the sidecar.
// On success tmpPath has been moved away (it must not be deleted by callers).
func (s *Store) finalizeImport(ctx context.Context, tmpPath string, h hash, fileNameHint, shasum string, validate bool, verify layout.VerificationStrategy) (Package, error) {
	sum := hex.EncodeToString(h.Sum(nil))
	if shasum != "" && !strings.EqualFold(shasum, sum) {
		return Package{}, fmt.Errorf("sha256 mismatch: expected %s, got %s", shasum, sum)
	}

	var meta v1alpha1.ZarfPackage
	fileName := fileNameHint
	if validate {
		pkgLayout, err := layout.LoadFromTar(ctx, tmpPath, layout.PackageLayoutOptions{
			VerificationStrategy: verify,
		})
		if err != nil {
			return Package{}, fmt.Errorf("package validation failed: %w", err)
		}
		defer func() { _ = pkgLayout.Cleanup() }()
		meta = pkgLayout.AsV1alpha1()
		fileName, err = pkgLayout.FileName()
		if err != nil {
			return Package{}, fmt.Errorf("unable to determine package file name: %w", err)
		}
	}
	if fileName == "" {
		return Package{}, fmt.Errorf("no file name available: pass a fileName or enable validation")
	}
	if !validID.MatchString(fileName) {
		return Package{}, fmt.Errorf("invalid package file name %q", fileName)
	}

	finalPath := filepath.Join(s.packagesDir, fileName)
	if _, err := os.Stat(finalPath); err == nil {
		return Package{}, fmt.Errorf("%w: %s", ErrExists, fileName)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return Package{}, fmt.Errorf("unable to move package into store: %w", err)
	}

	info, err := os.Stat(finalPath)
	if err != nil {
		return Package{}, err
	}
	components := make([]string, 0, len(meta.Components))
	for _, c := range meta.Components {
		components = append(components, c.Name)
	}
	pkg := Package{
		ID:           fileName,
		Name:         meta.Metadata.Name,
		Version:      meta.Metadata.Version,
		Architecture: meta.Metadata.Architecture,
		Kind:         string(meta.Kind),
		Description:  meta.Metadata.Description,
		YOLO:         meta.Metadata.YOLO,
		Components:   components,
		Size:         info.Size(),
		SHASum256:    sum,
		ImportedAt:   time.Now().UTC(),
	}
	b, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return Package{}, err
	}
	if err := os.WriteFile(s.metaPath(fileName), b, 0o644); err != nil {
		return Package{}, fmt.Errorf("unable to write package metadata: %w", err)
	}
	return pkg, nil
}

// --- chunked uploads ---

type sessionFile struct {
	ID           string         `json:"id"`
	FileNameHint string         `json:"fileNameHint,omitempty"`
	SHASum256    string         `json:"sha256,omitempty"`
	CreatedAt    time.Time      `json:"createdAt"`
	Chunks       map[int]int64  `json:"chunks"`
}

// CreateUpload starts a new chunked upload session.
func (s *Store) CreateUpload(_ context.Context, fileNameHint, shasum string) (Upload, error) {
	id, err := newUploadID()
	if err != nil {
		return Upload{}, err
	}
	u := Upload{
		ID:           id,
		FileNameHint: fileNameHint,
		SHASum256:    shasum,
		CreatedAt:    time.Now().UTC(),
		Chunks:       map[int]int64{},
	}
	dir := s.sessionDir(id)
	if err := os.MkdirAll(filepath.Join(dir, "chunks"), 0o755); err != nil {
		return Upload{}, err
	}
	if err := s.writeSession(u); err != nil {
		return Upload{}, err
	}
	return u, nil
}

// GetUpload returns the state of an upload session.
func (s *Store) GetUpload(_ context.Context, id string) (Upload, error) {
	return s.readSession(id)
}

// PutChunk stores one chunk of an upload session. Chunks may arrive in any
// order and may be re-sent; the last write wins.
func (s *Store) PutChunk(_ context.Context, id string, index int, r io.Reader) (Upload, error) {
	if index < 0 {
		return Upload{}, fmt.Errorf("chunk index must be >= 0")
	}
	u, err := s.readSession(id)
	if err != nil {
		return Upload{}, err
	}
	dir := filepath.Join(s.sessionDir(id), "chunks")
	tmp, err := os.CreateTemp(dir, ".chunk-*")
	if err != nil {
		return Upload{}, err
	}
	size, err := io.Copy(tmp, r)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return Upload{}, fmt.Errorf("unable to store chunk: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Upload{}, err
	}
	chunkPath := filepath.Join(dir, strconv.Itoa(index))
	if err := os.Rename(tmp.Name(), chunkPath); err != nil {
		return Upload{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-read under lock to avoid lost updates from concurrent chunk uploads.
	u, err = s.readSession(id)
	if err != nil {
		return Upload{}, err
	}
	u.Chunks[index] = size
	if err := s.writeSession(u); err != nil {
		return Upload{}, err
	}
	return u, nil
}

// AbortUpload deletes an upload session and all received chunks.
func (s *Store) AbortUpload(_ context.Context, id string) error {
	if _, err := s.readSession(id); err != nil {
		return err
	}
	return os.RemoveAll(s.sessionDir(id))
}

// CompleteUpload assembles the chunks (which must be contiguous starting at
// index 0), imports the resulting tarball and removes the session.
func (s *Store) CompleteUpload(ctx context.Context, id string, validate bool, verify layout.VerificationStrategy) (Package, error) {
	u, err := s.readSession(id)
	if err != nil {
		return Package{}, err
	}
	if len(u.Chunks) == 0 {
		return Package{}, fmt.Errorf("upload session has no chunks")
	}
	indexes := make([]int, 0, len(u.Chunks))
	for idx := range u.Chunks {
		indexes = append(indexes, idx)
	}
	slices.Sort(indexes)
	for want, got := range indexes {
		if want != got {
			return Package{}, fmt.Errorf("missing chunk %d", want)
		}
	}

	// The temp file must carry a .tar.zst suffix: zarf identifies the archive
	// format by extension when loading.
	tmp, err := os.CreateTemp(s.packagesDir, ".incoming-*.tar.zst")
	if err != nil {
		return Package{}, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	h := sha256.New()
	for _, idx := range indexes {
		chunkPath := filepath.Join(s.sessionDir(id), "chunks", strconv.Itoa(idx))
		f, err := os.Open(chunkPath)
		if err != nil {
			_ = tmp.Close()
			return Package{}, err
		}
		_, copyErr := io.Copy(io.MultiWriter(tmp, h), f)
		_ = f.Close()
		if copyErr != nil {
			_ = tmp.Close()
			return Package{}, copyErr
		}
	}
	if err := tmp.Close(); err != nil {
		return Package{}, err
	}

	pkg, err := s.finalizeImport(ctx, tmpPath, h, u.FileNameHint, u.SHASum256, validate, verify)
	if err != nil {
		return Package{}, err
	}
	_ = os.RemoveAll(s.sessionDir(id))
	return pkg, nil
}

// CountUploads returns the number of in-flight upload sessions.
func (s *Store) CountUploads(_ context.Context) (int, error) {
	entries, err := os.ReadDir(s.uploadsDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n, nil
}

func (s *Store) sessionDir(id string) string {
	return filepath.Join(s.uploadsDir, id)
}

func (s *Store) readSession(id string) (Upload, error) {
	if matched, _ := regexp.MatchString(`^[a-f0-9]{16}$`, id); !matched {
		return Upload{}, fmt.Errorf("%w: upload %q", ErrNotFound, id)
	}
	b, err := os.ReadFile(filepath.Join(s.sessionDir(id), "session.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Upload{}, fmt.Errorf("%w: upload %q", ErrNotFound, id)
	}
	if err != nil {
		return Upload{}, err
	}
	var sf sessionFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return Upload{}, err
	}
	return Upload{
		ID:           sf.ID,
		FileNameHint: sf.FileNameHint,
		SHASum256:    sf.SHASum256,
		CreatedAt:    sf.CreatedAt,
		Chunks:       sf.Chunks,
	}, nil
}

func (s *Store) writeSession(u Upload) error {
	b, err := json.MarshalIndent(sessionFile{
		ID:           u.ID,
		FileNameHint: u.FileNameHint,
		SHASum256:    u.SHASum256,
		CreatedAt:    u.CreatedAt,
		Chunks:       u.Chunks,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.sessionDir(u.ID), "session.json"), b, 0o644)
}

func newUploadID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hash is the subset of hash.Hash used by the store.
type hash interface {
	Sum(b []byte) []byte
}
