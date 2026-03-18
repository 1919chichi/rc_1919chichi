// package store —— 存储层的测试文件
package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/1919chichi/rc_1919chichi/internal/model"
)

// newTestStore 是测试辅助函数，创建一个临时的 Store 实例用于测试
func newTestStore(t *testing.T) *Store {
	t.Helper() // 标记为辅助函数，错误报告时显示调用方行号

	// filepath.Join 跨平台地拼接文件路径
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	// 测试结束后自动关闭数据库连接
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

// 测试场景：FetchPendingJobs 能否回收"卡住的"processing 状态任务
// 模拟一个任务被抢占后worker崩溃的情况——该任务应在超时后被重新抢占
func TestFetchPendingJobs_ReclaimsStaleProcessing(t *testing.T) {
	s := newTestStore(t)

	// 第一步：创建一个测试任务
	created, err := s.CreateJob(model.CreateNotificationRequest{
		URL:    "https://example.com/hook",
		Method: "POST",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// 第二步：首次抢占——应成功抢到 1 个任务
	first, err := s.FetchPendingJobs(10, time.Minute)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 claimed job, got %d", len(first))
	}
	if first[0].ID != created.ID {
		t.Fatalf("unexpected job id: got %d want %d", first[0].ID, created.ID)
	}

	// 第三步：再次抢占——任务已是 processing 状态且未超时，应抢到 0 个
	second, err := s.FetchPendingJobs(10, time.Minute)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("expected 0 claimed jobs, got %d", len(second))
	}

	// 第四步：人为将任务的 updated_at 设为 2 分钟前，模拟"worker 崩溃导致任务卡住"
	staleAt := time.Now().Add(-2 * time.Minute).Unix()
	// s.db 在测试文件中可以直接访问（因为测试文件和被测文件在同一个包中）
	if _, err := s.db.Exec(
		`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
		model.StatusProcessing, staleAt, created.ID,
	); err != nil {
		t.Fatalf("set stale processing: %v", err)
	}

	// 第五步：再次抢占——超时的 processing 任务应被回收，抢到 1 个
	reclaimed, err := s.FetchPendingJobs(10, time.Minute)
	if err != nil {
		t.Fatalf("reclaim fetch: %v", err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("expected 1 reclaimed job, got %d", len(reclaimed))
	}
	if reclaimed[0].ID != created.ID {
		t.Fatalf("unexpected reclaimed job id: got %d want %d", reclaimed[0].ID, created.ID)
	}
}

// 测试场景：当数据库中的 headers 字段包含无效 JSON 时，应优雅降级为空 map
func TestGetJob_InvalidHeaderJSONFallsBackToEmptyMap(t *testing.T) {
	s := newTestStore(t)

	// 先正常创建一个任务
	created, err := s.CreateJob(model.CreateNotificationRequest{
		URL:    "https://example.com/hook",
		Method: "POST",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// 直接通过 SQL 将 headers 字段改为无效 JSON，模拟数据损坏
	if _, err := s.db.Exec(`UPDATE jobs SET headers = ? WHERE id = ?`, "{invalid json", created.ID); err != nil {
		t.Fatalf("corrupt headers: %v", err)
	}

	// 查询该任务——应成功返回，且 headers 为空 map 而非报错
	job, err := s.GetJob(created.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if len(job.Headers) != 0 {
		t.Fatalf("expected empty headers on invalid JSON, got: %#v", job.Headers)
	}
}
