package engine

import (
	"context"
	"log"
	"sync"
	"time"
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
func NewChannelEngine() Engine {
	return &ChannelEngine{
		queues:       make(map[string]chan []byte),
		delayedQueue: make(map[int64][][]byte),
	}
}

func (e *ChannelEngine) getOrCreateQueue(queue string) chan []byte {
	e.queuesMu.RLock()
	ch, exists := e.queues[queue]
	e.queuesMu.RUnlock()

	if exists {
		return ch
	}

	e.queuesMu.Lock()
	defer e.queuesMu.Unlock()

	// 双重检查, 防止在读的期间, 队列被创建
	if ch, exists = e.queues[queue]; exists {
		return ch
	}

	// 创建新队列
	ch = make(chan []byte, 1000) // 使用缓冲通道
	e.queues[queue] = ch
	return ch
}

// Push 将数据推入队列(source 和 failed, processing)
func (e *ChannelEngine) Push(ctx context.Context, queue string, data []byte) error {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ChannelEngine.Push: Recovered from panic: %v", r)
		}
	}()

	ch := e.getOrCreateQueue(queue)

	select {
	case ch <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Pop 从队列中弹出数据(source 和 failed, processing)
func (e *ChannelEngine) Pop(ctx context.Context, queue string, timeout time.Duration) ([]byte, error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ChannelEngine.Pop: Recovered from panic: %v", r)
		}
	}()

	ch := e.getOrCreateQueue(queue)

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case data := <-ch:
		return data, nil
	case <-timer.C:
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// BatchPop 批量从队列中弹出数据(source 和 failed, processing)
func (e *ChannelEngine) BatchPop(ctx context.Context, queue string, count int, timeout time.Duration) ([][]byte, error) {
	ch := e.getOrCreateQueue(queue)
	result := make([][]byte, 0, count)

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for i := 0; i < count; i++ {
		select {
		case data := <-ch:
			result = append(result, data)
		case <-timer.C:
			return result, nil
		case <-ctx.Done():
			return result, ctx.Err()
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
	e.delayedQueueMu.Lock()
	defer e.delayedQueueMu.Unlock()

	var result [][]byte
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
				delete(e.delayedQueue, timestamp)
			} else {
				// 只取一部分
				result = append(result, dataArray[:canTake]...)
				e.delayedQueue[timestamp] = dataArray[canTake:]
			}
		}
	}

	return result, nil
}

func (e *ChannelEngine) Close() error {
	// 按照固定顺序获取锁，避免死锁
	// 顺序：queuesMu -> delayedQueueMu

	e.queuesMu.Lock()
	e.delayedQueueMu.Lock()

	// 关闭所有队列
	for _, ch := range e.queues {
		close(ch)
	}
	e.queues = make(map[string]chan []byte)

	// 清空延迟队列
	e.delayedQueue = make(map[int64][][]byte)

	// 按照获取顺序的反序释放锁
	e.delayedQueueMu.Unlock()
	e.queuesMu.Unlock()

	return nil
}
