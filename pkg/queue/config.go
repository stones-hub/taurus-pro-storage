package queue

import (
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// Config 队列处理器配置
type Config struct {
	// 引擎类型
	EngineType engine.Type

	// 队列配置
	Source     string // 源队列名称
	Failed     string // 失败队列名称
	Processing string // 处理中队列名称
	Retry      string // 延迟重试队列名称

	// 读取配置
	ReaderCount    int           // 读取协程数量
	ReaderBatch    int           // 批量读取大小
	ReaderInterval time.Duration // 无数据时休眠时间

	// Worker配置
	WorkerCount   int           // worker协程数量
	WorkerTimeout time.Duration // 处理超时时间

	// 重试配置
	MaxRetries    int           // 最大重试次数
	RetryDelay    time.Duration // 初始重试延迟
	MaxRetryDelay time.Duration // 最大重试延迟
	RetryFactor   float64       // 重试延迟倍数

	// 失败队列配置
	EnableFailedQueue bool          // 是否启用失败队列处理
	FailedInterval    time.Duration // 处理间隔
	FailedBatch       int           // 每次处理的最大数量

	// 重试队列配置
	EnableRetryQueue bool          // 是否启用延迟重试队列
	RetryInterval    time.Duration // 重试队列检查间隔
	RetryBatch       int           // 每次处理的重试数据数量
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		EngineType:        engine.TypeRedis,
		ReaderCount:       1,
		ReaderBatch:       100,
		ReaderInterval:    time.Second,
		WorkerCount:       5,
		WorkerTimeout:     time.Minute,
		MaxRetries:        3,
		RetryDelay:        time.Second,
		MaxRetryDelay:     time.Minute,
		RetryFactor:       2.0,
		EnableFailedQueue: true,
		FailedInterval:    time.Minute * 5,
		FailedBatch:       100,
		EnableRetryQueue:  true,
		RetryInterval:     time.Second * 10,
		RetryBatch:        50,
	}
}
