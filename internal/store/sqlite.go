// package store —— 数据持久化层，负责所有数据库操作
package store

import (
	"database/sql" // Go 标准库的数据库接口（类似 JDBC），定义了通用的 SQL 操作接口
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/1919chichi/rc_1919chichi/internal/model"
	"github.com/mattn/go-sqlite3"
)

// Store 封装了数据库连接，提供所有数据库操作方法
type Store struct {
	db *sql.DB // *sql.DB 是 Go 数据库连接池对象（不是单个连接）
}

// New 创建并初始化 Store
// 返回 (*Store, error) —— Go 的多返回值模式，第二个值是可能的错误
func New(dbPath string) (*Store, error) {
	// sql.Open 打开数据库连接（实际上创建连接池）
	// 第一个参数 "sqlite3" 是驱动名，第二个是数据源名称（DSN）
	// ?_journal_mode=WAL 启用 WAL 模式提升并发读写性能
	// &_busy_timeout=5000 数据库忙时最多等待 5 秒
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		// fmt.Errorf 创建格式化的错误信息
		// %w 是包装错误（wrap），保留原始错误链，方便上层用 errors.Is/As 判断
		return nil, fmt.Errorf("open db: %w", err)
	}
	// db.Ping() 实际尝试连接数据库，验证连接是否可用
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	// &Store{db: db} 创建 Store 实例并返回其指针
	s := &Store{db: db}
	// 运行数据库迁移（创建表等）
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close 关闭数据库连接
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate 执行数据库迁移：创建所需的表和索引
// 小写字母开头的函数/方法是私有的（unexported），只能在包内访问
// 大写字母开头的才是公开的（exported），类似其他语言的 public/private
func (s *Store) migrate() error {
	query := `
	CREATE TABLE IF NOT EXISTS jobs (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		vendor_id     TEXT    NOT NULL DEFAULT '',
		event         TEXT    NOT NULL DEFAULT '',
		biz_id        TEXT    NOT NULL DEFAULT '',
		url           TEXT    NOT NULL,
		method        TEXT    NOT NULL DEFAULT 'POST',
		headers       TEXT    NOT NULL DEFAULT '{}',
		body          TEXT    NOT NULL DEFAULT '',
		status        TEXT    NOT NULL DEFAULT 'pending',
		retry_count   INTEGER NOT NULL DEFAULT 0,
		max_retries   INTEGER NOT NULL DEFAULT 3,
		next_retry_at INTEGER NOT NULL DEFAULT 0,
		last_error    TEXT    NOT NULL DEFAULT '',
		created_at    INTEGER NOT NULL DEFAULT 0,
		updated_at    INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_jobs_status_next_retry ON jobs(status, next_retry_at);
	CREATE INDEX IF NOT EXISTS idx_jobs_status_updated_at ON jobs(status, updated_at);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_biz_key ON jobs(vendor_id, event, biz_id) WHERE biz_id != '';

	CREATE TABLE IF NOT EXISTS vendors (
		id          TEXT PRIMARY KEY,
		name        TEXT    NOT NULL,
		base_url    TEXT    NOT NULL,
		method      TEXT    NOT NULL DEFAULT 'POST',
		auth_type   TEXT    NOT NULL DEFAULT '',
		auth_config TEXT    NOT NULL DEFAULT '{}',
		headers     TEXT    NOT NULL DEFAULT '{}',
		body_tpl    TEXT    NOT NULL DEFAULT '',
		max_retries INTEGER NOT NULL DEFAULT 3,
		is_active   INTEGER NOT NULL DEFAULT 1,
		created_at  INTEGER NOT NULL DEFAULT 0,
		updated_at  INTEGER NOT NULL DEFAULT 0
	);
	`
	if _, err := s.db.Exec(query); err != nil {
		return err
	}

	for _, q := range []string{
		"ALTER TABLE jobs ADD COLUMN vendor_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE jobs ADD COLUMN event TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE jobs ADD COLUMN biz_id TEXT NOT NULL DEFAULT ''",
	} {
		s.db.Exec(q)
	}
	s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_biz_key ON jobs(vendor_id, event, biz_id) WHERE biz_id != ''`)
	return nil
}

// CreateJob persists a fully-resolved notification job.
// Returns (job, isNew, error). When an identical biz_key already exists,
// isNew is false and the existing job is returned without creating a duplicate.
func (s *Store) CreateJob(p model.CreateJobParams) (*model.Job, bool, error) {
	headers := p.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	headersJSON, err := json.Marshal(headers)
	if err != nil {
		return nil, false, fmt.Errorf("marshal headers: %w", err)
	}

	maxRetries := p.MaxRetries
	if maxRetries <= 0 {
		maxRetries = model.DefaultMaxRetries
	}

	now := time.Now().Unix()
	result, err := s.db.Exec(
		`INSERT INTO jobs (vendor_id, event, biz_id, url, method, headers, body, status, max_retries, next_retry_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.VendorID, p.Event, p.BizID, p.URL, p.Method, string(headersJSON), p.Body,
		model.StatusPending, maxRetries, now, now, now,
	)
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			existing, findErr := s.GetJobByBizKey(p.VendorID, p.Event, p.BizID)
			if findErr != nil {
				return nil, false, fmt.Errorf("find existing job: %w", findErr)
			}
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("insert job: %w", err)
	}

	id, _ := result.LastInsertId()
	job, err := s.GetJob(id)
	if err != nil {
		return nil, false, err
	}
	return job, true, nil
}

