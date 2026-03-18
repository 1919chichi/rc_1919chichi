# 架构设计文档

> 快速启动、API 示例、工程决策与取舍等见 [README](./README.md)

## 1. 系统概述

**API 通知系统**是一个轻量级的 HTTP 通知中间件，用于接收业务系统提交的外部 HTTP 通知请求，并异步、可靠地投递到目标地址。

### 核心能力

| 能力 | 说明 |
|------|------|
| 异步解耦 | 业务系统提交后立即返回 `202 Accepted`，投递在后台执行 |
| 可靠投递 | SQLite 持久化，进程重启不丢任务 |
| 失败重试 | 指数退避策略（10s → 30s → 90s），避免瞬时压垮外部系统 |
| 崩溃恢复 | `processing` 状态超时自动回收，保证无任务遗漏 |
| 死信与重放 | 超过重试上限后保留现场，支持手动重放 |

### 技术栈

- **语言**：Go 1.20+
- **HTTP**：Go 标准库 `net/http`（无第三方框架）
- **存储**：SQLite（`mattn/go-sqlite3`）
- **外部依赖**：仅 `go-sqlite3` 一个

---

## 2. 分层架构

```
┌─────────────────────────────────────────────────────┐
│                     main.go                         │
│            程序入口 / 依赖组装 / 优雅关闭              │
└──────────┬────────────────────────┬─────────────────┘
           │                        │
           ▼                        ▼
┌──────────────────┐     ┌────────────────────┐
│   handler 层      │     │    worker 层        │
│   (HTTP API)      │     │   (后台调度器)       │
│                   │     │                     │
│ • 请求校验        │     │ • 定时轮询          │
│ • 路由分发        │     │ • HTTP 投递         │
│ • 响应序列化      │     │ • 重试 / 死信       │
└────────┬─────────┘     └──────────┬──────────┘
         │                          │
         ▼                          ▼
┌───────────────────────────────────────────────┐
│                  store 层                      │
│              (数据持久化)                       │
│                                                │
│ • Job CRUD                                     │
│ • 事务抢占（乐观锁）                             │
│ • 状态流转                                      │
└────────────────────┬──────────────────────────┘
                     │
                     ▼
┌───────────────────────────────────────────────┐
│              SQLite (WAL 模式)                  │
│              jobs 表                            │
└───────────────────────────────────────────────┘
```

各层职责严格隔离：`handler` 和 `worker` 互不依赖，均通过 `store` 操作数据。`model` 作为共享的数据定义层，被所有包引用。

---

## 3. 包结构

```
.
├── main.go                          # 入口：依赖组装、HTTP Server、优雅关闭
├── go.mod                           # 模块声明与依赖
├── internal/
│   ├── model/
│   │   └── job.go                   # 数据模型、状态常量、退避算法
│   ├── handler/
│   │   ├── notification.go          # HTTP API 处理逻辑
│   │   └── notification_test.go     # Handler 单元测试
│   ├── store/
│   │   ├── sqlite.go                # SQLite 持久化实现
│   │   └── sqlite_test.go           # Store 单元测试
│   └── worker/
│       └── dispatcher.go            # 后台轮询调度与 HTTP 投递
├── README.md
├── ARCHITECTURE.md                  # 本文档
└── ai-usage.md                      # AI 辅助开发说明
```

### 包依赖关系

```
main ──→ handler ──→ store ──→ model
  │                    ↑
  └──→ worker ─────────┘
```

所有业务代码放在 `internal/` 下，Go 编译器保证外部项目无法导入，实现包级封装。

---

## 4. 核心数据模型

### 4.1 Job 结构

```go
type Job struct {
    ID          int64             // 主键，自增
    URL         string            // 投递目标 URL
    Method      string            // HTTP 方法 (GET/POST/PUT/PATCH/DELETE)
    Headers     map[string]string // 自定义请求头
    Body        string            // 请求体（完全透传）
    Status      JobStatus         // 状态机当前状态
    RetryCount  int               // 已重试次数
    MaxRetries  int               // 最大重试次数（默认 3）
    NextRetryAt time.Time         // 下次可重试时间
    LastError   string            // 最近一次失败原因
    CreatedAt   time.Time         // 创建时间
    UpdatedAt   time.Time         // 最后更新时间
}
```

### 4.2 状态机

```
              创建
               │
               ▼
         ┌──────────┐
         │ pending   │◄──────────────────────┐
         └────┬──────┘                       │
              │ FetchPendingJobs             │ MarkRetry
              │ (事务抢占)                    │ (重试次数未超限)
              ▼                              │
         ┌──────────┐          失败          │
         │processing│────────────────────────┘
         └────┬──────┘
              │
         ┌────┴────┐
         │         │
      成功(2xx)   失败(超限)
         │         │
         ▼         ▼
   ┌──────────┐ ┌──────────┐
   │completed │ │ failed   │
   └──────────┘ └─────┬────┘
                      │ Replay (手动重放)
                      │
                      ▼
                 ┌──────────┐
                 │ pending  │ (重置 retry_count=0)
                 └──────────┘
```

