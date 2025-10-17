package engine

import (
	"context"
	"time"
)

// Engine 定义队列引擎接口
type Engine interface {
	// PushBlocking 阻塞式推送，队列满时阻塞直到有空间或上下文取消
	PushBlocking(ctx context.Context, name string, data []byte) error

	// PushNonBlocking 非阻塞推送，立即返回，队列满时返回特定错误
	PushNonBlocking(ctx context.Context, name string, data []byte) error

	// PopBlocking 阻塞式读取，有数据立即返回，无数据时阻塞直到有数据或上下文取消
	PopBlocking(ctx context.Context, name string) ([]byte, error)

	// PopNonBlocking 非阻塞读取，立即返回，无数据时返回特定错误
	PopNonBlocking(ctx context.Context, name string) ([]byte, error)

	// BatchPopBlocking 批量阻塞式读取
	BatchPopBlocking(ctx context.Context, name string, count int) ([][]byte, error)

	// BatchPopNonBlocking 批量非阻塞读取
	BatchPopNonBlocking(ctx context.Context, name string, count int) ([][]byte, error)

	// PushDelayed 将数据推入延迟队列
	PushDelayed(ctx context.Context, name string, data []byte, delay time.Duration) error

	// PopDelayed 从延迟队列中弹出到期的数据
	PopDelayed(ctx context.Context, name string, count int) ([][]byte, error)

	// Close 关闭队列引擎
	Close() error
}

// Type 队列引擎类型
type Type string

const (
	REDIS   Type = "redis"
	KAFKA   Type = "kafka"
	CHANNEL Type = "channel"
)