// FetchPendingJobs 原子性地抢占最多 limit 个待投递的任务
// 选取条件：
//   1. 状态为 pending 且 next_retry_at 已到期的任务
//   2. 状态为 processing 但太久没更新的任务（崩溃恢复机制）
func (s *Store) FetchPendingJobs(limit int, processingTimeout time.Duration) ([]model.Job, error) {
	if limit <= 0 {
		return nil, nil // 返回 nil 切片表示"无结果"
	}

	now := time.Now().Unix()
	// 计算"过期时间点"：如果一个 processing 状态的任务在此时间点之前最后更新，视为卡住了
	staleBefore := now - int64(processingTimeout/time.Second)
	if processingTimeout <= 0 {
		staleBefore = now
	}

	// s.db.Begin() 开启一个数据库事务（Transaction）
	// 事务保证多个 SQL 操作的原子性：要么全部成功，要么全部回滚
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	// defer 在函数返回时执行 Rollback
	// 如果后面成功 Commit 了，Rollback 会变成空操作（no-op）
	defer func() { _ = tx.Rollback() }()

	// tx.Query 在事务中执行 SELECT 查询
	// 先查出符合条件的任务 ID 列表
	rows, err := tx.Query(
		`SELECT id FROM jobs
		 WHERE (status = ? AND next_retry_at <= ?)
		    OR (status = ? AND updated_at <= ?)
		 ORDER BY next_retry_at ASC, id ASC
		 LIMIT ?`,
		model.StatusPending, now, model.StatusProcessing, staleBefore, limit,
	)
	if err != nil {
		return nil, err
	}

	// var ids []int64 —— 声明一个 int64 切片（slice）
	// 切片是 Go 中的动态数组，类似 Python 的 list 或 Java 的 ArrayList
	var ids []int64
	// rows.Next() 逐行迭代查询结果，类似数据库游标
	for rows.Next() {
		var id int64
		// rows.Scan(&id) 将当前行的数据读取到变量中
		// 必须传指针（&id），这样 Scan 才能修改变量的值
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		// append 向切片追加元素（Go 内置函数）
		ids = append(ids, id)
	}
	rows.Close() // 关闭结果集释放资源
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(ids) == 0 {
		return nil, nil
	}

	// make([]int64, 0, len(ids)) —— 创建一个初始长度为 0、容量为 len(ids) 的切片
	// 提前指定容量可以减少动态扩容的开销
	claimedIDs := make([]int64, 0, len(ids))
	// 逐个尝试将任务状态从 pending/processing 更新为 processing（乐观锁模式）
	for _, id := range ids {
		// for _, id := range ids —— range 遍历切片
		// _ 忽略索引，id 是当前元素的值
		result, err := tx.Exec(
			`UPDATE jobs
			 SET status = ?, updated_at = ?
			 WHERE id = ?
			   AND (
					(status = ? AND next_retry_at <= ?)
				 OR (status = ? AND updated_at <= ?)
			   )`,
			model.StatusProcessing, now, id,
			model.StatusPending, now,
			model.StatusProcessing, staleBefore,
		)
		if err != nil {
			return nil, err
		}
		// RowsAffected 返回被更新的行数
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		// 只有确实更新了 1 行的 ID 才算抢占成功
		if affected == 1 {
			claimedIDs = append(claimedIDs, id)
		}
	}

	// tx.Commit() 提交事务，使所有修改生效
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if len(claimedIDs) == 0 {
		return nil, nil
	}

	// 查询已抢占的任务完整信息并返回
	jobs := make([]model.Job, 0, len(claimedIDs))
	for _, id := range claimedIDs {
		job, err := s.GetJob(id)
		if err != nil {
			return nil, err
		}
		// *job —— 解引用指针，获取指针指向的值
		// 这里将 *model.Job（指针指向的结构体）追加到切片中
		jobs = append(jobs, *job)
	}
	return jobs, nil
}

