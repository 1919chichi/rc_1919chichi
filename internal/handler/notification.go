// package handler —— 这个包负责处理所有的 HTTP 请求（即 API 接口层）
package handler

import (
	"encoding/json" // JSON 编解码
	"errors"        // 错误处理工具
	"io"            // I/O 接口定义（如 io.EOF 表示读到末尾）
	"net/http"      // HTTP 服务器和客户端
	"net/url"       // URL 解析工具
	"strconv"       // 字符串与其他类型之间的转换（如字符串转数字）
	"strings"       // 字符串操作工具

	"github.com/1919chichi/rc_1919chichi/internal/model"
	"github.com/1919chichi/rc_1919chichi/internal/store"
)

// Handler 结构体封装了所有 HTTP 接口的处理逻辑
// 它持有一个 *store.Store（指向 Store 的指针），用来操作数据库
type Handler struct {
	store *store.Store // * 表示指针类型：不复制整个 Store，而是存一个引用/地址
}

// const 定义常量：限制请求体最大为 1MB
// 1 << 20 是位运算，等于 2 的 20 次方 = 1048576 字节 = 1MB
const maxCreateRequestBodyBytes = 1 << 20

// map[string]struct{} —— 这是 Go 中实现"集合（Set）"的惯用方法
// struct{} 是空结构体，不占内存，只关心 key 是否存在
// 这里定义了允许的 HTTP 方法白名单
var allowedHTTPMethods = map[string]struct{}{
	http.MethodGet:    {}, // http.MethodGet 是常量 "GET"
	http.MethodPost:   {}, // "POST"
	http.MethodPut:    {}, // "PUT"
	http.MethodPatch:  {}, // "PATCH"
	http.MethodDelete: {}, // "DELETE"
}

// New 是 Handler 的构造函数（Go 没有 class，习惯用 New 函数来创建结构体实例）
// 参数 s 是 *store.Store 类型（Store 的指针）
// 返回值 *Handler 也是指针类型
func New(s *store.Store) *Handler {
	// &Handler{...} —— & 取地址，返回一个指向 Handler 的指针
	return &Handler{store: s}
}

// RegisterRoutes 将 URL 路由注册到 HTTP 路由器上
// (h *Handler) 是"方法接收者"——表示这个函数属于 Handler 类型
// 类似其他语言中的 this 或 self
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// HandleFunc 注册一个路由：当请求匹配该路径时，调用对应的函数
	mux.HandleFunc("/api/notifications", h.handleNotifications)
	mux.HandleFunc("/api/notifications/", h.handleNotificationByID) // 注意末尾的 /，表示匹配以此为前缀的所有路径
	// 健康检查接口，返回 {"status": "ok"}
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// handleNotifications 处理 /api/notifications 路径的请求
// w http.ResponseWriter —— 用于写入 HTTP 响应
// r *http.Request —— 包含 HTTP 请求的所有信息
func (h *Handler) handleNotifications(w http.ResponseWriter, r *http.Request) {
	// switch 是多分支选择语句（类似 if-else if 链）
	// r.Method 是请求的 HTTP 方法（GET/POST/PUT 等）
	switch r.Method {
	case http.MethodPost:
		h.Create(w, r) // 只允许 POST 方法
	default:
		// 其他方法返回 405 Method Not Allowed
		methodNotAllowed(w, http.MethodPost)
	}
}