| 状态 | 含义 | 流转条件 |
|------|------|----------|
| `pending` | 等待投递 | 新建 / 重试回退 / 手动重放 |
| `processing` | 正在投递 | 被 Worker 抢占 |
| `completed` | 投递成功 | HTTP 响应 2xx |
| `failed` | 最终失败 | 重试次数耗尽 |

---

## 5. API 设计

### 5.1 端点列表

| 方法 | 路径 | 说明 | 响应码 |
|------|------|------|--------|
| `POST` | `/api/notifications` | 创建通知任务 | `202` / `400` |
| `GET` | `/api/notifications/{id}` | 查询单个任务 | `200` / `404` |
| `GET` | `/api/notifications/failed` | 列出所有失败任务 | `200` |
| `POST` | `/api/notifications/{id}/replay` | 重放失败任务 | `200` / `400` |
| `GET` | `/health` | 健康检查 | `200` |

### 5.2 创建通知（核心接口）

**请求**

```json
POST /api/notifications
Content-Type: application/json

{
  "url": "https://vendor.com/api/event",
  "method": "POST",
  "headers": {
    "Authorization": "Bearer xxx",
    "Content-Type": "application/json"
  },
  "body": "{\"user_id\": 123, \"event\": \"registered\"}"
}
```

**响应**

```json
HTTP/1.1 202 Accepted

{
  "message": "notification enqueued",
  "job": {
    "id": 1,
    "url": "https://vendor.com/api/event",
    "method": "POST",
    "status": "pending",
    ...
  }
}
```

### 5.3 输入校验规则

| 校验项 | 规则 |
|--------|------|
| 请求体大小 | ≤ 1MB (`MaxBytesReader`) |
| JSON 格式 | 禁止未知字段 (`DisallowUnknownFields`) |
| 单对象 | 请求体只能包含一个 JSON 对象 |
| URL | 必填，必须为合法的 `http`/`https` URL |
| Method | 必填，自动转大写，白名单：`GET/POST/PUT/PATCH/DELETE` |
| Headers | key 不能为空字符串 |

---

## 6. 数据流

### 6.1 任务投递流程

```
业务系统                   HTTP API                   Store (SQLite)              Worker
   │                         │                            │                        │
   │  POST /api/notifications│                            │                        │
   │────────────────────────>│                            │                        │
   │                         │  校验 URL/Method/Headers   │                        │
   │                         │                            │                        │
   │                         │  CreateJob(req)            │                        │
   │                         │───────────────────────────>│  INSERT INTO jobs      │
   │                         │                            │  status = 'pending'    │
   │                         │  返回 Job                  │                        │
   │                         │<───────────────────────────│                        │
   │  202 Accepted           │                            │                        │
   │<────────────────────────│                            │                        │
   │                         │                            │                        │
   │                         │                            │  每 2 秒轮询           │
   │                         │                            │<───────────────────────│
   │                         │                            │  FetchPendingJobs      │
   │                         │                            │  事务内乐观锁抢占      │
   │                         │                            │  status → 'processing' │
   │                         │                            │───────────────────────>│
   │                         │                            │                        │
   │                         │                            │              ┌─────────┤
   │                         │                            │              │ doHTTP  │
   │                         │                            │              │ Request │
   │                         │                            │              └────┬────┘
   │                         │                            │                   │
   │                         │                            │  2xx 成功:         │
   │                         │                            │  MarkCompleted    │
   │                         │                            │<──────────────────│
   │                         │                            │                   │
   │                         │                            │  失败 & 未超限:    │
   │                         │                            │  MarkRetry        │
   │                         │                            │<──────────────────│
   │                         │                            │                   │
   │                         │                            │  失败 & 超限:      │
   │                         │                            │  MarkFailed       │
   │                         │                            │<──────────────────│
```

### 6.2 崩溃恢复流程

当 Worker 在投递过程中崩溃，任务会停留在 `processing` 状态。恢复机制：

1. `FetchPendingJobs` 在查询时额外匹配 `status = 'processing' AND updated_at <= staleBefore`
2. `staleBefore = now - processingTimeout`（默认 1 分钟）
3. 超时的 `processing` 任务被重新抢占并投递

---

## 7. 数据库设计

### 7.1 表结构

```sql
CREATE TABLE IF NOT EXISTS jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    url           TEXT    NOT NULL,
    method        TEXT    NOT NULL DEFAULT 'POST',
    headers       TEXT    NOT NULL DEFAULT '{}',       -- JSON 格式
    body          TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'pending',   -- pending/processing/completed/failed
    retry_count   INTEGER NOT NULL DEFAULT 0,
    max_retries   INTEGER NOT NULL DEFAULT 3,
    next_retry_at INTEGER NOT NULL DEFAULT 0,           -- Unix 时间戳
    last_error    TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL DEFAULT 0,           -- Unix 时间戳
    updated_at    INTEGER NOT NULL DEFAULT 0            -- Unix 时间戳
);
```

