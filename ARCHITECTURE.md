# 架构设计文档

> 快速启动、API 示例、工程决策与取舍等见 [README](./README.md)

## 1. 系统概述

**API 通知系统**是一个轻量级的 HTTP 通知中间件，用于接收业务系统提交的外部 HTTP 通知请求，并异步、可靠地投递到目标地址。

### 核心能力

| 能力 | 说明 |
|------|------|
| 异步解耦 | 业务系统提交后立即返回 `202 Accepted`（重复提交返回 `200 OK`），投递在后台执行 |
| 可靠投递 | SQLite 持久化，进程重启不丢任务 |
| 失败重试 | 指数退避策略（10s → 30s → 90s），避免瞬时压垮外部系统 |
| 崩溃恢复 | `processing` 状态超时自动回收，保证无任务遗漏 |
| 死信与重放 | 超过重试上限后保留现场，支持手动重放 |
| Vendor 管理 | 集中管理外部供应商配置（URL、认证、模板），业务方只需指定 `vendor_id` + `event` + `biz_id` |
| 幂等去重 | 通过 `biz_id` 业务标识实现提交侧去重，相同请求不会创建重复 Job |

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
└──────────┬─────────────┬──────────┬─────────────────┘
           │             │          │
           ▼             │          ▼
┌──────────────────┐     │ ┌────────────────────┐
│   handler 层      │     │ │    worker 层        │
│   (HTTP API)      │     │ │   (后台调度器)       │
│                   │     │ │                     │
│ • 请求校验        │     │ │ • 定时轮询          │
│ • 路由分发        │     │ │ • HTTP 投递         │
│ • 响应序列化      │     │ │ • 重试 / 死信       │
└────────┬─────────┘     │ └──────────┬──────────┘
         │               │            │
         │    ┌──────────▼─────────┐  │
         │    │   adapter 层         │  │
         │    │  (供应商适配)        │  │
         │    │                     │  │
         │    │ • Registry 注册表   │  │
         │    │ • Adapter 适配器    │  │
         │    │ • 请求构建 / 模板   │  │
         │    └──────────┬─────────┘  │
         │               │            │
         ▼               ▼            ▼
┌───────────────────────────────────────────────┐
│                  store 层                      │
│              (数据持久化)                       │
│                                                │
│ • Job CRUD          • Vendor CRUD              │
│ • 事务抢占（乐观锁）                             │
│ • 状态流转                                      │
└────────────────────┬──────────────────────────┘
                     │
                     ▼
┌───────────────────────────────────────────────┐
│              SQLite (WAL 模式)                  │
│           jobs 表  /  vendors 表                │
└───────────────────────────────────────────────┘
```

各层职责严格隔离：`handler` 和 `worker` 互不依赖，均通过 `store` 操作数据。`adapter` 层负责将业务事件映射为具体的 HTTP 请求，由 `handler` 在创建通知时调用。`model` 作为共享的数据定义层，被所有包引用。

---

## 3. 包结构

```
.
├── main.go                          # 入口：依赖组装、HTTP Server、优雅关闭
├── go.mod                           # 模块声明与依赖
├── internal/
│   ├── model/
│   │   ├── job.go                   # Job 模型、状态常量、退避算法
│   │   ├── response.go              # 统一响应封装（Response / ListData）
│   │   └── vendor.go                # VendorConfig 模型、请求/响应结构
│   ├── handler/
│   │   ├── notification.go          # 通知相关 HTTP API
│   │   ├── response.go              # 统一响应辅助函数
│   │   ├── vendor.go                # Vendor CRUD HTTP API
│   │   └── notification_test.go     # Handler 单元测试
│   ├── store/
│   │   ├── sqlite.go                # SQLite 持久化（Job + Vendor）
│   │   └── sqlite_test.go           # Store 单元测试
│   ├── adapter/
│   │   ├── adapter.go               # VendorAdapter 接口与 ResolvedRequest 定义
│   │   ├── config_adapter.go        # 配置驱动适配器（模板渲染 + 认证注入）
│   │   ├── registry.go              # Vendor 注册表（代码 Adapter 优先，配置兜底）
│   │   └── adapter_test.go          # Adapter 单元测试
│   └── worker/
│       └── dispatcher.go            # 后台轮询调度与 HTTP 投递
├── README.md
├── ARCHITECTURE.md                  # 本文档
└── ai-usage.md                      # AI 辅助开发说明
```

### 包依赖关系

```
main ──→ handler ──→ adapter ──→ model
  │         │                    ↑
  │         └──→ store ──────────┘
  │               ↑
  └──→ worker ────┘
