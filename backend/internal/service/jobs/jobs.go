package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	LaneRefresh = "refresh"
	LaneAI      = "ai"
	refreshN    = 6
	aiN         = 8
	recentMax   = 24
)

type Status string

const (
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusDone     Status = "done"
	StatusError    Status = "error"
	StatusCanceled Status = "cancelled"
)

type Job struct {
	ID              string
	Kind            string
	Label           string
	Lane            string
	Status          Status
	BatchID         string
	Step            string
	Done            []string
	DoneCount       int
	Current         int
	Total           int
	Pct             int
	Error           string
	OK              *bool
	Meta            map[string]any
	CancelRequested bool
	UpdatedAt       string
	Fn              func(p *Progress) error
}

type Batch struct {
	ID        string
	Kind      string
	Label     string
	Status    Status
	ChildIDs  []string
	Total     int
	DoneCount int
	Pct       int
}

type Progress struct {
	jobID string
	m     *Manager
}

func (p *Progress) Cancelled() bool { return p.m.isCancelled(p.jobID) }

func (p *Progress) Check() error {
	if p.Cancelled() {
		return ErrCancelled
	}
	return nil
}

func (p *Progress) SetTotal(total int)       { p.m.setTotal(p.jobID, total) }
func (p *Progress) Step(name string)         { p.m.setStep(p.jobID, name) }
func (p *Progress) CompleteStep(name string) { p.m.completeStep(p.jobID, name) }

var ErrCancelled = &CancelError{}

type CancelError struct{}

func (*CancelError) Error() string { return "job cancelled" }

type Manager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	batches map[string]*Batch
	recent  []*Job
	queues  map[string]chan *Job
	started bool
	prewarm string
}

func New() *Manager {
	m := &Manager{
		jobs:    map[string]*Job{},
		batches: map[string]*Batch{},
		queues:  map[string]chan *Job{LaneRefresh: make(chan *Job, 256), LaneAI: make(chan *Job, 256)},
	}
	m.ensureWorkers()
	return m
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowStr() string { return time.Now().Format("15:04:05") }

func laneOf(kind string) string {
	if strings.HasPrefix(kind, "ai.") {
		return LaneAI
	}
	return LaneRefresh
}

func (m *Manager) ensureWorkers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return
	}
	m.started = true
	for lane, n := range map[string]int{LaneRefresh: refreshN, LaneAI: aiN} {
		q := m.queues[lane]
		for i := 0; i < n; i++ {
			go m.workerLoop(lane, q)
		}
	}
}

func (m *Manager) workerLoop(lane string, q chan *Job) {
	for job := range q {
		m.mu.Lock()
		job.Status = StatusRunning
		job.UpdatedAt = nowStr()
		m.mu.Unlock()
		err := m.runJob(job)
		m.finalize(job, err)
	}
}

func (m *Manager) runJob(job *Job) error {
	if job.Fn == nil {
		return nil
	}
	return job.Fn(&Progress{jobID: job.ID, m: m})
}

func (m *Manager) finalize(job *Job, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job.CancelRequested || (err != nil && errors.Is(err, ErrCancelled)) {
		job.Status = StatusCanceled
		if err != nil {
			job.Error = err.Error()
		}
	} else if err != nil {
		job.Status = StatusError
		job.Error = err.Error()
	} else {
		job.Status = StatusDone
	}
	ok := job.Status == StatusDone
	job.OK = &ok
	job.Pct = 100
	job.UpdatedAt = nowStr()
	m.recent = append(m.recent, job)
	if len(m.recent) > recentMax {
		m.recent = m.recent[len(m.recent)-recentMax:]
	}
	if job.BatchID != "" {
		m.finishBatchIfDone(job.BatchID)
	}
}

func (m *Manager) finishBatchIfDone(batchID string) {
	b, ok := m.batches[batchID]
	if !ok {
		return
	}
	done := 0
	for _, id := range b.ChildIDs {
		if j, ok := m.jobs[id]; ok {
			if j.Status == StatusDone || j.Status == StatusError || j.Status == StatusCanceled {
				done++
			}
		}
	}
	b.DoneCount = done
	if b.Total > 0 {
		b.Pct = done * 100 / b.Total
		if b.Pct >= 100 {
			b.Pct = 100
		}
	}
	if done >= b.Total {
		b.Status = StatusDone
	}
}

