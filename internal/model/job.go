// package model —— 定义数据模型（即数据的结构/形状）
// Go 中用 package 关键字声明当前文件属于哪个包
package model

// 导入 time 包，用于处理时间相关的类型和操作
import "time"

// type ... string —— 定义一个新类型 JobStatus，底层是 string
// 这类似于给 string 起了个别名，但 Go 会把它当成不同类型来检查
// 好处：限制变量只能取特定的值，避免手写字符串出错
type JobStatus string

// const (...) —— 定义一组常量
// Go 中常量用 const 声明，类似 JavaScript 的 const
const (
	StatusPending    JobStatus = "pending"    // 等待投递
	StatusProcessing JobStatus = "processing" // 正在投递中
	StatusCompleted  JobStatus = "completed"  // 投递成功
	StatusFailed     JobStatus = "failed"     // 投递失败（超过最大重试次数）
)

// DefaultMaxRetries：默认最大重试次数为 3
const DefaultMaxRetries = 3

// type Job struct {...} —— 定义一个结构体（struct）
// struct 是 Go 中用来组合数据的方式，类似其他语言中的 class 或 object
// 每个字段有三部分：字段名、类型、标签（backtick 中的内容）
type Job struct {
	// `json:"id"` 是结构体标签（struct tag），告诉 JSON 序列化器：
	// 当这个结构体转成 JSON 时，这个字段的 key 应该叫 "id"
	ID          int64             `json:"id"`                // 任务唯一标识，int64 = 64位整数
	URL         string            `json:"url"`               // 通知目标地址
	Method      string            `json:"method"`            // HTTP 方法（GET/POST 等）
	Headers     map[string]string `json:"headers,omitempty"` // HTTP 请求头，map[string]string = 字符串到字符串的映射（字典）；omitempty 表示为空时 JSON 中省略该字段
	Body        string            `json:"body,omitempty"`    // HTTP 请求体
	Status      JobStatus         `json:"status"`            // 当前状态
	RetryCount  int               `json:"retry_count"`       // 已重试次数
	MaxRetries  int               `json:"max_retries"`       // 最大重试次数
	NextRetryAt time.Time         `json:"next_retry_at"`     // 下次重试时间，time.Time 是 Go 的时间类型
	LastError   string            `json:"last_error,omitempty"` // 最后一次错误信息
	CreatedAt   time.Time         `json:"created_at"`        // 创建时间
	UpdatedAt   time.Time         `json:"updated_at"`        // 更新时间
}

// CreateNotificationRequest 是 POST /api/notifications 接口接收的请求体结构
type CreateNotificationRequest struct {
	URL     string            `json:"url" binding:"required"`     // binding:"required" 标记该字段为必填（但本项目用标准库，实际靠代码逻辑验证）
	Method  string            `json:"method" binding:"required"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// BackoffDuration 计算指数退避的等待时间
// func 函数名(参数名 参数类型) 返回值类型 —— Go 的函数签名格式
// retry 0 -> 10秒, retry 1 -> 30秒, retry 2 -> 90秒, ...（每次乘以3）
func BackoffDuration(retryCount int) time.Duration {
	// time.Duration 是 Go 中表示时间长度的类型
	base := 10 * time.Second // 基础等待 10 秒
	// for 循环：Go 中只有 for，没有 while
	// for i := 0; i < retryCount; i++ 是经典的三段式 for 循环
	for i := 0; i < retryCount; i++ {
		base *= 3 // 每次重试，等待时间乘以 3
	}
	return base
}