```

`handler` 在创建通知时调用 `adapter.Registry` 解析供应商配置、构建 HTTP 请求，然后通过 `store` 持久化。`adapter` 包通过 `VendorStore` 接口与 `store` 解耦，只依赖 `model`，由 `main.go` 在组装时注入具体实现。所有业务代码放在 `internal/` 下，Go 编译器保证外部项目无法导入，实现包级封装。

---

## 4. 核心数据模型

### 4.1 VendorConfig 结构

```go
type VendorConfig struct {
    ID         string            // 供应商唯一标识（如 "crm", "ad_system"）
    Name       string            // 供应商显示名称
    BaseURL    string            // 投递目标 URL
    Method     string            // HTTP 方法（默认 POST）
    AuthType   string            // 认证类型（如 "bearer", "api_key", "basic", ""）
    AuthConfig map[string]string // 认证配置（如 {"token": "xxx"}）
    Headers    map[string]string // 默认请求头
    BodyTpl    string            // 请求体模板（Go text/template 语法）
    MaxRetries int               // 最大重试次数（默认 3）
    IsActive   bool              // 是否启用（软删除标记）
    CreatedAt  time.Time         // 创建时间
    UpdatedAt  time.Time         // 更新时间
}
```

`VendorConfig` 集中管理外部供应商的投递配置。业务方创建通知时只需指定 `vendor_id`，系统自动从配置中解析出完整的 HTTP 请求参数。`BodyTpl` 支持 Go 模板语法，可在模板中引用 `.Event`、`.Payload` 和 `.Timestamp`（UTC RFC3339 格式）变量。

### 4.2 Job 结构

```go
type Job struct {
    ID          int64             // 主键，自增
    VendorID    string            // 关联的供应商 ID
    Event       string            // 业务事件名称（如 "user_registered"）
    BizID       string            // 业务去重标识（vendor_id + event + biz_id 唯一）
    URL         string            // 投递目标 URL（由 Vendor 解析得到）
    Method      string            // HTTP 方法（由 Vendor 解析得到）
    Headers     map[string]string // 请求头（由 Vendor 解析得到）
    Body        string            // 请求体（由 Vendor 模板渲染得到）
    Status      JobStatus         // 状态机当前状态
    RetryCount  int               // 已重试次数
    MaxRetries  int               // 最大重试次数（继承自 Vendor 配置）
    NextRetryAt time.Time         // 下次可重试时间
    LastError   string            // 最近一次失败原因
    CreatedAt   time.Time         // 创建时间
    UpdatedAt   time.Time         // 最后更新时间
}
```

`BizID` 是调用方传入的业务去重标识，与 `VendorID` + `Event` 组成唯一键，实现幂等投递——相同业务请求重复提交时返回已有 Job 而非创建新记录。`URL`、`Method`、`Headers`、`Body` 不再由调用方直接指定，而是通过 Vendor 适配器自动构建。

### 4.3 CreateNotificationRequest（创建通知请求体）

```go
type CreateNotificationRequest struct {
    VendorID string         `json:"vendor_id"` // 供应商 ID（必填）
    Event    string         `json:"event"`     // 业务事件名称（必填）
    BizID    string         `json:"biz_id"`    // 业务去重标识（必填）
    Payload  map[string]any `json:"payload"`   // 事件负载数据（传入模板渲染）
}
```

调用方不再需要关心目标 URL、HTTP 方法、认证头等细节——这些全部由 Vendor 配置和适配器负责组装。`biz_id` 用于幂等去重，相同的 `(vendor_id, event, biz_id)` 组合不会创建重复 Job。

### 4.4 CreateJobParams（内部参数，handler → store）

```go
type CreateJobParams struct {
    VendorID   string
    Event      string
    BizID      string            // 业务去重标识
    URL        string            // 由 Vendor 适配器解析
    Method     string            // 由 Vendor 适配器解析
    Headers    map[string]string // 由 Vendor 适配器解析
    Body       string            // 由模板渲染
    MaxRetries int               // 继承自 Vendor 配置
}
```

`Handler.Create` 先通过 `adapter.Registry` 解析出完整的 HTTP 请求参数，再构造 `CreateJobParams` 传给 `store.CreateJob`。

### 4.5 Response 统一响应封装

```go
type Response struct {
    Code    int    `json:"code"`    // 业务码：0 表示成功，非 0 为 HTTP 状态码
    Message string `json:"message"` // 描述信息
    Data    any    `json:"data"`    // 业务数据（错误时省略）
}
```

所有 API 端点统一使用 `Response` 信封返回数据，调用方只需解析一种结构。`Code` 为 `0` 表示成功，非零时等于 HTTP 状态码（如 `400`、`404`、`500`），便于程序化错误处理。

### 4.6 ListData 列表数据封装

```go
type ListData struct {
    Items any `json:"items"` // 数据列表
    Total int `json:"total"` // 总数
}
```

列表类接口返回时，`Response.Data` 统一为 `ListData` 结构，包含 `items`（数据数组）和 `total`（总条数），方便前端分页处理。

### 4.7 状态机

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

### 5.1 统一响应格式

所有接口统一使用 `Response` 信封返回，调用方只需解析一种结构：

**成功响应（单资源）**

```json
{
  "code": 0,
  "message": "success",
  "data": { ... }
}
```

**成功响应（列表）**

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "items": [ ... ],
    "total": 3
  }
}
```