### 7.2 索引

| 索引 | 列 | 用途 |
|------|-----|------|
| `idx_jobs_status_next_retry` | `(status, next_retry_at)` | Worker 轮询查询优化 |
| `idx_jobs_status_updated_at` | `(status, updated_at)` | 崩溃恢复查询 + 失败列表排序 |

### 7.3 设计决策

| 决策 | 原因 |
|------|------|
| 时间用 Unix 整数 | 避免 SQLite DATETIME 文本格式与 Go 驱动之间的解析兼容问题 |
| Headers 用 JSON TEXT | 灵活支持任意 KV 对，无需额外表 |
| WAL 模式 | 提升读写并发性能，API 写入与 Worker 读取互不阻塞 |
| `_busy_timeout=5000` | 数据库锁等待 5 秒，避免 `SQLITE_BUSY` 错误 |

---

## 8. Worker 调度策略

### 8.1 参数配置

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `interval` | 2s | 轮询间隔 |
| `batch` | 10 | 单次最大抢占任务数 |
| `processingTimeout` | 1min | 超时回收阈值 |
| `http.Client.Timeout` | 15s | 单次 HTTP 请求超时 |

### 8.2 任务抢占机制（乐观锁）

```
事务开始
  │
  ├─ SELECT id FROM jobs WHERE (pending & 到期) OR (processing & 超时)
  │  ORDER BY next_retry_at ASC, id ASC LIMIT batch
  │
  ├─ 逐条 UPDATE status='processing' WHERE id=? AND (原条件仍满足)
  │  └─ affected == 1 → 抢占成功
  │  └─ affected == 0 → 已被其他 Worker 抢占，跳过
  │
  └─ COMMIT
```

通过在 `UPDATE` 中重复检查原条件，实现类似 CAS 的乐观锁，保证并发安全。

### 8.3 指数退避策略

```
公式：delay = 10s × 3^retryCount
```

| retryCount | 等待时间 | 累计耗时 |
|------------|----------|----------|
| 0 | 10s | 10s |
| 1 | 30s | 40s |
| 2 | 90s | 130s |
| ≥ MaxRetries(3) | — | 标记 failed |

---

## 9. 启动与关闭

### 9.1 启动流程

```
main()
  │
  ├─ 1. store.New(dbPath)           初始化 SQLite，执行 migration
  │
  ├─ 2. worker.New(db)              创建 Dispatcher
  │     └─ go dispatcher.Start(ctx) 后台 goroutine 轮询
  │
  ├─ 3. handler.New(db)             创建 Handler
  │     └─ RegisterRoutes(mux)      注册路由
  │
  ├─ 4. go srv.ListenAndServe()     后台 goroutine 监听 HTTP
  │
  └─ 5. signal.Notify(quit, ...)    阻塞等待退出信号
```

### 9.2 优雅关闭

```
收到 SIGINT/SIGTERM
  │
  ├─ 1. cancel()                    通知 Worker 停止轮询
  │                                 Worker 处理完当前批次后退出
  │
  ├─ 2. srv.Shutdown(5s timeout)    停止接受新请求
  │                                 等待已有请求完成（最多 5 秒）
  │
  └─ 3. db.Close()                  关闭数据库连接（defer）
```

---

## 10. 配置

通过环境变量配置，无配置文件：

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| `DB_PATH` | `notifications.db` | SQLite 数据库文件路径 |
| `ADDR` | `:8080` | HTTP 监听地址 |

---

## 11. 错误处理策略

| 层 | 策略 |
|----|------|
| **handler** | 返回结构化 JSON 错误响应，区分 `400`/`404`/`405`/`500` |
| **store** | 用 `fmt.Errorf("%w")` 包装错误向上传播，JSON 解析容错返回空 map |
| **worker** | 日志记录 + 状态流转（MarkRetry / MarkFailed），不 panic |
| **main** | `log.Fatalf` 处理不可恢复的初始化错误 |

---

## 12. 测试覆盖

| 文件 | 测试内容 |
|------|----------|
| `handler/notification_test.go` | 路由严格匹配（404）、Method 自动大写、拒绝未知 JSON 字段 |
| `store/sqlite_test.go` | 超时 processing 任务回收、无效 Headers JSON 容错 |

测试使用临时数据库文件，每个测试用例独立初始化，确保隔离性。

---

## 13. 未来演进

```
当前 (MVP)                          演进方向
─────────────────────────────────────────────────────
SQLite 单文件           ──→  MySQL/PostgreSQL (水平扩展)
DB 轮询队列             ──→  Redis / RabbitMQ (更高吞吐)
单进程 Worker           ──→  多实例部署 (乐观锁已支持)
无中间件                ──→  日志 / 认证 / CORS / 限流
无监控                  ──→  Prometheus 指标 + Grafana
CLI 管理                ──→  Web 管理界面
```
