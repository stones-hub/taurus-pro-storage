package engine

import (
	"context"
	"time"
)

// Engine 定义队列引擎接口
type Engine interface {
	// Push 将数据推入队列
	Push(ctx context.Context, queue string, data []byte) error

	// Pop 从队列中弹出数据
	Pop(ctx context.Context, queue string, timeout time.Duration) ([]byte, error)

	// BatchPop 批量从队列中弹出数据
	BatchPop(ctx context.Context, queue string, count int, timeout time.Duration) ([][]byte, error)

	// PushDelayed 将数据推入延迟队列
	PushDelayed(ctx context.Context, queue string, data []byte, delay time.Duration) error

	// PopDelayed 从延迟队列中弹出到期的数据
	PopDelayed(ctx context.Context, queue string, count int) ([][]byte, error)

	// Close 关闭队列引擎
	Close() error
}

// Type 队列引擎类型
type Type string

const (
	TypeRedis   Type = "redis"
	TypeKafka   Type = "kafka"
	TypeChannel Type = "channel"
)