**错误响应**

```json
{
  "code": 400,
  "message": "vendor_id, event and biz_id are required"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `code` | `int` | 业务码，`0` 表示成功，非零等于 HTTP 状态码 |
| `message` | `string` | 描述信息，成功时为 `"success"`，失败时为错误原因 |
| `data` | `object` | 业务数据，错误时省略 |

### 5.2 端点列表

| 方法 | 路径 | 说明 | 成功响应码 |
|------|------|------|--------|
| `POST` | `/api/notifications` | 创建通知任务（幂等） | `202` / `200` |
| `GET` | `/api/notifications/{id}` | 查询单个任务 | `200` |
| `GET` | `/api/notifications/failed` | 列出所有失败任务 | `200` |
| `POST` | `/api/notifications/{id}/replay` | 重放失败任务 | `200` |
| `POST` | `/api/vendors` | 创建供应商配置 | `201` |
| `GET` | `/api/vendors` | 列出所有供应商 | `200` |
| `GET` | `/api/vendors/{id}` | 查询单个供应商 | `200` |
| `PUT` | `/api/vendors/{id}` | 更新供应商配置 | `200` |
| `DELETE` | `/api/vendors/{id}` | 停用供应商（软删除） | `200` |
| `GET` | `/health` | 健康检查 | `200` |

### 5.3 通知接口

#### 5.3.1 创建通知 `POST /api/notifications`

调用方只需指定 `vendor_id`、`event`、`biz_id` 和可选的 `payload`，系统自动通过 Vendor 适配器构建完整的 HTTP 请求。接口支持幂等：相同 `(vendor_id, event, biz_id)` 组合的重复请求不会创建新 Job，而是返回已有记录。

**请求参数**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `vendor_id` | `string` | 是 | 供应商 ID，必须对应已存在的活跃供应商 |
| `event` | `string` | 是 | 业务事件名称（如 `"user_registered"`） |
| `biz_id` | `string` | 是 | 业务去重标识，与 `vendor_id` + `event` 组成唯一键 |
| `payload` | `object` | 否 | 事件负载数据，传入模板渲染 |

**请求示例**

```json
POST /api/notifications
Content-Type: application/json

