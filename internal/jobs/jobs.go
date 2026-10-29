package jobs

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

type Job struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	State     State     `json:"state"`
	Progress  float64   `json:"progress"`
	Message   string    `json:"message,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	cancel    context.CancelFunc
}

type Manager struct {
	mu   sync.RWMutex
	jobs map[string]*Job
	next uint64
}

func NewManager() *Manager { return &Manager{jobs: make(map[string]*Job)} }

func (m *Manager) Start(kind string, run func(context.Context, func(float64, string)) error) Job {
	m.mu.Lock()
	m.next++
	id := fmt.Sprintf("job-%06d", m.next)
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	job := &Job{ID: id, Type: kind, State: Queued, CreatedAt: now, UpdatedAt: now, cancel: cancel}
	m.jobs[id] = job
	m.mu.Unlock()
	go func() {
		m.update(id, func(j *Job) { j.State = Running })
		err := run(ctx, func(p float64, msg string) { m.update(id, func(j *Job) { j.Progress = p; j.Message = msg }) })
		m.update(id, func(j *Job) {
			if ctx.Err() != nil {
				j.State = Cancelled
				j.Message = "Cancelled"
			} else if err != nil {
				j.State = Failed
				j.Error = err.Error()
			} else {
				j.State = Succeeded
				j.Progress = 1
			}
		})
	}()
	return m.Get(id)
}
func (m *Manager) update(id string, update func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.jobs[id]; j != nil {
		update(j)
		j.UpdatedAt = time.Now().UTC()
	}
}
func (m *Manager) Get(id string) Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if j := m.jobs[id]; j != nil {
		c := *j
		c.cancel = nil
		return c
	}
	return Job{}
}
func (m *Manager) List() []Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		c := *j
		c.cancel = nil
		result = append(result, c)
	}
	return result
}
func (m *Manager) Cancel(id string) bool {
	m.mu.RLock()
	j := m.jobs[id]
	m.mu.RUnlock()
	if j == nil || j.cancel == nil {
		return false
	}
	j.cancel()
	return true
}
func (m *Manager) CancelAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, j := range m.jobs {
		if j.cancel != nil {
			j.cancel()
		}
	}
}
