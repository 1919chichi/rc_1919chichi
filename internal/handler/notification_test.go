// package handler —— 测试文件和被测文件放在同一个包中
// Go 的测试文件命名规则：xxx_test.go，Go 的测试工具会自动识别
package handler

import (
	"bytes"          // 提供内存缓冲区操作，这里用于构造请求体
	"encoding/json"  // JSON 编解码
	"net/http"       // HTTP 相关常量和类型
	"net/http/httptest" // HTTP 测试工具包，可以模拟 HTTP 请求和响应
	"path/filepath"  // 文件路径操作
	"testing"        // Go 内置的测试框架

	"github.com/1919chichi/rc_1919chichi/internal/store"
)

// newTestMux 是测试辅助函数，创建一个用于测试的 HTTP 路由器
// t *testing.T 是 Go 测试函数的标准参数，用于报告测试失败
func newTestMux(t *testing.T) *http.ServeMux {
	// t.Helper() 标记这是一个辅助函数
	// 这样测试失败时，报告的行号会指向调用者而非此函数内部
	t.Helper()

	// t.TempDir() 创建一个临时目录，测试结束后自动清理
	dbPath := filepath.Join(t.TempDir(), "test.db")
	// 创建一个用于测试的 SQLite 存储
	s, err := store.New(dbPath)
	if err != nil {
		// t.Fatalf 报告致命错误并终止当前测试
		t.Fatalf("create store: %v", err)
	}
	// t.Cleanup 注册一个在测试结束时执行的清理函数
	// 类似 defer，但作用域是整个测试而不是当前函数
	t.Cleanup(func() {
		_ = s.Close()
	})

	// 创建 Handler 和路由器，注册路由
	h := New(s)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

// TestXxx 格式是 Go 测试函数的命名规则：必须以 Test 开头，后跟大写字母
// 这个测试验证：访问 /api/notifications/1/extra（多余路径段）应返回 404
func TestHandleNotificationByID_StrictPath(t *testing.T) {
	mux := newTestMux(t)

	// httptest.NewRequest 创建一个模拟的 HTTP 请求（不会真正发送到网络）
	req := httptest.NewRequest(http.MethodGet, "/api/notifications/1/extra", nil)
	// httptest.NewRecorder 创建一个模拟的响应记录器，捕获 HTTP 响应
	rec := httptest.NewRecorder()
	// ServeHTTP 模拟将请求发送到路由器处理
	mux.ServeHTTP(rec, req)

	// rec.Code 是捕获到的 HTTP 状态码
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// 测试：创建通知时，method 字段会被自动转为大写（如 "post" -> "POST"）
func TestCreate_NormalizesMethod(t *testing.T) {
	mux := newTestMux(t)

	// []byte(`...`) 将字符串转换为字节切片（[]byte）
	// 反引号 ` 包裹的是原始字符串（不会转义 \ 等特殊字符）
	body := []byte(`{"url":"https://example.com/hook","method":"post"}`)
	// bytes.NewReader 将字节切片包装为 io.Reader 接口，用作请求体
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// http.StatusAccepted = 202，表示请求已接受
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	// var resp struct{...} —— 声明一个匿名结构体变量，用于解析响应 JSON
	// 匿名结构体常用于只在一处使用的临时数据结构
	var resp struct {
		Job struct {
			Method string `json:"method"` // 嵌套结构体，取 job.method 字段
		} `json:"job"`
	}
	// json.Unmarshal 将 JSON 字节切片解析到结构体中
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// 验证返回的 method 是大写的 "POST"
	if resp.Job.Method != http.MethodPost {
		t.Fatalf("expected method %q, got %q", http.MethodPost, resp.Job.Method)
	}
}

// 测试：当 JSON 中包含未知字段时，应返回 400 错误
func TestCreate_RejectsUnknownFields(t *testing.T) {
	mux := newTestMux(t)

	// JSON 中多了一个 "unexpected" 字段，不在 CreateNotificationRequest 的定义中
	body := []byte(`{"url":"https://example.com/hook","method":"POST","unexpected":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// 期望返回 400 Bad Request
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