{
  "vendor_id": "crm_vendor",
  "event": "user_registered",
  "biz_id": "user_123",
  "payload": {
    "user_id": 123,
    "name": "Alice"
  }
}
```

**响应参数（data 字段）**

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | `int` | Job 主键 |
| `vendor_id` | `string` | 供应商 ID |
| `event` | `string` | 事件名称 |
| `biz_id` | `string` | 业务去重标识 |
| `url` | `string` | 投递目标 URL（由 Vendor 解析） |
| `method` | `string` | HTTP 方法（由 Vendor 解析） |
| `headers` | `object` | 请求头（由 Vendor 解析） |
| `body` | `string` | 请求体（由模板渲染） |
| `status` | `string` | 任务状态：`pending` / `processing` / `completed` / `failed` |
| `retry_count` | `int` | 已重试次数 |
| `max_retries` | `int` | 最大重试次数 |
| `next_retry_at` | `string` | 下次重试时间（RFC3339） |
| `last_error` | `string` | 最近一次失败原因 |
| `created_at` | `string` | 创建时间（RFC3339） |
| `updated_at` | `string` | 更新时间（RFC3339） |

**响应示例（首次创建 — 202）**

```json
HTTP/1.1 202 Accepted

{
  "code": 0,
  "message": "success",
  "data": {
    "id": 1,
    "vendor_id": "crm_vendor",
    "event": "user_registered",
    "biz_id": "user_123",
    "url": "https://crm.example.com/api",
    "method": "POST",
    "headers": {"Content-Type": "application/json"},
    "body": "{\"event\": \"user_registered\", \"data\": {\"user_id\":123}}",
    "status": "pending",
    "retry_count": 0,
    "max_retries": 3,
    "next_retry_at": "2026-03-18T12:00:00Z",
    "created_at": "2026-03-18T12:00:00Z",
    "updated_at": "2026-03-18T12:00:00Z"
  }
}
```

**响应示例（重复请求 — 200）**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success",
  "data": {
    "id": 1,
    "vendor_id": "crm_vendor",
    "event": "user_registered",
    "biz_id": "user_123",
    "status": "pending",
    ...
  }
}
```

**错误响应示例**

```json
HTTP/1.1 400 Bad Request

{
  "code": 400,
  "message": "vendor_id, event and biz_id are required"
}
```

#### 5.3.2 查询通知 `GET /api/notifications/{id}`

**路径参数**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `id` | `int` | 是 | Job ID（正整数） |

**响应参数** — 同 5.3.1 的 `data` 字段。

**响应示例**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success",
  "data": {
    "id": 1,
    "vendor_id": "crm_vendor",
    "event": "user_registered",
    "biz_id": "user_123",
    "url": "https://crm.example.com/api",
    "method": "POST",
    "status": "completed",
    "retry_count": 0,
    "max_retries": 3,
    "created_at": "2026-03-18T12:00:00Z",
    "updated_at": "2026-03-18T12:00:05Z"
  }
}
```

**错误响应示例**

```json
HTTP/1.1 404 Not Found

{
  "code": 404,
  "message": "job not found"
}
```

#### 5.3.3 列出失败任务 `GET /api/notifications/failed`

无请求参数。

**响应参数（data 字段）**

| 字段 | 类型 | 说明 |
|------|------|------|
| `items` | `Job[]` | 失败任务列表（Job 结构同 5.3.1） |
| `total` | `int` | 总条数 |

**响应示例**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success",
  "data": {
    "items": [
      {
        "id": 3,
        "vendor_id": "ad_system",
        "event": "click",
        "biz_id": "click_789",
        "status": "failed",
        "retry_count": 3,
        "max_retries": 3,
        "last_error": "HTTP 503 Service Unavailable",
        ...
      }
    ],
    "total": 1
  }
}
```

#### 5.3.4 重放任务 `POST /api/notifications/{id}/replay`

将一个 `failed` 状态的任务重置为 `pending`，清零重试计数，重新进入投递队列。

**路径参数**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `id` | `int` | 是 | Job ID（必须处于 `failed` 状态） |

无请求体。

**响应参数** — 同 5.3.1 的 `data` 字段（重置后的 Job）。

**响应示例**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success",
  "data": {
    "id": 3,
    "vendor_id": "ad_system",
    "event": "click",
    "biz_id": "click_789",
    "status": "pending",
    "retry_count": 0,
    "max_retries": 3,
    "last_error": "",
    ...
  }
}
```

**错误响应示例**

```json
HTTP/1.1 400 Bad Request

