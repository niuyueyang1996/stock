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

// Cancelled 判断当前任务是否已被取消（含取消请求或已置为 cancelled 状态）。
func (p *Progress) Cancelled() bool { return p.m.isCancelled(p.jobID) }

// Check 任务取消检查：若已取消返回 ErrCancelled，否则返回 nil（供任务函数周期调用）。
func (p *Progress) Check() error {
	if p.Cancelled() {
		return ErrCancelled
	}
	return nil
}

// 进度上报快捷入口（对齐 Python Progress 语义）：
// SetTotal 设置任务总步数（Total）；Step 标记当前进行中的步骤名；CompleteStep 完成一个步骤名。
func (p *Progress) SetTotal(total int)       { p.m.setTotal(p.jobID, total) }
func (p *Progress) Step(name string)         { p.m.setStep(p.jobID, name) }
func (p *Progress) CompleteStep(name string) { p.m.completeStep(p.jobID, name) }

var ErrCancelled = &CancelError{}

type CancelError struct{}

// Error 返回取消错误的文本表示（实现 error 接口）。
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

// New 创建任务管理器，初始化 jobs/batches 表与两条 lane 的 worker 队列，并启动常驻 worker。
func New() *Manager {
	m := &Manager{
		jobs:    map[string]*Job{},
		batches: map[string]*Batch{},
		queues:  map[string]chan *Job{LaneRefresh: make(chan *Job, 256), LaneAI: make(chan *Job, 256)},
	}
	m.ensureWorkers()
	return m
}

// newID 生成 6 字节随机数的十六进制字符串作为任务/批次 ID。
func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// nowStr 返回当前时间的 "HH:MM:SS" 字符串（任务/批次时间戳）。
func nowStr() string { return time.Now().Format("15:04:05") }

// laneOf 依据任务 kind 前缀判定归属 lane：以 "ai." 开头走 AI 车道，否则走 refresh 车道。
func laneOf(kind string) string {
	if strings.HasPrefix(kind, "ai.") {
		return LaneAI
	}
	return LaneRefresh
}

// ensureWorkers 首次调用时为每条 lane 启动固定数量的常驻 worker goroutine（幂等，只启动一次）。
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

// workerLoop 单个 worker 主循环：从队列取任务，标记 running，执行后 finalize（直到队列被关闭）。
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

// runJob 执行任务的 Fn 回调：Fn 为空返回 nil；否则用带进度指针的 Progress 调用任务函数。
func (m *Manager) runJob(job *Job) error {
	if job.Fn == nil {
		return nil
	}
	return job.Fn(&Progress{jobID: job.ID, m: m})
}