func (m *Manager) Start(kind, label string, fn func(p *Progress) error) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := &Job{
		ID: newID(), Kind: kind, Label: label, Lane: laneOf(kind),
		Status: StatusQueued, Total: 1, Pct: 0, Meta: map[string]any{}, Fn: fn,
		UpdatedAt: nowStr(),
	}
	m.jobs[job.ID] = job
	m.queues[job.Lane] <- job
	return job.ID
}

func (m *Manager) EnqueueBatch(kind, label string, childLabels []string, fn func(label string, p *Progress) error) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := &Batch{ID: newID(), Kind: kind, Label: label, Status: StatusRunning, Total: len(childLabels)}
	m.batches[b.ID] = b
	for _, cl := range childLabels {
		job := &Job{
			ID: newID(), Kind: kind, Label: cl, Lane: laneOf(kind),
			Status: StatusQueued, BatchID: b.ID, Total: 1,
			Meta: map[string]any{}, UpdatedAt: nowStr(),
		}
		jobLabel := cl
		job.Fn = func(p *Progress) error { return fn(jobLabel, p) }
		b.ChildIDs = append(b.ChildIDs, job.ID)
		m.jobs[job.ID] = job
		m.queues[job.Lane] <- job
	}
	return b.ID
}

func (m *Manager) Cancel(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok {
		return false
	}
	j.CancelRequested = true
	return true
}

func (m *Manager) CancelBatch(batchID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[batchID]
	if !ok {
		return false
	}
	for _, id := range b.ChildIDs {
		if j, ok := m.jobs[id]; ok {
			j.CancelRequested = true
		}
	}
	b.Status = StatusCanceled
	return true
}

type JobPublic struct {
	JobID       string         `json:"job_id"`
	Kind        string         `json:"kind"`
	Label       string         `json:"label"`
	Lane        string         `json:"lane"`
	Status      Status         `json:"status"`
	BatchID     string         `json:"batch_id,omitempty"`
	Step        string         `json:"step"`
	Done        []string       `json:"done"`
	DoneCount   int            `json:"done_count"`
	Current     int            `json:"current"`
	Total       int            `json:"total"`
	Pct         int            `json:"pct"`
	Error       string         `json:"error,omitempty"`
	OK          *bool          `json:"ok,omitempty"`
	Meta        map[string]any `json:"meta"`
	Cancellable bool           `json:"cancellable"`
}

type BatchPublic struct {
	BatchID      string   `json:"batch_id"`
	Kind         string   `json:"kind"`
	Label        string   `json:"label"`
	Status       Status   `json:"status"`
	DoneCount    int      `json:"done_count"`
	Total        int      `json:"total"`
	Pct          int      `json:"pct"`
	Running      []string `json:"running"`
	Queued       []string `json:"queued"`
	CurrentLabel string   `json:"current_label"`
}

func (m *Manager) jobPublic(j *Job) JobPublic {
	return JobPublic{
		JobID: j.ID, Kind: j.Kind, Label: j.Label, Lane: j.Lane, Status: j.Status,
		BatchID: j.BatchID, Step: j.Step, Done: append([]string{}, j.Done...),
		DoneCount: j.DoneCount, Current: j.Current, Total: j.Total, Pct: j.Pct,
		Error: j.Error, OK: j.OK, Meta: j.Meta, Cancellable: j.Kind != "system.prewarm",
	}
}