func (s *Store) GetJob(id int64) (*model.Job, error) {
	row := s.db.QueryRow(
		`SELECT id, vendor_id, event, biz_id, url, method, headers, body, status,
		        retry_count, max_retries, next_retry_at, last_error, created_at, updated_at
		 FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

// GetJobByBizKey finds an existing job by its business dedup key.
func (s *Store) GetJobByBizKey(vendorID, event, bizID string) (*model.Job, error) {
	row := s.db.QueryRow(
		`SELECT id, vendor_id, event, biz_id, url, method, headers, body, status,
		        retry_count, max_retries, next_retry_at, last_error, created_at, updated_at
		 FROM jobs WHERE vendor_id = ? AND event = ? AND biz_id = ?`, vendorID, event, bizID)
	return scanJob(row)
}

// MarkCompleted 将任务标记为投递成功
func (s *Store) MarkCompleted(id int64) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
		model.StatusCompleted, now, id,
	)
	return err
}

// MarkRetry 将任务标记为待重试，增加重试计数并设置下次重试时间
func (s *Store) MarkRetry(id int64, nextRetryAt time.Time) error {
	now := time.Now().Unix()
	// retry_count = retry_count + 1 直接在 SQL 中自增
	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, retry_count = retry_count + 1, next_retry_at = ?, updated_at = ? WHERE id = ?`,
		model.StatusPending, nextRetryAt.Unix(), now, id,
	)
	return err
}

// MarkFailed 将任务标记为最终失败，记录最后一次错误信息
func (s *Store) MarkFailed(id int64, lastError string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		model.StatusFailed, lastError, now, id,
	)
	return err
}

