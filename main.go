// package main —— 每个 Go 程序都必须有一个 main 包，这是程序的入口包
package main

// import (...) —— 导入需要用到的包（类似 Python 的 import 或 Java 的 import）
// Go 的标准库包用短名称（如 "log"），第三方/自己的包用完整路径
import (
	"context" // context 包：用于在函数之间传递"取消信号"和超时控制
	"log"     // log 包：用于打印日志
	"net/http" // net/http 包：Go 内置的 HTTP 服务器和客户端库
	"os"       // os 包：操作系统相关功能（如读取环境变量）
	"os/signal" // os/signal 包：监听操作系统信号（如 Ctrl+C）
	"syscall"   // syscall 包：提供操作系统底层信号的常量（如 SIGINT、SIGTERM）
	"time"      // time 包：时间相关的工具

	// 以下是本项目内部的包（internal 表示仅项目内部可用）
	"github.com/1919chichi/rc_1919chichi/internal/handler" // HTTP 接口处理层
	"github.com/1919chichi/rc_1919chichi/internal/store"   // 数据库存储层
	"github.com/1919chichi/rc_1919chichi/internal/worker"  // 后台任务调度器
)

// func main() —— Go 程序的入口函数，程序从这里开始执行
func main() {
	// envOr() 是下面定义的辅助函数：读取环境变量，如果没设置就用默认值
	// 这里读取数据库文件路径，默认为 "notifications.db"
	dbPath := envOr("DB_PATH", "notifications.db")
	// 读取监听地址，默认为 ":8080"（即监听所有网卡的 8080 端口）
	addr := envOr("ADDR", ":8080")

	// store.New() 初始化 SQLite 数据库连接
	// Go 的函数可以返回多个值，这里返回 (db, err)
	// := 是 Go 的"短变量声明"，自动推断变量类型
	db, err := store.New(dbPath)
	// if err != nil —— Go 中没有 try/catch 异常机制
	// 而是通过返回 error 值来处理错误，nil 表示"没有错误"
	if err != nil {
		// log.Fatalf 打印错误日志后直接终止程序（Fatal = 致命的）
		// %v 是 Go 的格式化占位符，类似 printf 的 %s，但可以打印任意类型
		log.Fatalf("failed to init store: %v", err)
	}
	// defer 关键字：延迟执行，在当前函数（main）返回时才会执行
	// 这里确保程序退出前关闭数据库连接，避免资源泄漏
	defer db.Close()
	log.Printf("database initialized at %s", dbPath)

	// context.WithCancel 创建一个可取消的上下文（context）
	// ctx 用于通知所有 goroutine "该停止了"
	// cancel 是一个函数，调用它就会发出取消信号
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 同样用 defer 确保 main 退出时取消

	// worker.New(db) 创建一个后台任务调度器，负责轮询数据库并投递 HTTP 请求
	dispatcher := worker.New(db)
	// go 关键字：启动一个 goroutine（Go 的轻量级线程/协程）
	// 这样 dispatcher.Start 在后台运行，不会阻塞主流程
	go dispatcher.Start(ctx)

	// http.NewServeMux() 创建一个 HTTP 路由器（类似其他框架的 Router）
	mux := http.NewServeMux()
	// handler.New(db) 创建 HTTP 接口处理器
	h := handler.New(db)
	// 将路由规则注册到路由器上
	h.RegisterRoutes(mux)

	// &http.Server{...} —— & 取地址符，创建一个 http.Server 的指针
	// 结构体字面量初始化：{ 字段名: 值, ... }
	srv := &http.Server{Addr: addr, Handler: mux}

	// 在另一个 goroutine 中启动 HTTP 服务器
	// func() {...}() 是匿名函数（闭包），可以访问外部变量
	go func() {
		log.Printf("server listening on %s", addr)
		// ListenAndServe 开始监听端口并处理请求，这是一个阻塞调用
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// make(chan os.Signal, 1) —— 创建一个信号通道（channel）
	// channel 是 Go 的核心并发原语，用于 goroutine 之间安全地传递数据
	// 缓冲区大小为 1，可以暂存一个信号
	quit := make(chan os.Signal, 1)
	// 注册监听 SIGINT（Ctrl+C）和 SIGTERM（kill 命令）信号
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	// <-quit —— 从 channel 接收数据，这里会一直阻塞，直到收到退出信号
	// 这就是程序保持运行的原因
	<-quit
	log.Println("shutting down...")

	// 调用 cancel() 通知所有使用 ctx 的 goroutine 停止工作
	cancel()

	// 创建一个 5 秒超时的 context，用于限制优雅关闭的最长等待时间
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	// srv.Shutdown 优雅关闭 HTTP 服务器：等待已有请求处理完毕，不再接受新请求
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}

	log.Println("bye")
}

// envOr 是一个辅助函数
// func 函数名(参数名 参数类型, ...) 返回值类型 { ... }
// Go 的类型写在变量名后面（和 C/Java 相反）
func envOr(key, fallback string) string {
	// os.Getenv 读取环境变量的值
	// if v := ...; v != "" 是 Go 的简短 if 语法：先赋值再判断
	if v := os.Getenv(key); v != "" {
		return v
	}
	// 如果环境变量为空，返回默认值
	return fallback
}