{
  "code": 400,
  "message": "job 99 not found or not in failed status"
}
```

### 5.4 Vendor 管理接口

#### 5.4.1 创建供应商 `POST /api/vendors`

**请求参数**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `id` | `string` | 是 | 供应商唯一标识（如 `"crm_vendor"`） |
| `name` | `string` | 是 | 供应商显示名称 |
| `base_url` | `string` | 是 | 投递目标 URL，必须为合法 `http`/`https` URL |
| `method` | `string` | 否 | HTTP 方法，默认 `"POST"` |
| `auth_type` | `string` | 否 | 认证类型（`"bearer"` / `"api_key"` / `"basic"` / `""`） |
| `auth_config` | `object` | 否 | 认证配置（如 `{"token": "xxx"}`） |
| `headers` | `object` | 否 | 默认请求头 |
| `body_tpl` | `string` | 否 | 请求体模板（Go text/template 语法） |
| `max_retries` | `int` | 否 | 最大重试次数，默认 `3` |

**请求示例**

```json
POST /api/vendors
Content-Type: application/json

{
  "id": "crm_vendor",
  "name": "CRM System",
  "base_url": "https://crm.example.com/api",
  "method": "POST",
  "auth_type": "bearer",
  "auth_config": {"token": "secret-token"},
  "headers": {"Content-Type": "application/json"},
  "body_tpl": "{\"event\": {{json .Event}}, \"data\": {{json .Payload}}}",
  "max_retries": 5
}
```

**响应参数（data 字段）**

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | `string` | 供应商唯一标识 |
| `name` | `string` | 显示名称 |
| `base_url` | `string` | 投递目标 URL |
| `method` | `string` | HTTP 方法 |
| `auth_type` | `string` | 认证类型 |
| `auth_config` | `object` | 认证配置 |
| `headers` | `object` | 默认请求头 |
| `body_tpl` | `string` | 请求体模板 |
| `max_retries` | `int` | 最大重试次数 |
| `is_active` | `bool` | 是否启用 |
| `created_at` | `string` | 创建时间（RFC3339） |
| `updated_at` | `string` | 更新时间（RFC3339） |

**响应示例**

```json
HTTP/1.1 201 Created

{
  "code": 0,
  "message": "success",
  "data": {
    "id": "crm_vendor",
    "name": "CRM System",
    "base_url": "https://crm.example.com/api",
    "method": "POST",
    "auth_type": "bearer",
    "auth_config": {"token": "secret-token"},
    "headers": {"Content-Type": "application/json"},
    "body_tpl": "{\"event\": {{json .Event}}, \"data\": {{json .Payload}}}",
    "max_retries": 5,
    "is_active": true,
    "created_at": "2026-03-18T12:00:00Z",
    "updated_at": "2026-03-18T12:00:00Z"
  }
}
```

#### 5.4.2 查询供应商 `GET /api/vendors/{id}`

**路径参数**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `id` | `string` | 是 | 供应商 ID |

**响应参数** — 同 5.4.1 的 `data` 字段。

**响应示例**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success",
  "data": {
    "id": "crm_vendor",
    "name": "CRM System",
    "base_url": "https://crm.example.com/api",
    "method": "POST",
    "max_retries": 5,
    "is_active": true,
    ...
  }
}
```

**错误响应示例**

```json
HTTP/1.1 404 Not Found

{
  "code": 404,
  "message": "vendor not found"
}
```

#### 5.4.3 列出供应商 `GET /api/vendors`

无请求参数。

**响应参数（data 字段）**

| 字段 | 类型 | 说明 |
|------|------|------|
| `items` | `VendorConfig[]` | 供应商列表（结构同 5.4.1） |
| `total` | `int` | 总条数 |

**响应示例**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success",
  "data": {
    "items": [
      {
        "id": "crm_vendor",
        "name": "CRM System",
        "base_url": "https://crm.example.com/api",
        "is_active": true,
        ...
      },
      {
        "id": "ad_system",
        "name": "广告系统",
        "base_url": "https://example.com/ad/callback",
        "is_active": true,
        ...
      }
    ],
    "total": 2
  }
}
```

#### 5.4.4 更新供应商 `PUT /api/vendors/{id}`

**路径参数**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `id` | `string` | 是 | 供应商 ID |

**请求参数**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | `string` | 是 | 供应商显示名称 |
| `base_url` | `string` | 是 | 投递目标 URL |
| `method` | `string` | 否 | HTTP 方法，默认 `"POST"` |
| `auth_type` | `string` | 否 | 认证类型 |
| `auth_config` | `object` | 否 | 认证配置 |
| `headers` | `object` | 否 | 默认请求头 |
| `body_tpl` | `string` | 否 | 请求体模板 |
| `max_retries` | `int` | 否 | 最大重试次数，默认 `3` |

**请求示例**

```json
PUT /api/vendors/crm_vendor
Content-Type: application/json

