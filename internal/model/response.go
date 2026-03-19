// package model —— 定义 API 响应和通用数据结构
package model

// Response 是所有 API  endpoints 的统一响应封装
// Code: 业务状态码，0 通常表示成功
// Message: 人类可读的消息
// Data: 实际返回的数据，可为任意类型（any）
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ListData 用于包装分页或列表类数据，作为 Response.Data 的内容
// Items: 当前页的数据条目
// Total: 总数量（用于分页计算）
type ListData struct {
	Items any `json:"items"`
	Total int `json:"total"`
}
