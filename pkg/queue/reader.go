package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// Reader 表示一个读取协程
type Reader struct {
	id     int
	ctx    context.Context    // 上下文
	cancel context.CancelFunc // 取消函数
	wg     *sync.WaitGroup    // 等待组
	engine engine.Engine      // 队列引擎
	config *Config            // 配置
	closed bool               // 是否关闭
	mu     sync.RWMutex       // 互斥锁
	stop   chan struct{}      // 停止通道
}

// 初始化Reader对象， 用于从source队列中读取数据，并放入processing队列
func NewReader(id int, ctx context.Context, engine engine.Engine, config *Config) *Reader {
	if engine == nil {
		panicError := common.NewPanicError(errors.New("engine cannot be nil"), string(debug.Stack()))
		panic(panicError.FormatAsSimple())
	}
	if config == nil {
		panicError := common.NewPanicError(errors.New("config cannot be nil"), string(debug.Stack()))
		panic(panicError.FormatAsSimple())
	}

	ctx, cancel := context.WithCancel(ctx)

	return &Reader{
		id:     id,
		ctx:    ctx,
		cancel: cancel,
		wg:     &sync.WaitGroup{},
		engine: engine,
		config: config,
		closed: false,
		stop:   make(chan struct{}),
		mu:     sync.RWMutex{},
	}
}

func (r *Reader) Start() {
	r.wg.Add(1)
	go r.run()
}

// Stop 停止 Reader
func (r *Reader) Stop() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()
	// 关闭停止通道, 告诉协程无需再读取数据
	close(r.stop)

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Printf("Reader.Stop(): [Info] Reader[%d]队列, 成功停止读取。", r.id)
	case <-time.After(r.config.Timeout):
		log.Printf("Reader.Stop(): [Error] Reader[%d]队列, 停止读取超时。", r.id)
	}
}

// run Reader 的主循环
func (r *Reader) run() {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
		r.wg.Done()
	}()

	for {
		select {
		case <-r.stop:
			log.Printf("Reader.run(): [Info] Reader[%d]队列, 收到停止信号, 停止读取。", r.id)
			return
		case <-r.ctx.Done():
			log.Printf("Reader.run():[Info] Reader[%d]队列, 上下文context取消, 停止读取。", r.id)
			return
		default:
			if err := r.read(); err != nil {
				// 检查是否是context取消导致的错误
				if errors.Is(err, context.Canceled) {
					// context被取消，直接退出循环
					log.Printf("Reader.run(): [Info] Reader[%d]队列, context被取消, 停止读取。", r.id)
					return
				}
				// 其他错误记录日志，但继续运行
				log.Printf("Reader.run(): [Error] Reader[%d]队列, 读取数据错误(%v), 继续尝试读取。", r.id, err)
			}
		}
	}
}

// read 从source队列中读取数据，并放入processing队列
// 采用纯阻塞模式：直接使用阻塞读取，避免CPU空转，让出CPU资源
func (r *Reader) read() error {
	// 1. 从源队列读取数据 - 使用Reader的context，真正阻塞等待
	// 这里不设置超时，因为我们的目的就是阻塞等待数据
	data, err := r.engine.PopBlocking(r.ctx, r.config.Source)
	if err != nil {
		// 任何错误都直接返回，包括context.Canceled
		return err
	}

	// 2. 解析数据 - 纯内存操作，不需要context
	_, err = FromJSON(data)
	if err != nil {
		// 格式错误的数据移到失败队列
		// 移动到失败队列需要超时控制，避免长时间阻塞
		log.Printf("Reader.read(): [Error] Reader[%d]队列, 解析数据错误(%v), 将数据(%s)移动到失败队列。", r.id, err, string(data))
		return PushToFailedQueueNonBlocking(r.ctx, r.engine, r.config, data)
	}

	// 3. 移动到处理队列 - 需要超时控制，避免长时间阻塞
	// 这里设置超时是因为我们不想因为处理队列满而长时间阻塞
	err = PushToProcessingQueueNonBlocking(r.ctx, r.engine, r.config, data)
	if err != nil {
		log.Printf("Reader.read(): [Error] Reader[%d]队列, 将数据(%s)移动到处理中队列错误(%v), 数据将丢失。", r.id, string(data), err)
		return err
	}

	return nil
}
