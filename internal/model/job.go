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
	ID          int64             `json:"id"`
	VendorID    string            `json:"vendor_id"`
	Event       string            `json:"event"`
	BizID       string            `json:"biz_id"`
	URL         string            `json:"url"`
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	Status      JobStatus         `json:"status"`
	RetryCount  int               `json:"retry_count"`
	MaxRetries  int               `json:"max_retries"`
	NextRetryAt time.Time         `json:"next_retry_at"`
	LastError   string            `json:"last_error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// CreateNotificationRequest is the request body for POST /api/notifications.
// Callers only specify which vendor, what event, and the event payload.
type CreateNotificationRequest struct {
	VendorID string         `json:"vendor_id"`
	Event    string         `json:"event"`
	BizID    string         `json:"biz_id"`
	Payload  map[string]any `json:"payload,omitempty"`
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
