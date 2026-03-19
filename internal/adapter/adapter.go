// package adapter —— 适配器层，负责将业务事件转换为具体 HTTP 请求
package adapter

// ResolvedRequest 保存已完全解析的 HTTP 请求参数
// 由 VendorAdapter 的 BuildRequest 方法产出，可直接用于发起 HTTP 调用
type ResolvedRequest struct {
	URL        string            // 完整请求 URL
	Method     string            // HTTP 方法（如 POST、GET）
	Headers    map[string]string // 请求头（含认证等）
	Body       string            // 请求体
	MaxRetries int               // 该请求的最大重试次数
}

// VendorAdapter 将业务事件转换为针对某厂商 API 的具体 HTTP 请求
// 不同厂商可实现各自的适配器（如 ConfigAdapter 从配置构建，或自定义代码适配器）
type VendorAdapter interface {
	VendorID() string                                                      // 返回厂商唯一标识
	BuildRequest(event string, payload map[string]any) (*ResolvedRequest, error) // 根据事件和负载构建请求
}
