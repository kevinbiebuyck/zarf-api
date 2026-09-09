// Package jobs tracks asynchronous cluster operations (deploy, remove) and
// captures the logs zarf produces while they run.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"maps"
	"sync"
	"time"
)

// Kind identifies the type of operation a job performs.
type Kind string

const (
	// KindDeploy deploys a package into the cluster.
	KindDeploy Kind = "deploy"
	// KindRemove removes a deployed package from the cluster.
	KindRemove Kind = "remove"
)

// Status is the lifecycle state of a job.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// LogEntry is a single captured log line.
type LogEntry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// Job represents one asynchronous operation.
type Job struct {
	ID         string     `json:"id"`
	Kind       Kind       `json:"kind"`
	Package    string     `json:"package"`
	Status     Status     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      string     `json:"error,omitempty"`
	// Result is an operation-specific payload (e.g. deployed components).
	Result any `json:"result,omitempty"`

	maxLogLines int
	mu          sync.Mutex
	logs        []LogEntry
	done        chan struct{}
}

// Manager creates, tracks and retains jobs.
type Manager struct {
	mu          sync.Mutex
	jobs        map[string]*Job
	order       []string
	maxJobs     int
	maxLogLines int
}

// NewManager returns a Manager retaining at most maxJobs jobs.
func NewManager(maxJobs, maxLogLines int) *Manager {
	if maxJobs < 1 {
		maxJobs = 100
	}
	if maxLogLines < 1 {
		maxLogLines = 2000
	}
	return &Manager{
		jobs:        map[string]*Job{},
		maxJobs:     maxJobs,
		maxLogLines: maxLogLines,
	}
}

// Create registers a new pending job.
func (m *Manager) Create(kind Kind, pkg string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()

	j := &Job{
		ID:          newID(),
		Kind:        kind,
		Package:     pkg,
		Status:      StatusPending,
		CreatedAt:   time.Now().UTC(),
		maxLogLines: m.maxLogLines,
		done:        make(chan struct{}),
	}
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)

	// Evict oldest finished jobs beyond the retention cap.
	for len(m.order) > m.maxJobs {
		oldest := m.jobs[m.order[0]]
		if oldest == nil || oldest.Status == StatusPending || oldest.Status == StatusRunning {
			break
		}
		delete(m.jobs, m.order[0])
		m.order = m.order[1:]
	}
	return j
}

// Get returns a job by ID.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List returns all retained jobs, oldest first.
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, 0, len(m.order))
	for _, id := range m.order {
		if j, ok := m.jobs[id]; ok {
			out = append(out, j)
		}
	}
	return out
}

// Start marks the job running.
func (j *Job) Start() {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now().UTC()
	j.StartedAt = &now
	j.Status = StatusRunning
}

// Succeed marks the job finished successfully with an optional result payload.
func (j *Job) Succeed(result any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now().UTC()
	j.FinishedAt = &now
	j.Status = StatusSucceeded
	j.Result = result
	close(j.done)
}

// Fail marks the job finished with an error.
func (j *Job) Fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now().UTC()
	j.FinishedAt = &now
	j.Status = StatusFailed
	j.Error = err.Error()
	close(j.done)
}

// Done closes when the job reaches a terminal state.
func (j *Job) Done() <-chan struct{} {
	return j.done
}

// Logs returns up to tail captured log entries (0 = all).
func (j *Job) Logs(tail int) []LogEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	logs := j.logs
	if tail > 0 && len(logs) > tail {
		logs = logs[len(logs)-tail:]
	}
	out := make([]LogEntry, len(logs))
	copy(out, logs)
	return out
}

func (j *Job) append(e LogEntry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.logs = append(j.logs, e)
	if overflow := len(j.logs) - j.maxLogLines; overflow > 0 {
		j.logs = append([]LogEntry{}, j.logs[overflow:]...)
	}
}

// Logger returns a slog.Logger that tees records into the job's log buffer
// while also forwarding them to next (the server-wide logger).
func (j *Job) Logger(next *slog.Logger) *slog.Logger {
	return slog.New(&teeHandler{job: j, next: next.Handler()})
}

type teeHandler struct {
	job   *Job
	next  slog.Handler
	attrs []slog.Attr
	group string
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := map[string]any{}
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		attrs[key] = a.Value.Any()
		return true
	})
	h.job.append(LogEntry{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   maps.Clone(attrs),
	})
	return h.next.Handle(ctx, r)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{job: h.job, next: h.next.WithAttrs(attrs), attrs: append(h.attrs, attrs...), group: h.group}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{job: h.job, next: h.next.WithGroup(name), attrs: h.attrs, group: name}
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never realistically fails; fall back to a timestamp.
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))
	}
	return hex.EncodeToString(b)
}
