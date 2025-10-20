package main

// 注意：这个文件需要单独运行，不能与其他main函数一起编译
// 运行方式：go run config_example.go

import (
	"log"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// 配置示例：展示不同场景下的队列配置

// 基础配置示例
func createBasicConfig() *queue.Config {
	return &queue.Config{
		// 使用内存队列（适合开发测试）
		EngineType: engine.CHANNEL,

		// 基础队列配置
		QueueSize:  1000,
		Source:     "basic_source",
		Failed:     "basic_failed",
		Processing: "basic_processing",
		Retry:      "basic_retry",

		// 基础并发配置
		ReaderCount: 1,
		WorkerCount: 3,

		// 基础重试配置
		MaxRetries:    3,
		RetryDelay:    time.Second,
		MaxRetryDelay: time.Minute,
		RetryFactor:   2.0,

		// 基础失败队列配置
		EnableFailedQueue: true,
		FailedInterval:    time.Minute * 5,
		FailedBatch:       50,

		// 基础重试队列配置
		EnableRetryQueue: true,
		RetryInterval:    time.Second * 10,
		RetryBatch:       25,

		// 基础数据落盘配置
		EnableFailedDataToFile: true,
		FailedDataFilePath:     "./logs/basic_failed.log",

		// 基础超时配置
		Timeout: time.Minute * 2,
	}
}

// 高并发配置示例
func createHighConcurrencyConfig() *queue.Config {
	return &queue.Config{
		// 使用Redis队列（适合生产环境）
		EngineType: engine.REDIS,

		// 高并发队列配置
		QueueSize:  10000,
		Source:     "high_concurrency_source",
		Failed:     "high_concurrency_failed",
		Processing: "high_concurrency_processing",
		Retry:      "high_concurrency_retry",

		// 高并发配置
		ReaderCount: 5,  // 更多读取协程
		WorkerCount: 20, // 更多工作协程

		// 高并发重试配置
		MaxRetries:    5,
		RetryDelay:    time.Second * 2,
		MaxRetryDelay: time.Minute * 3,
		RetryFactor:   1.8,

		// 高并发失败队列配置
		EnableFailedQueue: true,
		FailedInterval:    time.Minute * 2,
		FailedBatch:       200, // 更大的批处理

		// 高并发重试队列配置
		EnableRetryQueue: true,
		RetryInterval:    time.Second * 15,
		RetryBatch:       100, // 更大的批处理

		// 高并发数据落盘配置
		EnableFailedDataToFile: true,
		FailedDataFilePath:     "./logs/high_concurrency_failed.log",

		// 高并发超时配置
		Timeout: time.Minute * 5,
	}
}

// 低延迟配置示例
func createLowLatencyConfig() *queue.Config {
	return &queue.Config{
		// 使用内存队列（最低延迟）
		EngineType: engine.CHANNEL,

		// 低延迟队列配置
		QueueSize:  5000,
		Source:     "low_latency_source",
		Failed:     "low_latency_failed",
		Processing: "low_latency_processing",
		Retry:      "low_latency_retry",

		// 低延迟并发配置
		ReaderCount: 3,
		WorkerCount: 15,

		// 低延迟重试配置
		MaxRetries:    2,                      // 更少重试次数
		RetryDelay:    time.Millisecond * 500, // 更短的重试延迟
		MaxRetryDelay: time.Second * 30,       // 更短的最大重试延迟
		RetryFactor:   2.5,                    // 更激进的重试倍数

		// 低延迟失败队列配置
		EnableFailedQueue: true,
		FailedInterval:    time.Second * 30, // 更频繁的失败队列处理
		FailedBatch:       20,               // 更小的批处理

		// 低延迟重试队列配置
		EnableRetryQueue: true,
		RetryInterval:    time.Second * 5, // 更频繁的重试检查
		RetryBatch:       10,              // 更小的批处理

		// 低延迟数据落盘配置
		EnableFailedDataToFile: true,
		FailedDataFilePath:     "./logs/low_latency_failed.log",

		// 低延迟超时配置
		Timeout: time.Second * 30, // 更短的超时时间
	}
}

// 高可靠性配置示例
func createHighReliabilityConfig() *queue.Config {
	return &queue.Config{
		// 使用Redis队列（高可靠性）
		EngineType: engine.REDIS,

		// 高可靠性队列配置
		QueueSize:  20000,
		Source:     "high_reliability_source",
		Failed:     "high_reliability_failed",
		Processing: "high_reliability_processing",
		Retry:      "high_reliability_retry",

		// 高可靠性并发配置
		ReaderCount: 8,
		WorkerCount: 30,

		// 高可靠性重试配置
		MaxRetries:    10,               // 更多重试次数
		RetryDelay:    time.Second * 5,  // 更长的初始重试延迟
		MaxRetryDelay: time.Minute * 10, // 更长的最大重试延迟
		RetryFactor:   1.5,              // 更温和的重试倍数

		// 高可靠性失败队列配置
		EnableFailedQueue: true,
		FailedInterval:    time.Minute * 10, // 更长的失败队列处理间隔
		FailedBatch:       500,              // 更大的批处理

		// 高可靠性重试队列配置
		EnableRetryQueue: true,
		RetryInterval:    time.Minute * 2, // 更长的重试检查间隔
		RetryBatch:       200,             // 更大的批处理

		// 高可靠性数据落盘配置
		EnableFailedDataToFile: true,
		FailedDataFilePath:     "./logs/high_reliability_failed.log",

		// 高可靠性超时配置
		Timeout: time.Minute * 10, // 更长的超时时间
	}
}

// 演示不同配置的使用
func demonstrateConfigs() {
	log.Println("=== 队列配置示例演示 ===")

	// 1. 基础配置
	log.Println("1. 基础配置（适合开发测试）")
	basicConfig := createBasicConfig()
	log.Printf("   - 引擎类型: %v", basicConfig.EngineType)
	log.Printf("   - 队列大小: %d", basicConfig.QueueSize)
	log.Printf("   - Worker数量: %d", basicConfig.WorkerCount)
	log.Printf("   - 最大重试次数: %d", basicConfig.MaxRetries)
	log.Printf("   - 超时时间: %v", basicConfig.Timeout)

	// 2. 高并发配置
	log.Println("\n2. 高并发配置（适合生产环境）")
	highConcurrencyConfig := createHighConcurrencyConfig()
	log.Printf("   - 引擎类型: %v", highConcurrencyConfig.EngineType)
	log.Printf("   - 队列大小: %d", highConcurrencyConfig.QueueSize)
	log.Printf("   - Worker数量: %d", highConcurrencyConfig.WorkerCount)
	log.Printf("   - 最大重试次数: %d", highConcurrencyConfig.MaxRetries)
	log.Printf("   - 超时时间: %v", highConcurrencyConfig.Timeout)

	// 3. 低延迟配置
	log.Println("\n3. 低延迟配置（适合实时处理）")
	lowLatencyConfig := createLowLatencyConfig()
	log.Printf("   - 引擎类型: %v", lowLatencyConfig.EngineType)
	log.Printf("   - 队列大小: %d", lowLatencyConfig.QueueSize)
	log.Printf("   - Worker数量: %d", lowLatencyConfig.WorkerCount)
	log.Printf("   - 最大重试次数: %d", lowLatencyConfig.MaxRetries)
	log.Printf("   - 超时时间: %v", lowLatencyConfig.Timeout)

	// 4. 高可靠性配置
	log.Println("\n4. 高可靠性配置（适合关键业务）")
	highReliabilityConfig := createHighReliabilityConfig()
	log.Printf("   - 引擎类型: %v", highReliabilityConfig.EngineType)
	log.Printf("   - 队列大小: %d", highReliabilityConfig.QueueSize)
	log.Printf("   - Worker数量: %d", highReliabilityConfig.WorkerCount)
	log.Printf("   - 最大重试次数: %d", highReliabilityConfig.MaxRetries)
	log.Printf("   - 超时时间: %v", highReliabilityConfig.Timeout)

	log.Println("\n=== 配置选择建议 ===")
	log.Println("• 开发测试: 使用基础配置")
	log.Println("• 生产环境: 使用高并发配置")
	log.Println("• 实时处理: 使用低延迟配置")
	log.Println("• 关键业务: 使用高可靠性配置")
	log.Println("• 自定义需求: 根据业务特点调整参数")
}

func main() {
	demonstrateConfigs()
}
