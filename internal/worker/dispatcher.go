// package worker —— 后台任务调度器，负责轮询数据库并执行 HTTP 通知投递
package worker

import (
	"context"    // 上下文（用于取消信号传递和超时控制）
	"fmt"        // 格式化输出
	"io"         // I/O 接口
	"log"        // 日志
	"net/http"   // HTTP 客户端
	"strings"    // 字符串操作
	"time"       // 时间

	"github.com/1919chichi/rc_1919chichi/internal/model"
	"github.com/1919chichi/rc_1919chichi/internal/store"
)

// Dispatcher 是后台任务调度器
// 它定期从数据库中拉取待投递的任务，发送 HTTP 请求，并处理成功/失败
type Dispatcher struct {
	store             *store.Store   // 数据库操作层
	client            *http.Client   // HTTP 客户端（用于发送通知请求）
	interval          time.Duration  // 轮询间隔（多久查一次数据库）
	batch             int            // 每次最多拉取多少个任务
	processingTimeout time.Duration  // 任务处理超时时间（超过这个时间视为卡死）
}

// New 创建一个新的 Dispatcher
func New(s *store.Store) *Dispatcher {
	// &Dispatcher{...} 创建结构体并返回指针
	return &Dispatcher{
		store: s,
		client: &http.Client{
			Timeout: 15 * time.Second, // 单个 HTTP 请求的超时时间为 15 秒
		},
		interval:          2 * time.Second, // 每 2 秒轮询一次
		batch:             10,              // 每次最多处理 10 个任务
		processingTimeout: time.Minute,     // processing 状态超过 1 分钟视为卡死
	}
}

// Start 启动轮询循环，持续运行直到 ctx 被取消
// ctx context.Context 是 Go 的上下文参数，用于优雅停机
func (d *Dispatcher) Start(ctx context.Context) {
	log.Println("[worker] dispatcher started")
	// time.NewTicker 创建一个定时器，每隔 d.interval 发送一个时间信号
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop() // 确保退出时停止定时器

	// for { ... } 是 Go 的无限循环（等价于 while(true)）
	for {
		// select 是 Go 的多路复用语句，用于同时等待多个 channel 操作
		// 哪个 channel 先有数据，就执行对应的 case
		select {
		case <-ctx.Done():
			// ctx.Done() 返回一个 channel，当 context 被取消时，这个 channel 会关闭
			// <-ctx.Done() 接收到信号，说明程序要退出了
			log.Println("[worker] dispatcher stopped")
			return
		case <-ticker.C:
			// ticker.C 是定时器的 channel，每隔 interval 会发送当前时间
			// 收到信号就执行一次轮询
			d.poll(ctx)
		}
	}
}

// poll 执行一次轮询：从数据库抢占任务，逐个投递
func (d *Dispatcher) poll(ctx context.Context) {
	// 从数据库中抢占一批待处理的任务
	jobs, err := d.store.FetchPendingJobs(d.batch, d.processingTimeout)
	if err != nil {
		log.Printf("[worker] fetch jobs error: %v", err)
		return
	}
	// range 遍历任务列表
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			// 每处理一个任务前检查是否需要停止
			return
		default:
			// default 分支：如果 ctx 未取消，继续执行投递
			d.deliver(job)
		}
	}
}

// deliver 执行单个任务的 HTTP 投递
func (d *Dispatcher) deliver(job model.Job) {
	log.Printf("[worker] delivering job %d -> %s %s (retry %d/%d)",
		job.ID, job.Method, job.URL, job.RetryCount, job.MaxRetries)

	// 发送 HTTP 请求
	err := d.doHTTPRequest(job)
	if err != nil {
		// 请求失败，进入失败处理流程（重试或最终标记失败）
		d.handleFailure(job, err)
		return
	}

	// 请求成功，将任务标记为 completed
	if err := d.store.MarkCompleted(job.ID); err != nil {
		log.Printf("[worker] mark completed error for job %d: %v", job.ID, err)
		return
	}
	log.Printf("[worker] job %d delivered successfully", job.ID)
}

// doHTTPRequest 根据 Job 的配置构建并发送 HTTP 请求
func (d *Dispatcher) doHTTPRequest(job model.Job) error {
	// io.Reader 是 Go 的核心接口，表示"可以从中读取数据的东西"
	var bodyReader io.Reader
	if job.Body != "" {
		// strings.NewReader 将字符串转换为 io.Reader
		bodyReader = strings.NewReader(job.Body)
	}

	// http.NewRequest 创建一个 HTTP 请求对象（还没发送）
	// 参数：方法、URL、请求体
	req, err := http.NewRequest(job.Method, job.URL, bodyReader)
	if err != nil {
		// fmt.Errorf 创建格式化的错误信息
		// %w 包装原始错误，保留错误链
		return fmt.Errorf("build request: %w", err)
	}

	// 设置自定义请求头
	// for k, v := range map —— 遍历 map，同时获取 key 和 value
	for k, v := range job.Headers {
		req.Header.Set(k, v)
	}

	// d.client.Do(req) 实际发送 HTTP 请求并获取响应
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("http call: %w", err)
	}
	// defer resp.Body.Close() 确保响应体被关闭（释放网络连接）
	defer resp.Body.Close()
	// 读取并丢弃响应体内容（必须读完才能正确回收连接到连接池）
	io.Copy(io.Discard, resp.Body)

	// 判断响应状态码：2xx 表示成功
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil // nil 表示没有错误，即成功
	}
	return fmt.Errorf("unexpected status %d", resp.StatusCode)
}

// handleFailure 处理投递失败的任务：决定重试还是最终标记失败
func (d *Dispatcher) handleFailure(job model.Job, deliverErr error) {
	// 检查是否已超过最大重试次数
	// RetryCount 从 0 开始，所以 +1 后和 MaxRetries 比较
	if job.RetryCount+1 >= job.MaxRetries {
		// 超过重试上限，永久标记为失败
		log.Printf("[worker] job %d exceeded max retries, marking failed: %v", job.ID, deliverErr)
		if err := d.store.MarkFailed(job.ID, deliverErr.Error()); err != nil {
			log.Printf("[worker] mark failed error for job %d: %v", job.ID, err)
		}
		return
	}

	// 还有重试机会，计算下次重试时间（指数退避）
	nextRetry := time.Now().UTC().Add(model.BackoffDuration(job.RetryCount))
	log.Printf("[worker] job %d failed (%v), scheduling retry at %s",
		job.ID, deliverErr, nextRetry.Format(time.DateTime))

	// 将任务标记为待重试状态
	if err := d.store.MarkRetry(job.ID, nextRetry); err != nil {
		log.Printf("[worker] mark retry error for job %d: %v", job.ID, err)
	}
}