// handleNotificationByID 处理 /api/notifications/{id} 和 /api/notifications/failed 等路径
func (h *Handler) handleNotificationByID(w http.ResponseWriter, r *http.Request) {
	// strings.TrimPrefix 去掉路径前缀，提取出 ID 部分
	// 例如 /api/notifications/42 -> "42"
	// 例如 /api/notifications/failed -> "failed"
	path := strings.TrimPrefix(r.URL.Path, "/api/notifications/")
	if path == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
		return // return 提前退出函数
	}

	// 处理 /api/notifications/failed —— 列出所有失败的任务
	if path == "failed" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		h.ListFailed(w, r)
		return
	}

	// strings.Split 按 "/" 分割路径
	// 例如 "42" -> ["42"]，"42/replay" -> ["42", "replay"]
	parts := strings.Split(path, "/")
	// switch { case 条件: ... } 是 Go 的无表达式 switch，等价于 if-else if 链
	switch {
	case len(parts) == 1:
		// 路径只有一段，即 /api/notifications/{id} —— 查询单个任务
		id, err := parseJobID(parts[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		h.Get(w, r, id)
	case len(parts) == 2 && parts[1] == "replay":
		// 路径是 /api/notifications/{id}/replay —— 重放失败的任务
		id, err := parseJobID(parts[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		h.Replay(w, r, id)
	default:
		// 不匹配任何已知路由，返回 404
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
	}
}

// Create 处理 POST /api/notifications —— 创建一个新的通知任务
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	// http.MaxBytesReader 包装请求体，限制最大读取字节数（防止恶意大请求）
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateRequestBodyBytes)

	// var req model.CreateNotificationRequest —— 声明一个变量
	// var 变量名 类型 是 Go 的标准变量声明方式
	var req model.CreateNotificationRequest
	// json.NewDecoder 创建一个 JSON 解码器，从请求体中读取 JSON
	decoder := json.NewDecoder(r.Body)
	// DisallowUnknownFields 设定：如果 JSON 中有未知字段，报错
	decoder.DisallowUnknownFields()

	// decoder.Decode(&req) 将 JSON 解码到 req 变量中
	// &req 是取地址——把 req 的地址传进去，这样函数才能修改 req 的值
	if err := decoder.Decode(&req); err != nil {
		// errors.As 检查 err 是否是特定类型的错误
		// 类似 Java 的 instanceof 或 Python 的 isinstance
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	// 尝试再解码一次；如果没到 EOF，说明请求体包含多个 JSON 对象
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must contain a single JSON object"})
		return
	}

	// 清理和标准化输入
	req.URL = strings.TrimSpace(req.URL)                         // 去除 URL 两端空白
	req.Method = strings.ToUpper(strings.TrimSpace(req.Method))  // 方法转大写（如 "post" -> "POST"）

	// 验证必填字段
	if req.URL == "" || req.Method == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url and method are required"})
		return
	}

	// url.ParseRequestURI 解析并验证 URL 格式
	parsedURL, err := url.ParseRequestURI(req.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url must be a valid http/https URL"})
		return
	}

	// 检查 HTTP 方法是否在白名单中
	// _, ok := map[key] 是 Go 中检查 map 中是否存在某个 key 的惯用写法
	// ok 为 true 表示存在，_ 表示忽略值（因为值是空结构体，我们不需要）
	if _, ok := allowedHTTPMethods[req.Method]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "method must be one of GET, POST, PUT, PATCH, DELETE"})
		return
	}

	// range 遍历 map，检查是否有空的 header 名
	// for key := range map 只遍历 key（不需要 value）
	for key := range req.Headers {
		if strings.TrimSpace(key) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "header name cannot be empty"})
			return
		}
	}

	// 所有验证通过，将任务写入数据库
	job, err := h.store.CreateJob(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to enqueue notification"})
		return
	}

	// 返回 202 Accepted（已接受但尚未处理完）
	// map[string]any —— any 是 Go 的空接口类型，可以放任何类型的值
	writeJSON(w, http.StatusAccepted, map[string]any{
		"message": "notification enqueued",
		"job":     job,
	})
}

// Get 处理 GET /api/notifications/{id} —— 查询单个任务
// _ *http.Request 中的 _ 表示忽略该参数（用不到但必须写在签名里）
func (h *Handler) Get(w http.ResponseWriter, _ *http.Request, id int64) {
	job, err := h.store.GetJob(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// ListFailed 处理 GET /api/notifications/failed —— 列出所有失败的任务
func (h *Handler) ListFailed(w http.ResponseWriter, _ *http.Request) {
	jobs, err := h.store.ListFailedJobs()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list jobs"})
		return
	}
	// 确保返回空数组 [] 而不是 null
	if jobs == nil {
		jobs = []model.Job{} // []model.Job{} 创建一个空的 Job 切片
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "count": len(jobs)})
}

// Replay 处理 POST /api/notifications/{id}/replay —— 重放一个失败的任务
func (h *Handler) Replay(w http.ResponseWriter, _ *http.Request, id int64) {
	job, err := h.store.ResetJob(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "job replayed",
		"job":     job,
	})
}

// writeJSON 是一个工具函数，将数据序列化为 JSON 并写入 HTTP 响应
// data any —— any 类型意味着接受任何类型的参数
func writeJSON(w http.ResponseWriter, status int, data any) {
	// 设置响应头的 Content-Type 为 JSON
	w.Header().Set("Content-Type", "application/json")
	// 写入 HTTP 状态码（如 200、400、500 等）
	w.WriteHeader(status)
	// json.NewEncoder(w).Encode(data) 将 data 编码为 JSON 写入 w
	// _ = ... 中 _ 用于丢弃返回的 error（这里简化处理，不处理编码错误）
	_ = json.NewEncoder(w).Encode(data)
}

// methodNotAllowed 返回 405 响应，并在 Allow 头中告知客户端支持哪些方法
// ...string 是可变参数（variadic），类似 Python 的 *args
func methodNotAllowed(w http.ResponseWriter, allowedMethods ...string) {
	if len(allowedMethods) > 0 {
		// strings.Join 将字符串切片用分隔符连接
		w.Header().Set("Allow", strings.Join(allowedMethods, ", "))
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

// parseJobID 将字符串解析为 int64 类型的任务 ID
// 返回 (int64, error) —— Go 函数可以有多个返回值
func parseJobID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		// errors.New 创建一个新的 error 对象
		return 0, errors.New("empty job id")
	}
	// strconv.ParseInt 将字符串解析为整数
	// 参数：字符串、进制（10=十进制）、位数（64=int64）
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid job id")
	}
	return id, nil // nil 表示没有错误
}
