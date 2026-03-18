// package store —— 数据持久化层，负责所有数据库操作
package store

import (
	"database/sql" // Go 标准库的数据库接口（类似 JDBC），定义了通用的 SQL 操作接口
	"encoding/json" // JSON 编解码
	"fmt"           // 格式化输出（类似 printf）
	"log"           // 日志
	"strings"       // 字符串操作
	"time"          // 时间

	"github.com/1919chichi/rc_1919chichi/internal/model"
	// _ "github.com/mattn/go-sqlite3" —— 下划线导入（blank import）
	// 只执行该包的 init() 函数来注册 SQLite 驱动，不直接使用该包的任何导出符号
	// 这是 Go database/sql 驱动注册的标准模式
	_ "github.com/mattn/go-sqlite3"
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
	// 反引号 ` 包裹的是原始字符串（raw string），可以跨多行，不需要转义
	query := `
	CREATE TABLE IF NOT EXISTS jobs (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
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
	`
	// s.db.Exec 执行 SQL 语句（不返回结果行）
	_, err := s.db.Exec(query)
	return err
}

// CreateJob 在数据库中创建一个新的通知任务
func (s *Store) CreateJob(req model.CreateNotificationRequest) (*model.Job, error) {
	headers := req.Headers
	if headers == nil {
		// map[string]string{} 创建一个空的 map（字典）
		headers = map[string]string{}
	}

	// json.Marshal 将 Go 数据结构序列化为 JSON 字节切片
	headersJSON, err := json.Marshal(headers)
	if err != nil {
		return nil, fmt.Errorf("marshal headers: %w", err)
	}

	// time.Now().Unix() 获取当前时间的 Unix 时间戳（秒数）
	now := time.Now().Unix()
	// s.db.Exec 执行 INSERT SQL 语句
	// ? 是参数占位符，防止 SQL 注入（后面的参数按顺序替换 ?）
	result, err := s.db.Exec(
		`INSERT INTO jobs (url, method, headers, body, status, max_retries, next_retry_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.URL, req.Method, string(headersJSON), req.Body,
		model.StatusPending, model.DefaultMaxRetries, now, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	// result.LastInsertId() 获取刚插入行的自增 ID
	id, _ := result.LastInsertId()
	// 插入后再查询一次，返回完整的 Job 对象
	return s.GetJob(id)
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

// GetJob 根据 ID 查询单个任务
func (s *Store) GetJob(id int64) (*model.Job, error) {
	// QueryRow 执行查询并返回最多一行结果
	row := s.db.QueryRow(
		`SELECT id, url, method, headers, body, status, retry_count, max_retries,
		        next_retry_at, last_error, created_at, updated_at
		 FROM jobs WHERE id = ?`, id)
	// 调用辅助函数将数据库行扫描为 Job 结构体
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

// ListFailedJobs 查询所有失败的任务，按更新时间倒序排列
func (s *Store) ListFailedJobs() ([]model.Job, error) {
	// Query 执行查询并返回多行结果（与 QueryRow 的区别）
	rows, err := s.db.Query(
		`SELECT id, url, method, headers, body, status, retry_count, max_retries,
		        next_retry_at, last_error, created_at, updated_at
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

// scanJob 将数据库查询结果的一行扫描（读取）为 Job 结构体
func scanJob(row scannable) (*model.Job, error) {
	var j model.Job                        // 声明一个 Job 变量（零值初始化）
	var headersStr, status string          // 数据库中 headers 和 status 以字符串存储
	var nextRetry, created, updated int64  // 数据库中时间以 Unix 时间戳（整数）存储

	// row.Scan 按顺序将查询结果的各列读取到对应变量中
	// 每个参数都必须是指针（&变量名）
	err := row.Scan(&j.ID, &j.URL, &j.Method, &headersStr, &j.Body, &status,
		&j.RetryCount, &j.MaxRetries, &nextRetry, &j.LastError, &created, &updated)
	if err != nil {
		return nil, err
	}

	// 将数据库中的原始值转换为 Go 类型
	j.Status = model.JobStatus(status)              // 类型转换：string -> JobStatus
	j.Headers = decodeHeaders(headersStr)            // JSON 字符串 -> map
	j.NextRetryAt = time.Unix(nextRetry, 0).UTC()   // Unix 时间戳 -> time.Time
	j.CreatedAt = time.Unix(created, 0).UTC()
	j.UpdatedAt = time.Unix(updated, 0).UTC()
	return &j, nil // 返回 Job 的指针
}

// scanJobFromRows 功能同 scanJob，但接收 *sql.Rows 类型
// 这是因为 *sql.Rows 和 *sql.Row 的 Scan 方法签名略有不同
func scanJobFromRows(rows *sql.Rows) (*model.Job, error) {
	var j model.Job
	var headersStr, status string
	var nextRetry, created, updated int64

	err := rows.Scan(&j.ID, &j.URL, &j.Method, &headersStr, &j.Body, &status,
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

// decodeHeaders 将 JSON 字符串解析为 map[string]string
// 如果解析失败（如数据库中存了无效 JSON），返回空 map 而不是报错
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
