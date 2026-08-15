package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	recent  []RecentPublic
	queues  map[string]chan *Job
	started bool
	prewarm string

	// WebSocket 推送钩子（可选，nil 则跳过）：
	// force=true 表示任务终态（完成/失败/取消），必须送达前端，跳过节流。
	OnBroadcast func(data map[string]any, force bool)
	// 推送节流状态（独立于 mu，避免回调持锁调用）
	pushMu   sync.Mutex
	lastPush time.Time
}

const pushInterval = 300 * time.Millisecond // 进度推送节流窗口（对齐 Python 0.3s）

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
	m.recent = append(m.recent, recentPublic(job))
	if len(m.recent) > recentMax {
		m.recent = m.recent[len(m.recent)-recentMax:]
	}
	if job.BatchID != "" {
		m.finishBatchIfDone(job.BatchID)
	}
	m.mu.Unlock()
	// 终态必须送达前端（force 跳过节流）
	m.notify(true)
}

// notify 广播任务快照（节流）：先在节流锁内决定是否推送，再取快照并回调。
// 注意：OnBroadcast 绝不能在持 m.mu 时调用（回调内会再进 hub 广播并发访问），
// 因此这里先解锁 m.mu、由调用者保证已释放锁后才进入 notify。
func (m *Manager) notify(force bool) {
	if m.OnBroadcast == nil {
		return
	}
	now := time.Now()
	m.pushMu.Lock()
	if !force && now.Sub(m.lastPush) < pushInterval {
		m.pushMu.Unlock()
		return
	}
	m.lastPush = now
	m.pushMu.Unlock()
	// 已释放 m.mu：Slapshot 内部重新加锁取快照，解锁后再回调
	data := m.Snapshot()
	if m.OnBroadcast != nil {
		m.OnBroadcast(data, force)
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

// BatchChild 批量子任务描述：相比 EnqueueBatch 仅传 label，额外携带 Kind 与 Meta（供刷新扇出按股定位）。
type BatchChild struct {
	Kind  string
	Label string
	Meta  map[string]any
}

// EnqueueBatchWithMeta EnqueueBatch 的最小扩展：每个子任务可带独立 Kind 与 Meta。
// BatchID 对子任务与一个可选的收尾 job 相同——把收尾 job 并入同一 batch，保证 batch 在收尾完成后才结束。
// fn 收到该子任务的 BatchChild（含 Meta），便于在扇出中直接定位 code。
func (m *Manager) EnqueueBatchWithMeta(kind, label string, children []BatchChild, fn func(child BatchChild, p *Progress) error) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 各子任务的 batch_id 用 batch 自身的 ID
	b := &Batch{ID: newID(), Kind: kind, Label: label, Status: StatusRunning, Total: len(children)}
	m.batches[b.ID] = b
	for _, ch := range children {
		job := &Job{
			ID: newID(), Kind: ch.Kind, Label: ch.Label, Lane: laneOf(kind),
			Status: StatusQueued, BatchID: b.ID, Total: 1,
			Meta: ch.Meta, UpdatedAt: nowStr(),
		}
		c := ch
		job.Fn = func(p *Progress) error { return fn(c, p) }
		b.ChildIDs = append(b.ChildIDs, job.ID)
		m.jobs[job.ID] = job
		m.queues[job.Lane] <- job
	}
	return b.ID
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

// RecentPublic 最近任务精简结构（对齐 Python _recent 元素）
type RecentPublic struct {
	JobID   string `json:"job_id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Status  Status `json:"status"`
	OK      *bool  `json:"ok"`
	Error   any    `json:"error"`
	BatchID any    `json:"batch_id"`
}

func recentPublic(j *Job) RecentPublic {
	return RecentPublic{
		JobID: j.ID, Kind: j.Kind, Label: j.Label, Status: j.Status,
		OK: j.OK, Error: strOrNil(j.Error), BatchID: batchIDOf(j),
	}
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
	var active []*Job
	for _, j := range m.jobs {
		if j.Status == StatusQueued || j.Status == StatusRunning {
			active = append(active, j)
		}
	}
	// primary：优先 batch，其次 refresh 车道 running，其次任意 running/queued，最后 recent
	var primary map[string]any
	// batch 优先
	for _, b := range m.batches {
		if b.Status == StatusRunning {
			bp := m.batchPublic(b)
			primary = map[string]any{
				"running": true, "job_id": bp.BatchID, "kind": bp.Kind, "label": bp.Label,
				"step": bp.CurrentLabel, "done": []string{}, "done_count": bp.DoneCount,
				"current": bp.DoneCount, "total": bp.Total, "pct": bp.Pct,
				"error": nil, "ok": nil, "batch_id": bp.BatchID,
			}
			break
		}
	}
	if primary == nil {
		for _, lane := range []string{LaneRefresh, LaneAI} {
			for _, j := range active {
				if j.Lane == lane && j.Status == StatusRunning {
					primary = m.primaryOf(j)
					break
				}
			}
			if primary != nil {
				break
			}
		}
	}
	if primary == nil && len(active) > 0 {
		primary = m.primaryOf(active[0])
	}
	if primary == nil {
		var last *RecentPublic
		if len(m.recent) > 0 {
			r := m.recent[0]
			last = &r
		}
		okV := any(nil)
		if last != nil {
			okV = last.OK
		}
		pct := 0
		if last != nil && last.OK != nil && *last.OK {
			pct = 100
		}
		primary = map[string]any{
			"running": false, "job_id": jobIDOf2(last), "kind": kindOf2(last), "label": labelOf2(last),
			"step": "", "done": []string{}, "done_count": 0, "current": 0, "total": 1,
			"pct": pct, "error": errOf2(last), "ok": okV, "batch_id": batchIDOf2(last),
		}
	}

	laneSnap := func(lane string) map[string]any {
		var run, q []JobPublic
		for _, j := range active {
			if j.Lane != lane {
				continue
			}
			if j.Status == StatusRunning {
				run = append(run, m.jobPublic(j))
			} else if j.Status == StatusQueued {
				q = append(q, m.jobPublic(j))
			}
		}
		if run == nil {
			run = []JobPublic{}
		}
		if q == nil {
			q = []JobPublic{}
		}
		return map[string]any{"running": run, "queue": q}
	}
	var queue, jobsPub, batchesPub, recents []any
	for _, j := range active {
		jp := m.jobPublic(j)
		jobsPub = append(jobsPub, jp)
		if j.Status == StatusQueued {
			queue = append(queue, jp)
		}
	}
	for _, b := range m.batches {
		if b.Status == StatusRunning {
			batchesPub = append(batchesPub, m.batchPublic(b))
		}
	}
	for _, r := range m.recent {
		recents = append(recents, r)
	}
	if jobsPub == nil {
		jobsPub = []any{}
	}
	if queue == nil {
		queue = []any{}
	}
	if batchesPub == nil {
		batchesPub = []any{}
	}
	if recents == nil {
		recents = []any{}
	}
	for k, v := range primary {
		primary[k] = v
	}
	out := map[string]any{
		"running": primary["running"], "job_id": primary["job_id"],
		"kind": primary["kind"], "label": primary["label"], "step": primary["step"],
		"done": primary["done"], "done_count": primary["done_count"],
		"current": primary["current"], "total": primary["total"], "pct": primary["pct"],
		"error": primary["error"], "ok": primary["ok"], "batch_id": primary["batch_id"],
		"updated_at": nowStr(), "queue": queue, "jobs": jobsPub,
		"batches": batchesPub, "recent": recents,
		"lanes": map[string]any{LaneRefresh: laneSnap(LaneRefresh), LaneAI: laneSnap(LaneAI)},
	}
	return out
}

func (m *Manager) primaryOf(j *Job) map[string]any {
	return map[string]any{
		"running": true, "job_id": j.ID, "kind": j.Kind, "label": j.Label,
		"step": j.Step, "done": append([]string{}, j.Done...), "done_count": j.DoneCount,
		"current": j.Current, "total": j.Total, "pct": j.Pct,
		"error": strOrNil(j.Error), "ok": j.OK, "batch_id": j.BatchID,
	}
}

func (m *Manager) batchPublic(b *Batch) BatchPublic {
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
	return bp
}

func jobIDOf(j *Job) any {
	if j == nil {
		return nil
	}
	return j.ID
}
func kindOf(j *Job) string {
	if j == nil {
		return ""
	}
	return j.Kind
}
func labelOf(j *Job) string {
	if j == nil {
		return ""
	}
	return j.Label
}
func errOf(j *Job) any {
	if j == nil || j.Error == "" {
		return nil
	}
	return j.Error
}
func batchIDOf(j *Job) any {
	if j == nil || j.BatchID == "" {
		return nil
	}
	return j.BatchID
}
func jobIDOf2(j *RecentPublic) any {
	if j == nil {
		return nil
	}
	return j.JobID
}
func kindOf2(j *RecentPublic) string {
	if j == nil {
		return ""
	}
	return j.Kind
}
func labelOf2(j *RecentPublic) string {
	if j == nil {
		return ""
	}
	return j.Label
}
func errOf2(j *RecentPublic) any {
	if j == nil || j.Error == nil {
		return nil
	}
	return j.Error
}
func batchIDOf2(j *RecentPublic) any {
	if j == nil || j.BatchID == nil {
		return nil
	}
	return j.BatchID
}

func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
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
			"current": 0, "total": 1, "pct": 0, "updated_at": nowStr(),
			"kind": "", "label": "", "job_id": nil, "ok": nil, "error": nil}
	}
	running := j.Status == StatusRunning || j.Status == StatusQueued
	pct := j.Pct
	if !running && j.OK != nil && *j.OK {
		pct = 100
	}
	return map[string]any{
		"running": running, "step": j.Step, "done": append([]string{}, j.Done...),
		"done_count": j.DoneCount, "current": j.Current, "total": j.Total,
		"pct": pct, "updated_at": j.UpdatedAt,
		"kind": j.Kind, "label": j.Label, "job_id": j.ID, "ok": j.OK, "error": strOrNil(j.Error),
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
	j, ok := m.jobs[jobID]
	if !ok || j.Status != StatusRunning {
		m.mu.Unlock()
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
	m.mu.Unlock()
	// 进度变更 → 节流推送快照（300ms 合并）
	m.notify(false)
}

func (m *Manager) completeStep(jobID, name string) {
	m.mu.Lock()
	j, ok := m.jobs[jobID]
	if !ok || j.Status != StatusRunning {
		m.mu.Unlock()
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
	m.mu.Unlock()
	// 进度变更 → 节流推送快照（300ms 合并）
	m.notify(false)
}