// finalize 收尾一个任务：按取消/错误/完成设置终态、OK、完成度与时间戳，记入 recent 列表，
// 若属某个批次则尝试结束批次，最后强制推送终态快照到前端。
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
	// 最新完成的放最前（recent[0] 始终是最新；前端进度条/primary 兜底都按 [0] 取）
	m.recent = append([]RecentPublic{recentPublic(job)}, m.recent...)
	if len(m.recent) > recentMax {
		m.recent = m.recent[:recentMax]
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

// finishBatchIfDone 按批次子任务完成情况更新批次 DoneCount/Pct，全部完成时置批次为 done。
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

// Start 创建并排队一个独立任务：按 kind 分配 lane，注册进 jobs 表并入队，返回任务 ID。
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

// EnqueueBatch 创建一批子任务：每个 childLabels 项生成一个并入同一 batch 的任务，
// fn 按各子标签依次执行；返回批次 ID。
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

// Cancel 请求取消指定任务：标记 CancelRequested（由任务函数通过 Progress 感知）；任务不存在返回 false。
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

// CancelBatch 取消整批任务：给每种子任务标记取消请求并把批次状态置为 cancelled；批次不存在返回 false。
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

// recentPublic 把内部 Job 转为精简的 RecentPublic（空串/空字段归一为 nil）。
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

// jobPublic 把内部 Job 转为对外 JobPublic（Done 切片复制，system.prewarm 任务不可取消）。
func (m *Manager) jobPublic(j *Job) JobPublic {
	return JobPublic{
		JobID: j.ID, Kind: j.Kind, Label: j.Label, Lane: j.Lane, Status: j.Status,
		BatchID: j.BatchID, Step: j.Step, Done: append([]string{}, j.Done...),
		DoneCount: j.DoneCount, Current: j.Current, Total: j.Total, Pct: j.Pct,
		Error: j.Error, OK: j.OK, Meta: j.Meta, Cancellable: j.Kind != "system.prewarm",
	}
}

// Snapshot 构造对外推送/读取的任务全集快照：primary 任务（优先 batch，其次 refresh 车道 running，
// 再任意 running/queued，最后 recent）、各 lane 的 running/queue 列表、进行中的批次与最近任务。
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
		// 优先选 ai.* 类型（用户分析任务），避免 system.prewarm 抢占显示
		for _, j := range active {
			if strings.HasPrefix(j.Kind, "ai.") {
				primary = m.primaryOf(j)
				break
			}
		}
		if primary == nil {
			primary = m.primaryOf(active[0])
		}
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

// primaryOf 把单个运行中任务转为主任务呈现 map（running=true，含进度与批信息）。
func (m *Manager) primaryOf(j *Job) map[string]any {
	return map[string]any{
		"running": true, "job_id": j.ID, "kind": j.Kind, "label": j.Label,
		"step": j.Step, "done": append([]string{}, j.Done...), "done_count": j.DoneCount,
		"current": j.Current, "total": j.Total, "pct": j.Pct,
		"error": strOrNil(j.Error), "ok": j.OK, "batch_id": j.BatchID,
	}
}

// batchPublic 把内部 Batch 转为对外 BatchPublic：汇总 running/queued 子任务标签并给出当前执行子任务。
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

// jobIDOf 取 Job 的 ID，nil 返回 nil。
func jobIDOf(j *Job) any {
	if j == nil {
		return nil
	}
	return j.ID
}

// kindOf 取 Job 的 Kind，nil 返回空串。
func kindOf(j *Job) string {
	if j == nil {
		return ""
	}
	return j.Kind
}

// labelOf 取 Job 的 Label，nil 返回空串。
func labelOf(j *Job) string {
	if j == nil {
		return ""
	}
	return j.Label
}

// errOf 取 Job 的错误信息，nil 或空串返回 nil。
func errOf(j *Job) any {
	if j == nil || j.Error == "" {
		return nil
	}
	return j.Error
}

// batchIDOf 取 Job 的 BatchID，nil 或空串返回 nil。
func batchIDOf(j *Job) any {
	if j == nil || j.BatchID == "" {
		return nil
	}
	return j.BatchID
}

// jobIDOf2 取 RecentPublic 的 JobID，nil 返回 nil。
func jobIDOf2(j *RecentPublic) any {
	if j == nil {
		return nil
	}
	return j.JobID
}

// kindOf2 取 RecentPublic 的 Kind，nil 返回空串。
func kindOf2(j *RecentPublic) string {
	if j == nil {
		return ""
	}
	return j.Kind
}

// labelOf2 取 RecentPublic 的 Label，nil 返回空串。
func labelOf2(j *RecentPublic) string {
	if j == nil {
		return ""
	}
	return j.Label
}

// errOf2 取 RecentPublic 的错误，nil 返回 nil。
func errOf2(j *RecentPublic) any {
	if j == nil || j.Error == nil {
		return nil
	}
	return j.Error
}

// batchIDOf2 取 RecentPublic 的 BatchID，nil 返回 nil。
func batchIDOf2(j *RecentPublic) any {
	if j == nil || j.BatchID == nil {
		return nil
	}
	return j.BatchID
}

// strOrNil 空字符串转 nil，否则原样返回。
func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Prewarm 入队一个系统启动预热任务：steps **并发**执行 fn 并上报进度（各步骤互不依赖，
// 如市场列表/指数/汇率/除权；并发可显著缩短预热总时长）。返回任务 ID。
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
		var wg sync.WaitGroup
		var mu sync.Mutex
		var firstErr error
		for _, s := range steps {
			wg.Add(1)
			go func(s string) {
				defer wg.Done()
				p.Step(s)
				if err := fn(s); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				p.CompleteStep(s)
			}(s)
		}
		wg.Wait()
		return firstErr
	}
	m.jobs[id] = job
	m.queues[job.Lane] <- job
	return id
}

// PrewarmSnapshot 构造预热/当前任务的精简快照：优先读 prewarm 任务本身，否则读任意 running 任务。
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

// IsRefreshBusy 判断 refresh 车道是否有排队或运行中的任务（用于刷新互斥/忙碌判断）。
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

// isCancelled 检查任务是否已取消（取消请求或状态为 cancelled）。
func (m *Manager) isCancelled(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	return ok && (j.CancelRequested || j.Status == StatusCanceled)
}

// setTotal 设置运行中任务的总步数（Total，最小 1）并复位进度为 0。
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

// setStep 更新运行中任务的当前步骤名、时间戳与占比（进行中步骤按 (已完成+1)/Total 计，上限 99%），
// 并节流推送快照。
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

// completeStep 记录已完成步骤名（去重）、更新已完成计数/占比（上限 99%）与时间戳，并节流推送快照。
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