{
  "name": "CRM System v2",
  "base_url": "https://crm2.example.com/api",
  "method": "PUT"
}
```

**响应参数** — 同 5.4.1 的 `data` 字段（更新后的完整供应商配置）。

**响应示例**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success",
  "data": {
    "id": "crm_vendor",
    "name": "CRM System v2",
    "base_url": "https://crm2.example.com/api",
    "method": "PUT",
    "is_active": true,
    ...
  }
}
```

#### 5.4.5 停用供应商 `DELETE /api/vendors/{id}`

执行软删除，将 `is_active` 设为 `false`，保留历史数据。

**路径参数**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `id` | `string` | 是 | 供应商 ID（必须处于启用状态） |

无请求体。

**响应示例**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success"
}
```

**错误响应示例**

```json
HTTP/1.1 400 Bad Request

{
  "code": 400,
  "message": "vendor \"unknown\" not found or already inactive"
}
```

### 5.5 健康检查 `GET /health`

无请求参数。

**响应示例**

```json
HTTP/1.1 200 OK

{
  "code": 0,
  "message": "success",
  "data": {
    "status": "ok"
  }
}
```

### 5.6 输入校验规则

**通知创建接口**

| 校验项 | 规则 |
|--------|------|
| 请求体大小 | ≤ 1MB (`MaxBytesReader`) |
| JSON 格式 | 禁止未知字段 (`DisallowUnknownFields`) |
| 单对象 | 请求体只能包含一个 JSON 对象 |
| vendor_id | 必填，必须对应已存在的活跃供应商 |
| event | 必填 |
| biz_id | 必填，业务去重标识（与 vendor_id + event 组成唯一键） |

**Vendor 管理接口**

| 校验项 | 规则 |
|--------|------|
| id | 创建时必填，不可更新 |
| name | 必填 |
| base_url | 必填，必须为合法的 `http`/`https` URL |
| method | 可选，默认 `POST` |
| max_retries | 可选，默认 3 |

**通用错误码**

| HTTP 状态码 | `code` | 含义 |
|-------------|--------|------|
| `400` | `400` | 请求参数校验失败 |
| `404` | `404` | 资源不存在 |
| `405` | `405` | HTTP 方法不允许 |
| `500` | `500` | 服务器内部错误 |

---

## 6. 数据流

### 6.1 任务投递流程

```
业务系统              HTTP API             Vendor Registry        Store (SQLite)        Worker
   │                    │                       │                      │                  │
   │  POST /api/notifications                   │                      │                  │
   │  {vendor_id, event, biz_id, payload}       │                      │                  │
   │───────────────────>│                       │                      │                  │
   │                    │  校验 vendor_id/event/biz_id                 │                  │
   │                    │                       │                      │                  │
   │                    │  Resolve(vendor_id)    │                      │                  │
   │                    │──────────────────────>│                      │                  │
   │                    │                       │  GetVendor(id)       │                  │
   │                    │                       │─────────────────────>│                  │
   │                    │  返回 Adapter          │                      │                  │
   │                    │<──────────────────────│                      │                  │
   │                    │                       │                      │                  │
   │                    │  BuildRequest(event, payload)                │                  │
   │                    │  → 解析 URL/Method/Headers/Body              │                  │
   │                    │                       │                      │                  │
   │                    │  CreateJob(params)     │                      │                  │
   │                    │─────────────────────────────────────────────>│                  │
   │                    │                       │                      │  INSERT INTO jobs│
   │                    │                       │                      │  status='pending'│
   │                    │  返回 Job              │                      │                  │
   │                    │<─────────────────────────────────────────────│                  │
   │  202 Accepted      │                       │                      │                  │
   │<───────────────────│                       │                      │                  │
   │                    │                       │                      │                  │
   │                    │                       │                      │  每 2 秒轮询     │
   │                    │                       │                      │<─────────────────│
   │                    │                       │                      │  FetchPendingJobs│
   │                    │                       │                      │  事务内乐观锁抢占│
   │                    │                       │                      │  → 'processing'  │
   │                    │                       │                      │─────────────────>│
   │                    │                       │                      │                  │
   │                    │                       │                      │        ┌─────────┤
   │                    │                       │                      │        │ doHTTP  │
   │                    │                       │                      │        │ Request │
   │                    │                       │                      │        └────┬────┘
   │                    │                       │                      │             │
   │                    │                       │                      │  2xx 成功:   │
   │                    │                       │                      │ MarkCompleted│
   │                    │                       │                      │<────────────│
   │                    │                       │                      │             │
   │                    │                       │                      │ 失败&未超限: │
   │                    │                       │                      │ MarkRetry   │
   │                    │                       │                      │<────────────│
   │                    │                       │                      │             │
   │                    │                       │                      │ 失败&超限:   │
   │                    │                       │                      │ MarkFailed  │
   │                    │                       │                      │<────────────│
