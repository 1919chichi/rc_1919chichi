# API 通知系统 / API Notification Service

> 企业内部 HTTP 通知中间件——接收业务系统提交的外部 HTTP 通知请求，并可靠地投递到目标地址。

## 问题理解

业务系统触发关键事件后，需要通知各类外部供应商（广告系统、CRM、库存系统等）。不同供应商的 API 格式各异，业务系统不关心返回值，只要求通知**稳定送达**。

核心挑战：**外部系统不可控**——网络抖动、供应商宕机都可能导致投递失败，需要异步 + 重试机制保障可靠性。

## 整体架构

业务系统通过 HTTP API 提交通知请求 → SQLite 持久化为 Job → Worker 后台轮询投递 → 失败重试或写入死信。

> 系统边界、可靠性策略、取舍与演进等设计说明见 [架构设计文档 §0](./ARCHITECTURE.md#0-设计说明与评审要求对应)；分层架构图、数据流时序图、数据库设计等见 [架构设计文档](./ARCHITECTURE.md)

## 核心设计

- **投递语义**：至少一次（At-least-once），接入方需具备幂等性处理能力
- **接口幂等**：通过 `biz_id` 实现去重，相同 `(vendor_id, event, biz_id)` 不会重复创建 Job
- **重试策略**：指数退避（`delay = 10s × 3^retry_count`）；默认 `max_retries=3` 时会在 10s、30s 后进入死信，支持手动重放
- **崩溃恢复**：`processing` 超时任务自动回收并重新投递

> 详细的状态机流转、退避公式、抢占机制等见 [架构设计文档](./ARCHITECTURE.md)

## 关键工程决策与取舍

### 选择解决

- 异步解耦：业务系统提交后立即返回，投递在后台进行
- 可靠投递：SQLite 持久化，进程重启不丢 Job
- 失败重试：指数退避，避免瞬间打爆外部系统
- 死信记录：超过重试上限后保留现场，支持人工介入
- **认证能力**：内置 `bearer` / `api_key` / `basic` 凭证注入；复杂认证（如 OAuth2 刷新、签名）通过代码 Adapter 扩展，不要求调用方传入凭证

### 明确不解决

- **投递侧去重 / 接收端幂等**：提交侧已用 `biz_id` 防重复创建 Job；同一 Job 可能因重试被多次投递到接收方，接收方需自行做幂等处理
- **限流 / 频控**：MVP 阶段不做，可在演进阶段加
- **通知内容的业务校验**：本系统只负责投递，不关心业务语义

### HTTP 框架：Go 标准库而非 Gin

最初考虑使用 Gin 框架，但它引入了大量间接依赖（protobuf、yaml、sonic 等），与「控制复杂度」的原则相悖。
Go 标准库 `net/http` 完全满足本系统的路由需求，且零外部依赖。

### SQLite 驱动：mattn/go-sqlite3

唯一的外部依赖。选择 CGo 版本而非纯 Go 版本（modernc.org/sqlite），因为后者包体积巨大，下载耗时过长。

### 存储选型：SQLite 而非 MySQL/PostgreSQL

MVP 阶段无额外中间件依赖，单文件存储，`git clone` 后直接运行。
流量增长后可演进到 MySQL/PostgreSQL，但需要新增对应 `store` 实现并在 `main.go` 替换注入。

## Project Structure

五层分包：`handler`（HTTP API）→ `adapter`（供应商适配）→ `store`（持久化）← `worker`（后台调度），`model`（共享数据定义）。

> 完整目录树与包依赖关系见 [架构设计文档 §3](./ARCHITECTURE.md#3-包结构)

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

首次启动会自动预置 3 个供应商：`ad_system`、`crm_system`、`inventory_system`。

### API 示例

所有接口统一使用 `{code, message, data}` 信封格式返回，`code=0` 表示成功。

> 完整的请求参数和响应参数说明见 [架构设计文档 §5](./ARCHITECTURE.md#5-api-设计)

```bash
# 提交通知请求（首次 202，重复 200）
curl -X POST http://localhost:8080/api/notifications \
  -H "Content-Type: application/json" \
  -d '{
    "vendor_id": "crm_system",
    "event": "user_registered",
    "biz_id": "user_123",
    "payload": {"user_id": 123, "name": "Alice"}
  }'
# → {"code":0,"message":"success","data":{"id":1,"vendor_id":"crm_system",...}}

# 查询 Job 状态
curl http://localhost:8080/api/notifications/1
# → {"code":0,"message":"success","data":{"id":1,"status":"completed",...}}

# 查看所有失败的 Job
curl http://localhost:8080/api/notifications/failed
# → {"code":0,"message":"success","data":{"items":[...],"total":0}}

# 手动重放某个失败 Job
curl -X POST http://localhost:8080/api/notifications/1/replay
# → {"code":0,"message":"success","data":{"id":1,"status":"pending",...}}

# 健康检查
curl http://localhost:8080/health
# → {"code":0,"message":"success","data":{"status":"ok"}}
```

## 未来演进方向

存储升级（MySQL）、队列替换（Redis/MQ）、可观测性（Prometheus）、限流、管理界面等。

> 详细演进路线见 [架构设计文档 §13](./ARCHITECTURE.md#13-未来演进)

---

详见 [AI 使用说明](./ai-usage.md)