func (s *Store) ListFailedJobs() ([]model.Job, error) {
	rows, err := s.db.Query(
		`SELECT id, vendor_id, event, biz_id, url, method, headers, body, status,
		        retry_count, max_retries, next_retry_at, last_error, created_at, updated_at
		 FROM jobs WHERE status = ? ORDER BY updated_at DESC`, model.StatusFailed,
	)
	if err != nil {
		return nil, err
	}
	// defer rows.Close() 确保结果集最终被关闭
	defer rows.Close()

	var jobs []model.Job
	for rows.Next() {
		job, err := scanJobFromRows(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, nil
}

// ResetJob 重放一个失败的任务：将状态重置为 pending，清零重试计数
func (s *Store) ResetJob(id int64) (*model.Job, error) {
	now := time.Now().Unix()
	result, err := s.db.Exec(
		`UPDATE jobs SET status = ?, retry_count = 0, next_retry_at = ?, last_error = '', updated_at = ? WHERE id = ? AND status = ?`,
		model.StatusPending, now, now, id, model.StatusFailed,
	)
	if err != nil {
		return nil, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		// 没有匹配的行 -> 任务不存在或不在 failed 状态
		return nil, fmt.Errorf("job %d not found or not in failed status", id)
	}
	return s.GetJob(id)
}

// scannable 是一个接口（interface）
// Go 的接口是隐式实现的：只要一个类型实现了接口中的所有方法，就自动满足该接口
// 这里 *sql.Row 和 *sql.Rows 都有 Scan 方法，所以都满足 scannable 接口
// 这让 scanJob 函数可以同时处理单行查询和多行查询的结果
type scannable interface {
	Scan(dest ...any) error // ...any 表示接受任意数量、任意类型的参数
}

func scanJob(row scannable) (*model.Job, error) {
	var j model.Job
	var headersStr, status string
	var nextRetry, created, updated int64

	err := row.Scan(&j.ID, &j.VendorID, &j.Event, &j.BizID,
		&j.URL, &j.Method, &headersStr, &j.Body, &status,
		&j.RetryCount, &j.MaxRetries, &nextRetry, &j.LastError, &created, &updated)
	if err != nil {
		return nil, err
	}

	j.Status = model.JobStatus(status)
	j.Headers = decodeHeaders(headersStr)
	j.NextRetryAt = time.Unix(nextRetry, 0).UTC()
	j.CreatedAt = time.Unix(created, 0).UTC()
	j.UpdatedAt = time.Unix(updated, 0).UTC()
	return &j, nil
}

func scanJobFromRows(rows *sql.Rows) (*model.Job, error) {
	var j model.Job
	var headersStr, status string
	var nextRetry, created, updated int64

	err := rows.Scan(&j.ID, &j.VendorID, &j.Event, &j.BizID,
		&j.URL, &j.Method, &headersStr, &j.Body, &status,
		&j.RetryCount, &j.MaxRetries, &nextRetry, &j.LastError, &created, &updated)
	if err != nil {
		return nil, err
	}

	j.Status = model.JobStatus(status)
	j.Headers = decodeHeaders(headersStr)
	j.NextRetryAt = time.Unix(nextRetry, 0).UTC()
	j.CreatedAt = time.Unix(created, 0).UTC()
	j.UpdatedAt = time.Unix(updated, 0).UTC()
	return &j, nil
}

// --------------- Vendor CRUD ---------------

func (s *Store) GetVendor(id string) (*model.VendorConfig, error) {
	row := s.db.QueryRow(
		`SELECT id, name, base_url, method, auth_type, auth_config, headers,
		        body_tpl, max_retries, is_active, created_at, updated_at
		 FROM vendors WHERE id = ?`, id)
	return scanVendor(row)
}

func (s *Store) ListVendors() ([]model.VendorConfig, error) {
	rows, err := s.db.Query(
		`SELECT id, name, base_url, method, auth_type, auth_config, headers,
		        body_tpl, max_retries, is_active, created_at, updated_at
		 FROM vendors ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vendors []model.VendorConfig
	for rows.Next() {
		v, err := scanVendorFromRows(rows)
		if err != nil {
			return nil, err
		}
		vendors = append(vendors, *v)
	}
	return vendors, nil
}

func (s *Store) CreateVendor(req model.CreateVendorRequest) (*model.VendorConfig, error) {
	authCfg, err := json.Marshal(nonNilMap(req.AuthConfig))
	if err != nil {
		return nil, fmt.Errorf("marshal auth_config: %w", err)
	}
	hdrs, err := json.Marshal(nonNilMap(req.Headers))
	if err != nil {
		return nil, fmt.Errorf("marshal headers: %w", err)
	}

	method := req.Method
	if method == "" {
		method = "POST"
	}
	maxRetries := req.MaxRetries
	if maxRetries <= 0 {
		maxRetries = model.DefaultMaxRetries
	}

	now := time.Now().Unix()
	_, err = s.db.Exec(
		`INSERT INTO vendors (id, name, base_url, method, auth_type, auth_config, headers, body_tpl, max_retries, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		req.ID, req.Name, req.BaseURL, method, req.AuthType,
		string(authCfg), string(hdrs), req.BodyTpl, maxRetries, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert vendor: %w", err)
	}
	return s.GetVendor(req.ID)
}

func (s *Store) UpdateVendor(id string, req model.UpdateVendorRequest) (*model.VendorConfig, error) {
	authCfg, err := json.Marshal(nonNilMap(req.AuthConfig))
	if err != nil {
		return nil, fmt.Errorf("marshal auth_config: %w", err)
	}
	hdrs, err := json.Marshal(nonNilMap(req.Headers))
	if err != nil {
		return nil, fmt.Errorf("marshal headers: %w", err)
	}

	method := req.Method
	if method == "" {
		method = "POST"
	}
	maxRetries := req.MaxRetries
	if maxRetries <= 0 {
		maxRetries = model.DefaultMaxRetries
	}

	now := time.Now().Unix()
	result, err := s.db.Exec(
		`UPDATE vendors SET name=?, base_url=?, method=?, auth_type=?, auth_config=?, headers=?, body_tpl=?, max_retries=?, updated_at=?
		 WHERE id = ?`,
		req.Name, req.BaseURL, method, req.AuthType,
		string(authCfg), string(hdrs), req.BodyTpl, maxRetries, now, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update vendor: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, fmt.Errorf("vendor %q not found", id)
	}
	return s.GetVendor(id)
}

func (s *Store) DeleteVendor(id string) error {
	now := time.Now().Unix()
	result, err := s.db.Exec(
		`UPDATE vendors SET is_active = 0, updated_at = ? WHERE id = ? AND is_active = 1`, now, id)
	if err != nil {
		return fmt.Errorf("delete vendor: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("vendor %q not found or already inactive", id)
	}
	return nil
}

func scanVendor(row scannable) (*model.VendorConfig, error) {
	var v model.VendorConfig
	var authCfgStr, headersStr string
	var isActive int
	var created, updated int64

	err := row.Scan(&v.ID, &v.Name, &v.BaseURL, &v.Method, &v.AuthType,
		&authCfgStr, &headersStr, &v.BodyTpl, &v.MaxRetries, &isActive, &created, &updated)
	if err != nil {
		return nil, err
	}

	v.AuthConfig = decodeHeaders(authCfgStr)
	v.Headers = decodeHeaders(headersStr)
	v.IsActive = isActive == 1
	v.CreatedAt = time.Unix(created, 0).UTC()
	v.UpdatedAt = time.Unix(updated, 0).UTC()
	return &v, nil
}

func scanVendorFromRows(rows *sql.Rows) (*model.VendorConfig, error) {
	var v model.VendorConfig
	var authCfgStr, headersStr string
	var isActive int
	var created, updated int64

	err := rows.Scan(&v.ID, &v.Name, &v.BaseURL, &v.Method, &v.AuthType,
		&authCfgStr, &headersStr, &v.BodyTpl, &v.MaxRetries, &isActive, &created, &updated)
	if err != nil {
		return nil, err
	}

	v.AuthConfig = decodeHeaders(authCfgStr)
	v.Headers = decodeHeaders(headersStr)
	v.IsActive = isActive == 1
	v.CreatedAt = time.Unix(created, 0).UTC()
	v.UpdatedAt = time.Unix(updated, 0).UTC()
	return &v, nil
}

// SeedDefaultVendors inserts built-in vendor configurations on first startup.
// Uses INSERT OR IGNORE so manually-modified vendors are never overwritten.
func (s *Store) SeedDefaultVendors() error {
	vendors := []struct {
		id, name, baseURL string
	}{
		{"ad_system", "广告系统", "https://example.com/ad/callback"},
		{"crm_system", "CRM 系统", "https://example.com/crm/webhook"},
		{"inventory_system", "库存系统", "https://example.com/inventory/webhook"},
	}

	now := time.Now().Unix()
	for _, v := range vendors {
		_, err := s.db.Exec(
			`INSERT OR IGNORE INTO vendors (id, name, base_url, method, auth_type, auth_config, headers, body_tpl, max_retries, is_active, created_at, updated_at)
			 VALUES (?, ?, ?, 'POST', '', '{}', '{}', '', 3, 1, ?, ?)`,
			v.id, v.name, v.baseURL, now, now,
		)
		if err != nil {
			return fmt.Errorf("seed vendor %q: %w", v.id, err)
		}
	}
	return nil
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// --------------- Helpers ---------------

func decodeHeaders(headersStr string) map[string]string {
	if strings.TrimSpace(headersStr) == "" {
		return map[string]string{}
	}

	var headers map[string]string
	// json.Unmarshal 将 JSON 字节切片解析为 Go 数据结构
	// []byte(headersStr) 将字符串转换为字节切片
	if err := json.Unmarshal([]byte(headersStr), &headers); err != nil {
		log.Printf("[store] invalid headers JSON %q: %v", headersStr, err)
		return map[string]string{}
	}
	if headers == nil {
		return map[string]string{}
	}
	return headers
}
