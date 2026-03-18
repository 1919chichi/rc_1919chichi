# API 通知系统 / API Notification Service

> 企业内部 HTTP 通知中间件——接收业务系统提交的外部 HTTP 通知请求，并可靠地投递到目标地址。

## 问题理解

业务系统触发关键事件后，需要通知各类外部供应商（广告系统、CRM、库存系统等）。不同供应商的 API 格式各异，业务系统不关心返回值，只要求通知**稳定送达**。

核心挑战：**外部系统不可控**——网络抖动、供应商宕机都可能导致投递失败，需要异步 + 重试机制保障可靠性。

## 整体架构

```
业务系统
  │
  ▼ POST /api/notifications
┌─────────────────────┐
│   HTTP API 层        │  接收通知请求，写入 Job 队列
└─────────┬───────────┘
          │ 写入（SQLite）
          ▼
┌─────────────────────┐
│   Job Queue (SQLite) │  持久化存储，保证进程重启不丢数据
└─────────┬───────────┘
          │ 轮询消费
          ▼
┌─────────────────────┐
│   Worker / Dispatcher│  异步投递，指数退避重试
└─────────┬───────────┘
          │ 失败超过上限
          ▼
┌─────────────────────┐
│   Failed Jobs 表     │  死信记录，支持手动重放
└─────────────────────┘
```

## 核心设计

### 投递语义：至少一次（At-least-once）

Job 投递成功后才从队列删除，保证不丢失。接入方需具备幂等性处理能力。

### 重试策略：指数退避

| 重试次数 | 等待时间 |
|---------|---------|
| 第 1 次 | 10s |
| 第 2 次 | 30s |
| 第 3 次 | 90s |
| 超过上限 | 写入 failed_jobs，停止重试 |

### 请求格式（完全透传）

```json
POST /api/notifications
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

## 关键工程决策与取舍

### 选择解决

- 异步解耦：业务系统提交后立即返回，投递在后台进行
- 可靠投递：SQLite 持久化，进程重启不丢 Job
- 失败重试：指数退避，避免瞬间打爆外部系统
- 死信记录：超过重试上限后保留现场，支持人工介入

### 明确不解决

- **消息去重**：幂等性由外部接收方保证，本系统不做
- **认证 Token 自动刷新**：调用方负责传入有效凭证
- **限流 / 频控**：MVP 阶段不做，可在演进阶段加
- **通知内容的业务校验**：本系统只负责投递，不关心业务语义

### HTTP 框架：Go 标准库而非 Gin

最初考虑使用 Gin 框架，但它引入了大量间接依赖（protobuf、yaml、sonic 等），与「控制复杂度」的原则相悖。
Go 标准库 `net/http` 完全满足本系统的路由需求，且零外部依赖。

### SQLite 驱动：mattn/go-sqlite3

唯一的外部依赖。选择 CGo 版本而非纯 Go 版本（modernc.org/sqlite），因为后者包体积巨大，下载耗时过长。

### 存储选型：SQLite 而非 MySQL/PostgreSQL

MVP 阶段无额外中间件依赖，单文件存储，`git clone` 后直接运行。
流量增长后可无缝切换至 MySQL，改一行配置即可。

## Project Structure

```
.
├── main.go                    # 程序入口
├── go.mod
├── internal/
│   ├── handler/               # HTTP 接口层
│   │   └── notification.go
│   ├── worker/                # 异步投递 Worker
│   │   └── dispatcher.go
│   ├── store/                 # 数据持久化层
│   │   └── sqlite.go
│   └── model/                 # 数据模型
│       └── job.go
├── README.md
└── ai-usage.md                # AI 使用说明
```

## 环境要求

- Go 1.20+
- GCC（macOS 自带 Xcode Command Line Tools，Linux 需安装 `build-essential`）

## 快速启动

```bash
# 克隆项目
git clone https://github.com/YOUR_USERNAME/rc_1919chichi.git
cd rc_1919chichi

# 安装依赖
go mod tidy

# 编译 & 运行（自动初始化 SQLite 数据库）
go build -o bin/notifier ./main.go
./bin/notifier

# 或直接 go run
go run main.go

# 服务启动在 :8080
```

### API 示例

```bash
# 提交通知请求（立即返回 202）
curl -X POST http://localhost:8080/api/notifications \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://httpbin.org/post",
    "method": "POST",
    "headers": {"X-Custom": "value"},
    "body": "{\"event\":\"test\"}"
  }'

# 查询 Job 状态
curl http://localhost:8080/api/notifications/1

# 查看所有失败的 Job
curl http://localhost:8080/api/notifications/failed

# 手动重放某个失败 Job
curl -X POST http://localhost:8080/api/notifications/1/replay

# 健康检查
curl http://localhost:8080/health
```

## 未来演进方向

1. **存储层**：SQLite → MySQL/PostgreSQL，Worker 可水平扩展
2. **队列**：DB Queue → Redis/RabbitMQ，支持更高并发
3. **可观测性**：接入 Prometheus 指标，投递成功率、延迟监控
4. **限流**：per-vendor 频率控制，避免打爆外部 API
5. **管理界面**：查看 failed jobs，一键重放

---

详见 [AI 使用说明](./ai-usage.md)
