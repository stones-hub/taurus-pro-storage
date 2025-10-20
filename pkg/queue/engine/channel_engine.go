package engine

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
)

// ChannelEngine Channel队列引擎实现
//
// 锁的使用策略：
// 1. queuesMu (RWMutex): 保护queues map的访问
// 2. delayedQueueMu (RWMutex): 保护delayedQueue map的访问
//
// 死锁预防原则：
// 1. 锁的获取顺序：queuesMu -> delayedQueueMu
// 2. 锁的释放顺序：delayedQueueMu -> queuesMu
// 3. 永远不要在持有锁时进行可能阻塞的操作（如channel写入）
// 4. 使用defer确保锁的释放
type ChannelEngine struct {
	// queues 存储所有队列，使用Go的channel实现
	// 包括：source队列、failed队列、processing队列等
	// 使用channel的好处是天然支持并发安全，无需额外的锁机制
	queues map[string]chan []byte
	// queuesMu 保护queues map的读写锁
	queuesMu sync.RWMutex

	// delayedQueue 存储延迟队列，使用map[int64][][]byte实现
	// key为到期时间戳，value为该时间到期的数据数组
	// 优势：插入O(1)，查找到期数据O(1)，删除O(1)
	delayedQueue map[int64][][]byte
	// delayedQueueMu 保护delayedQueue的读写锁
	delayedQueueMu sync.RWMutex
}

// NewChannelEngine 创建Channel队列引擎
func NewChannelEngine(queueSize int, source, failed, processing string) Engine {
	engine := &ChannelEngine{
		queues:       make(map[string]chan []byte),
		delayedQueue: make(map[int64][][]byte),
	}

	// 预创建所有需要的队列
	if queueSize <= 0 {
		queueSize = 1000 // 默认大小
	}

	// 创建所有业务队列
	engine.queues[source] = make(chan []byte, queueSize)
	engine.queues[processing] = make(chan []byte, queueSize)
	engine.queues[failed] = make(chan []byte, queueSize)

	return engine
}

// getQueue 获取已存在的队列，如果队列不存在则返回nil
// 由于队列在初始化时已经预创建，这里只需要获取即可
func (e *ChannelEngine) getQueue(name string) chan []byte {
	e.queuesMu.RLock()
	defer e.queuesMu.RUnlock()

	return e.queues[name]
}

// PushBlocking 阻塞式推送数据到队列
func (e *ChannelEngine) PushBlocking(ctx context.Context, queue string, data []byte) error {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	ch := e.getQueue(queue)
	if ch == nil {
		return fmt.Errorf("队列 %s 不存在", queue)
	}

	select {
	case ch <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PushNonBlocking 非阻塞推送数据到队列
func (e *ChannelEngine) PushNonBlocking(ctx context.Context, queue string, data []byte) error {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	ch := e.getQueue(queue)
	if ch == nil {
		return common.ErrQueueNotExist
	}

	select {
	case ch <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		// 队列满，返回特定错误
		return common.ErrQueueFull
	}
}

// PopBlocking 阻塞式读取，有数据立即返回，无数据时阻塞直到有数据或上下文取消
func (e *ChannelEngine) PopBlocking(ctx context.Context, queue string) ([]byte, error) {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	ch := e.getQueue(queue)
	if ch == nil {
		return nil, common.ErrQueueNotExist
	}

	select {
	case data := <-ch:
		return data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// PopNonBlocking 非阻塞读取，立即返回，无数据时返回特定错误
func (e *ChannelEngine) PopNonBlocking(ctx context.Context, queue string) ([]byte, error) {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	ch := e.getQueue(queue)
	if ch == nil {
		return nil, common.ErrQueueNotExist
	}

	select {
	case data := <-ch:
		return data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		// 队列为空，返回特定错误
		return nil, common.ErrQueueEmpty
	}
}

// BatchPopBlocking 批量阻塞式读取
func (e *ChannelEngine) BatchPopBlocking(ctx context.Context, queue string, count int) ([][]byte, error) {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	ch := e.getQueue(queue)
	if ch == nil {
		return nil, common.ErrQueueNotExist
	}

	result := make([][]byte, 0, count)

	for i := 0; i < count; i++ {
		select {
		case data := <-ch:
			result = append(result, data)
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}

	return result, nil
}

// BatchPopNonBlocking 批量非阻塞读取
func (e *ChannelEngine) BatchPopNonBlocking(ctx context.Context, queue string, count int) ([][]byte, error) {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	ch := e.getQueue(queue)
	if ch == nil {
		return nil, common.ErrQueueNotExist
	}

	result := make([][]byte, 0, count)

	for i := 0; i < count; i++ {
		select {
		case data := <-ch:
			result = append(result, data)
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			// 如果通道为空，直接返回已获取的数据
			return result, nil
		}
	}

	return result, nil
}

// PushDelayed 将数据推入延迟队列
// 使用Map实现，插入时间复杂度O(1)
func (e *ChannelEngine) PushDelayed(ctx context.Context, queue string, data []byte, delay time.Duration) error {

	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	// 计算到期时间戳
	expireTimestamp := time.Now().Add(delay).Unix()

	e.delayedQueueMu.Lock()
	defer e.delayedQueueMu.Unlock()

	// 直接按时间戳分组存储，无需排序
	if e.delayedQueue[expireTimestamp] == nil {
		e.delayedQueue[expireTimestamp] = make([][]byte, 0)
	}
	e.delayedQueue[expireTimestamp] = append(e.delayedQueue[expireTimestamp], data)

	return nil
}

// PopDelayed 从延迟队列中弹出到期的数据
// 使用Map实现，查找到期数据时间复杂度O(1)
func (e *ChannelEngine) PopDelayed(ctx context.Context, queue string, count int) ([][]byte, error) {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	e.delayedQueueMu.Lock()
	defer e.delayedQueueMu.Unlock()

	var result [][]byte
	var expiredTimestamps []int64
	now := time.Now().Unix()

	// 遍历所有到期的时间戳
	for timestamp, dataArray := range e.delayedQueue {
		if timestamp <= now {
			// 如果已经取够了数据，就停止
			if len(result) >= count {
				break
			}

			// 计算还能取多少数据
			canTake := count - len(result)

			// 取数据（取较小的那个数量）
			if len(dataArray) <= canTake {
				// 全部取走
				result = append(result, dataArray...)
				expiredTimestamps = append(expiredTimestamps, timestamp) // 标记为过期
			} else {
				// 只取一部分
				result = append(result, dataArray[:canTake]...)
				e.delayedQueue[timestamp] = dataArray[canTake:]
			}
		}
	}

	// 清理过期的时间戳
	for _, timestamp := range expiredTimestamps {
		delete(e.delayedQueue, timestamp)
	}

	return result, nil
}

func (e *ChannelEngine) Close() error {
	// 按照固定顺序获取锁，避免死锁
	// 顺序：queuesMu -> delayedQueueMu

	e.queuesMu.Lock()
	defer e.queuesMu.Unlock()
	e.delayedQueueMu.Lock()
	defer e.delayedQueueMu.Unlock()

	// 关闭所有队列
	for _, ch := range e.queues {
		close(ch)
	}
	e.queues = make(map[string]chan []byte)

	// 清空延迟队列
	e.delayedQueue = make(map[int64][][]byte)
	return nil
}
