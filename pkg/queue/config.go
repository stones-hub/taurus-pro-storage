package queue

import (
	"fmt"
	"log"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// Config 队列处理器配置
//
// 零值约定：MaxRetries 的 0 表示“不重试”（不会被默认值覆盖）；时长、数量类字段为 0 时
// 由 MergeWithDefaults 补默认值（对它们而言 0 没有意义）。DefaultConfig() 给出推荐值，
// 但不会替你填 Source。
type Config struct {
	// 引擎类型：redis（Stream，至少一次，持久）或 channel（进程内，易失，崩溃即丢）
	EngineType engine.Type
	// Timeout Stop 排空在途任务的最长等待时间
	Timeout time.Duration

	// Source 队列名称（必填），仅允许 1-128 位字母数字及 . _ : -，且以字母数字开头。
	// 同名队列的多个进程/实例共享同一份任务。
	Source string
	// QueueSize 仅 channel 引擎：未清理任务（含失败留存）的数量上限，超出时 AddData 返回 ErrQueueFull
	QueueSize int

	// RedisClient 使用的 Redis 客户端；为 nil 时使用全局 redisx.Redis
	RedisClient *redisx.RedisClient

	// WorkerCount worker 协程数量
	WorkerCount int

	// 重试配置：第 n 次重试的延迟 = RetryDelay * RetryFactor^(n-1)，上限 MaxRetryDelay。
	// common.RetryableError 与 Processor panic 会被重试；其它错误直接进入失败留存。
	MaxRetries    int // 最大重试次数，0 表示不重试（直接失败留存）
	RetryDelay    time.Duration
	MaxRetryDelay time.Duration
	RetryFactor   float64

	// LeaseDuration 领取租约时长。Worker 每 LeaseDuration/3 自动续租；进程崩溃后，
	// 任务在租约到期后被其它实例重新领取。必须大于 Redis 抖动与 GC 暂停，默认 30s。
	LeaseDuration time.Duration
	// DedupWindow AddDataWithID 任务完成后的去重保留时长（默认 24h）。
	// 只影响“已成功完成”的任务；未完成（待处理/执行中/待重试）和失败留存的任务
	// 在被确认或重放完成前始终参与去重，不受此窗口限制。
	DedupWindow time.Duration
	// FailedRetention 失败留存的保留时长；0（默认）表示永久保留直到被重放，
	// 此时请通过 ListFailed 监控其规模。
	FailedRetention time.Duration
	// PollInterval 无任务时的轮询间隔，默认 200ms
	PollInterval time.Duration

	// ---- 以下为已废弃字段 ----
	// 可靠路径不再使用独立的 Reader / Processing / Retry / Failed 队列，设置这些字段不会有任何效果，
	// NewManager 会在日志中提示。失败任务始终留存（见 ListFailed/ReplayFailed），不再自动再次调用 Processor，
	// 也不再写 /tmp 文本文件。

	// Deprecated: 无效，Redis Stream 由消费者组直接领取，不再有 Reader。
	ReaderCount int
	// Deprecated: 无效，失败任务按 Source 命名空间留存。
	Failed string
	// Deprecated: 无效，已由租约/Stream pending 取代。
	Processing string
	// Deprecated: 无效，延迟重试按 Source 命名空间存放。
	Retry string
	// Deprecated: 无效，失败任务始终留存。
	EnableFailedQueue bool
	// Deprecated: 无效，重试由 MaxRetries 控制。
	EnableRetryQueue bool
	// Deprecated: 无效，失败任务不再自动重新处理。
	FailedInterval time.Duration
	// Deprecated: 无效。
	FailedBatch int
	// Deprecated: 无效，到期的重试在领取时原子转回就绪队列。
	RetryInterval time.Duration
	// Deprecated: 无效。
	RetryBatch int
	// Deprecated: 无效，不再依赖文本文件保存失败数据。
	EnableFailedDataToFile bool
	// Deprecated: 无效。
	FailedDataFilePath string
}

// DefaultConfig 返回推荐配置。Source 必须由调用方设置。
func DefaultConfig() *Config {
	return &Config{
		EngineType: engine.REDIS,
		Timeout:    time.Minute * 5,
		QueueSize:  1000,

		WorkerCount: 5,

		MaxRetries:    3,
		RetryDelay:    time.Second,
		MaxRetryDelay: time.Minute,
		RetryFactor:   2.0,

		LeaseDuration: 30 * time.Second,
		DedupWindow:   24 * time.Hour,
		PollInterval:  200 * time.Millisecond,
	}
}

// MergeWithDefaults 为“0 没有意义”的字段补默认值，并验证。
// 不会改写 MaxRetries、FailedRetention 以及任何 bool 字段，显式的 0/false 保持原样。
func (c *Config) MergeWithDefaults() error {
	d := DefaultConfig()
	if c.EngineType == "" {
		c.EngineType = d.EngineType
	}
	if c.Timeout == 0 {
		c.Timeout = d.Timeout
	}
	if c.QueueSize == 0 {
		c.QueueSize = d.QueueSize
	}
	if c.WorkerCount == 0 {
		c.WorkerCount = d.WorkerCount
	}
	if c.RetryDelay == 0 {
		c.RetryDelay = d.RetryDelay
	}
	if c.MaxRetryDelay == 0 {
		c.MaxRetryDelay = d.MaxRetryDelay
	}
	if c.RetryFactor == 0 {
		c.RetryFactor = d.RetryFactor
	}
	if c.LeaseDuration == 0 {
		c.LeaseDuration = d.LeaseDuration
	}
	if c.DedupWindow == 0 {
		c.DedupWindow = d.DedupWindow
	}
	if c.PollInterval == 0 {
		c.PollInterval = d.PollInterval
	}
	return c.Validate()
}

// Validate 验证配置的合理性
func (c *Config) Validate() error {
	switch c.EngineType {
	case engine.REDIS, engine.CHANNEL:
	default:
		return fmt.Errorf("unsupported queue engine type: %q", c.EngineType)
	}
	if err := engine.ValidateID(c.Source); err != nil {
		return fmt.Errorf("source queue name %q: %w", c.Source, err)
	}
	if c.QueueSize <= 0 {
		return fmt.Errorf("queue size must be positive, got %d", c.QueueSize)
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
	if c.LeaseDuration < 500*time.Millisecond {
		return fmt.Errorf("lease duration must be >= 500ms, got %v", c.LeaseDuration)
	}
	if c.DedupWindow < 0 || c.FailedRetention < 0 {
		return fmt.Errorf("dedup window and failed retention cannot be negative")
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll interval must be positive, got %v", c.PollInterval)
	}
	return nil
}

// warnDeprecated 对设置了已废弃字段的配置给出可见提示（不静默忽略）
func (c *Config) warnDeprecated() {
	var set []string
	add := func(cond bool, name string) {
		if cond {
			set = append(set, name)
		}
	}
	add(c.ReaderCount != 0, "ReaderCount")
	add(c.Failed != "", "Failed")
	add(c.Processing != "", "Processing")
	add(c.Retry != "", "Retry")
	add(c.EnableFailedQueue, "EnableFailedQueue")
	add(c.EnableRetryQueue, "EnableRetryQueue")
	add(c.FailedInterval != 0, "FailedInterval")
	add(c.FailedBatch != 0, "FailedBatch")
	add(c.RetryInterval != 0, "RetryInterval")
	add(c.RetryBatch != 0, "RetryBatch")
	add(c.EnableFailedDataToFile, "EnableFailedDataToFile")
	add(c.FailedDataFilePath != "", "FailedDataFilePath")
	if len(set) > 0 {
		log.Printf("queue.NewManager(): [Warning] 配置字段 %v 已废弃且不再生效：失败任务始终留存且不会自动重新处理，请改用 ListFailed/ReplayFailed", set)
	}
}