```

### 6.2 崩溃恢复流程

当 Worker 在投递过程中崩溃，任务会停留在 `processing` 状态。恢复机制：

1. `FetchPendingJobs` 在查询时额外匹配 `status = 'processing' AND updated_at <= staleBefore`
2. `staleBefore = now - processingTimeout`（默认 1 分钟）
3. 超时的 `processing` 任务被重新抢占并投递

---

## 7. 数据库设计

### 7.1 表结构

**jobs 表**

```sql
CREATE TABLE IF NOT EXISTS jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    vendor_id     TEXT    NOT NULL DEFAULT '',          -- 关联的供应商 ID
    event         TEXT    NOT NULL DEFAULT '',          -- 业务事件名称
    biz_id        TEXT    NOT NULL DEFAULT '',          -- 业务去重标识
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

**vendors 表**

```sql
CREATE TABLE IF NOT EXISTS vendors (
    id          TEXT    PRIMARY KEY,                    -- 供应商唯一标识
    name        TEXT    NOT NULL,                       -- 显示名称
    base_url    TEXT    NOT NULL,                       -- 投递目标 URL
    method      TEXT    NOT NULL DEFAULT 'POST',        -- HTTP 方法
    auth_type   TEXT    NOT NULL DEFAULT '',             -- 认证类型
    auth_config TEXT    NOT NULL DEFAULT '{}',           -- 认证配置（JSON）
    headers     TEXT    NOT NULL DEFAULT '{}',           -- 默认请求头（JSON）
    body_tpl    TEXT    NOT NULL DEFAULT '',             -- 请求体模板
    max_retries INTEGER NOT NULL DEFAULT 3,             -- 最大重试次数
    is_active   INTEGER NOT NULL DEFAULT 1,             -- 1=启用 0=停用（软删除）
    created_at  INTEGER NOT NULL DEFAULT 0,             -- Unix 时间戳
    updated_at  INTEGER NOT NULL DEFAULT 0              -- Unix 时间戳
);
```

### 7.2 索引

| 索引 | 列 | 用途 |
|------|-----|------|
| `idx_jobs_status_next_retry` | `(status, next_retry_at)` | Worker 轮询查询优化 |
| `idx_jobs_status_updated_at` | `(status, updated_at)` | 崩溃恢复查询 + 失败列表排序 |
| `idx_jobs_biz_key` (UNIQUE) | `(vendor_id, event, biz_id) WHERE biz_id != ''` | 幂等去重（部分索引，空 biz_id 不参与） |

### 7.3 向后兼容迁移

对于已有的 `jobs` 表，`migrate()` 通过 `ALTER TABLE` 追加 `vendor_id`、`event` 和 `biz_id` 列（`DEFAULT ''`），忽略"列已存在"的错误，确保存量数据不受影响。同时创建 `idx_jobs_biz_key` 唯一部分索引用于幂等去重。

### 7.4 设计决策

