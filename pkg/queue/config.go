package queue

import (
	"fmt"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// Config 队列处理器配置
type Config struct {
	// 引擎类型
	EngineType engine.Type
	Timeout    time.Duration // 超时时间

	// 队列配置
	QueueSize  int    // 队列大小
	Source     string // 源队列名称
	Failed     string // 失败队列名称
	Processing string // 处理中队列名称
	Retry      string // 延迟重试队列名称

	// 读取配置
	ReaderCount int // 读取协程数量

	// Worker配置
	WorkerCount int // worker协程数量

	// 重试配置
	MaxRetries    int           // 最大重试次数
	RetryDelay    time.Duration // 初始重试延迟
	MaxRetryDelay time.Duration // 最大重试延迟
	RetryFactor   float64       // 重试延迟倍数

	// 失败队列配置
	EnableFailedQueue bool          // 是否启用失败队列处理
	FailedInterval    time.Duration // 失败队列处理间隔或超时时间
	FailedBatch       int           // 每次处理的最大数量

	// 重试队列配置
	EnableRetryQueue bool          // 是否启用延迟重试队列
	RetryInterval    time.Duration // 重试队列检查间隔或超时时间
	RetryBatch       int           // 每次处理的重试数据数量

}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		EngineType: engine.REDIS,
		Timeout:    time.Minute * 5,
		QueueSize:  1000,

		ReaderCount: 1,
		WorkerCount: 5,

		MaxRetries:    3,
		RetryDelay:    time.Second,
		MaxRetryDelay: time.Minute,
		RetryFactor:   2.0,

		EnableFailedQueue: true,
		FailedInterval:    time.Minute * 5,
		FailedBatch:       100,

		EnableRetryQueue: true,
		RetryInterval:    time.Second * 10,
		RetryBatch:       50,
	}
}

// MergeWithDefaults 将配置与默认配置合并，并验证
func (c *Config) MergeWithDefaults() error {
	defaults := DefaultConfig()

	// 合并配置
	if c.EngineType == "" {
		c.EngineType = defaults.EngineType
	}
	if c.Timeout == 0 {
		c.Timeout = defaults.Timeout
	}
	if c.QueueSize == 0 {
		c.QueueSize = defaults.QueueSize
	}
	if c.Source == "" {
		c.Source = defaults.Source
	}
	if c.Failed == "" {
		c.Failed = defaults.Failed
	}
	if c.Processing == "" {
		c.Processing = defaults.Processing
	}
	if c.Retry == "" {
		c.Retry = defaults.Retry
	}
	if c.ReaderCount == 0 {
		c.ReaderCount = defaults.ReaderCount
	}
	if c.WorkerCount == 0 {
		c.WorkerCount = defaults.WorkerCount
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = defaults.MaxRetries
	}
	if c.RetryDelay == 0 {
		c.RetryDelay = defaults.RetryDelay
	}
	if c.MaxRetryDelay == 0 {
		c.MaxRetryDelay = defaults.MaxRetryDelay
	}
	if c.RetryFactor == 0 {
		c.RetryFactor = defaults.RetryFactor
	}
	if c.FailedInterval == 0 {
		c.FailedInterval = defaults.FailedInterval
	}
	if c.FailedBatch == 0 {
		c.FailedBatch = defaults.FailedBatch
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = defaults.RetryInterval
	}
	if c.RetryBatch == 0 {
		c.RetryBatch = defaults.RetryBatch
	}

	// 验证配置
	return c.Validate()
}

// Validate 验证配置的合理性
func (c *Config) Validate() error {
	if c.QueueSize <= 0 {
		return fmt.Errorf("queue size must be positive, got %d", c.QueueSize)
	}
	if c.ReaderCount <= 0 {
		return fmt.Errorf("reader count must be positive, got %d", c.ReaderCount)
	}
	if c.WorkerCount <= 0 {
		return fmt.Errorf("worker count must be positive, got %d", c.WorkerCount)
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("max retries cannot be negative, got %d", c.MaxRetries)
	}
	if c.RetryDelay <= 0 {
		return fmt.Errorf("retry delay must be positive, got %v", c.RetryDelay)
	}
	if c.MaxRetryDelay <= 0 {
		return fmt.Errorf("max retry delay must be positive, got %v", c.MaxRetryDelay)
	}
	if c.RetryFactor <= 0 {
		return fmt.Errorf("retry factor must be positive, got %f", c.RetryFactor)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive, got %v", c.Timeout)
	}
	if c.EnableFailedQueue && c.FailedInterval <= 0 {
		return fmt.Errorf("failed interval must be positive when failed queue is enabled, got %v", c.FailedInterval)
	}
	if c.EnableFailedQueue && c.FailedBatch <= 0 {
		return fmt.Errorf("failed batch must be positive when failed queue is enabled, got %d", c.FailedBatch)
	}
	if c.EnableRetryQueue && c.RetryInterval <= 0 {
		return fmt.Errorf("retry interval must be positive when retry queue is enabled, got %v", c.RetryInterval)
	}
	if c.EnableRetryQueue && c.RetryBatch <= 0 {
		return fmt.Errorf("retry batch must be positive when retry queue is enabled, got %d", c.RetryBatch)
	}

	return nil
}