func (m *Manager) Snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var running, queued []JobPublic
	for _, j := range m.jobs {
		switch j.Status {
		case StatusRunning:
			running = append(running, m.jobPublic(j))
		case StatusQueued:
			queued = append(queued, m.jobPublic(j))
		}
	}
	sort.Slice(running, func(i, jj int) bool { return running[i].JobID < running[jj].JobID })
	sort.Slice(queued, func(i, jj int) bool { return queued[i].JobID < queued[jj].JobID })
	var recents []JobPublic
	for _, j := range m.recent {
		recents = append(recents, m.jobPublic(j))
	}
	var batches []BatchPublic
	for _, b := range m.batches {
		bp := BatchPublic{BatchID: b.ID, Kind: b.Kind, Label: b.Label, Status: b.Status,
			DoneCount: b.DoneCount, Total: b.Total, Pct: b.Pct}
		for _, id := range b.ChildIDs {
			if j, ok := m.jobs[id]; ok {
				if j.Status == StatusRunning {
					bp.Running = append(bp.Running, j.Label)
				} else if j.Status == StatusQueued {
					bp.Queued = append(bp.Queued, j.Label)
				}
			}
		}
		if len(bp.Running) > 0 {
			bp.CurrentLabel = bp.Running[0]
		} else if len(bp.Queued) > 0 {
			bp.CurrentLabel = bp.Queued[0]
		}
		batches = append(batches, bp)
	}
	step := ""
	var done []string
	doneCount, current, total, pct := 0, 0, 1, 0
	runningAny := len(running) > 0
	if len(running) > 0 {
		r := running[0]
		step, done, doneCount, current, total, pct = r.Step, r.Done, r.DoneCount, r.Current, r.Total, r.Pct
	}
	return map[string]any{
		"running": runningAny, "step": step, "done": done,
		"done_count": doneCount, "current": current, "total": total, "pct": pct,
		"updated_at": nowStr(), "jobs": running, "queued": queued, "recent": recents, "batches": batches,
	}
}

func (m *Manager) Prewarm(steps []string, fn func(step string) error) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := newID()
	m.prewarm = id
	job := &Job{
		ID: id, Kind: "system.prewarm", Label: "启动预热", Lane: LaneRefresh,
		Status: StatusQueued, Total: len(steps), Meta: map[string]any{}, UpdatedAt: nowStr(),
	}
	job.Fn = func(p *Progress) error {
		p.SetTotal(len(steps))
		for _, s := range steps {
			p.Step(s)
			if err := fn(s); err != nil {
				return err
			}
			p.CompleteStep(s)
		}
		return nil
	}
	m.jobs[id] = job
	m.queues[job.Lane] <- job
	return id
}

func (m *Manager) PrewarmSnapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 读 prewarm 任务本身（无论状态），非预热时读当前任务
	var j *Job
	if id := m.prewarm; id != "" {
		j = m.jobs[id]
	}
	if j == nil {
		for _, x := range m.jobs {
			if x.Status == StatusRunning {
				j = x
				break
			}
		}
	}
	if j == nil {
		return map[string]any{"running": false, "step": "", "done": []string{}, "done_count": 0,
			"current": 0, "total": 1, "pct": 0, "updated_at": nowStr()}
	}
	running := j.Status == StatusRunning || j.Status == StatusQueued
	return map[string]any{
		"running": running, "step": j.Step, "done": append([]string{}, j.Done...),
		"done_count": j.DoneCount, "current": j.Current, "total": j.Total,
		"pct": j.Pct, "updated_at": j.UpdatedAt,
	}
}

func (m *Manager) IsRefreshBusy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.Lane == LaneRefresh && (j.Status == StatusRunning || j.Status == StatusQueued) {
			return true
		}
	}
	return false
}

func (m *Manager) isCancelled(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	return ok && (j.CancelRequested || j.Status == StatusCanceled)
}

func (m *Manager) setTotal(jobID string, total int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok || j.Status != StatusRunning {
		return
	}
	if total < 1 {
		total = 1
	}
	j.Total = total
	j.Pct = 0
}

func (m *Manager) setStep(jobID, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok || j.Status != StatusRunning {
		return
	}
	j.Step = name
	j.UpdatedAt = nowStr()
	if j.Total > 0 {
		j.Pct = (len(j.Done) + 1) * 100 / j.Total
		if j.Pct > 99 {
			j.Pct = 99
		}
	}
}

func (m *Manager) completeStep(jobID, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok || j.Status != StatusRunning {
		return
	}
	exists := false
	for _, d := range j.Done {
		if d == name {
			exists = true
			break
		}
	}
	if !exists {
		j.Done = append(j.Done, name)
	}
	j.Step = ""
	j.DoneCount = len(j.Done)
	if j.Total > 0 {
		j.Pct = j.DoneCount * 100 / j.Total
		if j.Pct > 99 {
			j.Pct = 99
		}
	}
	j.UpdatedAt = nowStr()
}