| 决策 | 原因 |
|------|------|
| 时间用 Unix 整数 | 避免 SQLite DATETIME 文本格式与 Go 驱动之间的解析兼容问题 |
| Headers / AuthConfig 用 JSON TEXT | 灵活支持任意 KV 对，无需额外表 |
| WAL 模式 | 提升读写并发性能，API 写入与 Worker 读取互不阻塞 |
| `_busy_timeout=5000` | 数据库锁等待 5 秒，避免 `SQLITE_BUSY` 错误 |
| vendors.id 用 TEXT 主键 | 语义化标识（如 `"crm"`, `"ad_system"`），比自增 ID 更直观 |
| 软删除（is_active） | 停用供应商后保留历史配置，已创建的 Job 仍可追溯 |
| biz_id 幂等去重 | 通过 `(vendor_id, event, biz_id)` 唯一索引在数据库层面保证幂等，INSERT 冲突时返回已有记录 |
| 部分索引（`WHERE biz_id != ''`） | 空 biz_id 的存量数据不参与唯一约束，向后兼容 |
| 默认供应商预置（SeedDefaultVendors） | 首次启动时 INSERT OR IGNORE 内置供应商，手动修改后不会被覆盖 |

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
  ├─ 1. store.New(dbPath)              初始化 SQLite，执行 migration（含 vendors 表 + biz_id 列）
  │
  ├─ 1.5 db.SeedDefaultVendors()      预置内置供应商（INSERT OR IGNORE，不覆盖已有）
  │
  ├─ 2. adapter.NewRegistry(db)        创建 Vendor 注册表（从 DB 加载配置）
  │
  ├─ 3. worker.New(db)                 创建 Dispatcher
  │     └─ go dispatcher.Start(ctx)    后台 goroutine 轮询
  │
  ├─ 4. handler.New(db, registry)      创建 Handler（注入 Store + Registry）
  │     └─ RegisterRoutes(mux)         注册路由（含 Vendor CRUD）
  │
  ├─ 5. go srv.ListenAndServe()        后台 goroutine 监听 HTTP
  │
  └─ 6. signal.Notify(quit, ...)       阻塞等待退出信号
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
| **handler** | 统一使用 `Response` 信封返回（`respondSuccess` / `respondError` / `respondList`），`code=0` 表示成功，非零为 HTTP 状态码；Vendor 不存在返回 `400`；重复提交返回 `200` |
| **adapter** | 适配器构建失败时返回 error，由 handler 转为 `500` 响应 |
| **store** | 用 `fmt.Errorf("%w")` 包装错误向上传播，JSON 解析容错返回空 map |
| **worker** | 日志记录 + 状态流转（MarkRetry / MarkFailed），不 panic |
| **main** | `log.Fatalf` 处理不可恢复的初始化错误 |

---

## 12. 测试覆盖

| 文件 | 测试内容 |
|------|----------|
| `handler/notification_test.go` | 路由严格匹配（404）、vendor_id/event/biz_id 必填校验、Vendor 解析并创建 Job（含 biz_id）、幂等去重（202→200）、拒绝未知 Vendor、拒绝未知 JSON 字段、Vendor CRUD 全流程 |
| `store/sqlite_test.go` | 超时 processing 任务回收、无效 Headers JSON 容错、CreateJob 存储 vendor_id/event/biz_id、CreateJob 幂等去重、SeedDefaultVendors 预置与重复调用安全、Vendor CRUD（创建/查询/列表/更新/软删除） |
| `adapter/adapter_test.go` | ConfigAdapter 模板渲染、认证注入（bearer/api_key/basic）、Registry 解析优先级（代码 Adapter > 配置）、异常处理 |

测试使用临时数据库文件，每个测试用例独立初始化，确保隔离性。Handler 测试通过 `seedVendor` 辅助函数预置供应商配置，模拟完整的"配置 Vendor → 创建通知"流程。

---

## 13. 未来演进

```
当前                                演进方向
─────────────────────────────────────────────────────
SQLite 单文件           ──→  MySQL/PostgreSQL (水平扩展)
DB 轮询队列             ──→  Redis / RabbitMQ (更高吞吐)
单进程 Worker           ──→  多实例部署 (乐观锁已支持)
无中间件                ──→  日志 / 认证 / CORS / 限流
无监控                  ──→  Prometheus 指标 + Grafana
CLI 管理                ──→  Web 管理界面
基础 body_tpl 模板      ──→  更丰富的模板函数 / 条件逻辑
Vendor 认证字段存储      ──→  运行时认证注入（OAuth2 / HMAC 签名）
```
