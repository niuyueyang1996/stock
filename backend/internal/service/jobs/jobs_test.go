package jobs

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func waitStatus(t *testing.T, m *Manager, id string, want Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		j, ok := m.jobs[id]
		m.mu.Unlock()
		if ok && j.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if ok {
		t.Fatalf("job %s status = %s, want %s (err=%s)", id, j.Status, want, j.Error)
	}
	t.Fatalf("job %s not found", id)
}

func TestStartDone(t *testing.T) {
	m := New()
	ran := false
	id := m.Start("test.kind", "测试任务", func(p *Progress) error {
		ran = true
		p.SetTotal(2)
		p.Step("step1")
		p.CompleteStep("step1")
		return nil
	})
	waitStatus(t, m, id, StatusDone)
	if !ran {
		t.Fatal("任务未执行")
	}
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j.OK == nil || !*j.OK {
		t.Fatalf("ok = %v", j.OK)
	}
	if len(j.Done) != 1 || j.Done[0] != "step1" {
		t.Fatalf("done = %v", j.Done)
	}
}

func TestStartError(t *testing.T) {
	m := New()
	id := m.Start("test.err", "失败任务", func(p *Progress) error {
		return errors.New("boom")
	})
	waitStatus(t, m, id, StatusError)
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j.Error != "boom" {
		t.Fatalf("error = %q", j.Error)
	}
}

func TestBatch(t *testing.T) {
	m := New()
	var mu sync.Mutex
	count := 0
	bid := m.EnqueueBatch("test.batch", "批量", []string{"a", "b", "c"}, func(label string, p *Progress) error {
		mu.Lock()
		count++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		b, ok := m.batches[bid]
		done := b != nil && b.Status == StatusDone
		m.mu.Unlock()
		if ok && done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count != 3 {
		t.Fatalf("批量执行数 = %d", count)
	}
	m.mu.Lock()
	b := m.batches[bid]
	m.mu.Unlock()
	if b == nil || b.Status != StatusDone || b.Pct != 100 {
		t.Fatalf("batch = %+v", b)
	}
}

func TestCancel(t *testing.T) {
	m := New()
	id := m.Start("test.slow", "慢任务", func(p *Progress) error {
		for i := 0; i < 100; i++ {
			if err := p.Check(); err != nil {
				return err
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	})
	time.Sleep(30 * time.Millisecond)
	if !m.Cancel(id) {
		t.Fatal("cancel 失败")
	}
	waitStatus(t, m, id, StatusCanceled)
}

func TestPrewarm(t *testing.T) {
	m := New()
	var order []string
	id := m.Prewarm([]string{"step1", "step2"}, func(step string) error {
		order = append(order, step)
		return nil
	})
	waitStatus(t, m, id, StatusDone)
	if len(order) != 2 || order[0] != "step1" {
		t.Fatalf("order = %v", order)
	}
	snap := m.PrewarmSnapshot()
	if snap["done_count"].(int) != 2 {
		t.Fatalf("prewarm done_count = %v", snap["done_count"])
	}
}
