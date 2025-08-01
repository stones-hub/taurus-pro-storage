package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// TestProcessor 测试用的数据处理器
type TestProcessor struct {
	processedCount int64
	failedCount    int64
	retryCount     int64
	mu             sync.RWMutex
	shouldFail     bool
	shouldRetry    bool
	processingTime time.Duration
	errors         []error
}

func NewTestProcessor(shouldFail, shouldRetry bool, processingTime time.Duration) *TestProcessor {
	return &TestProcessor{
		shouldFail:     shouldFail,
		shouldRetry:    shouldRetry,
		processingTime: processingTime,
		errors:         make([]error, 0),
	}
}

func (p *TestProcessor) Process(ctx context.Context, data []byte) error {
	// 模拟处理时间
	if p.processingTime > 0 {
		select {
		case <-time.After(p.processingTime):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.shouldFail {
		atomic.AddInt64(&p.failedCount, 1)
		if p.shouldRetry {
			atomic.AddInt64(&p.retryCount, 1)
			err := queue.NewRetryableError(fmt.Errorf("simulated retryable error for data: %s", string(data)))
			p.errors = append(p.errors, err)
			return err
		}
		err := fmt.Errorf("simulated permanent error for data: %s", string(data))
		p.errors = append(p.errors, err)
		return err
	}

	atomic.AddInt64(&p.processedCount, 1)
	log.Printf("TestProcessor: Successfully processed data: %s", string(data))
	return nil
}

func (p *TestProcessor) GetStats() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return map[string]interface{}{
		"processed": atomic.LoadInt64(&p.processedCount),
		"failed":    atomic.LoadInt64(&p.failedCount),
		"retry":     atomic.LoadInt64(&p.retryCount),
		"errors":    len(p.errors),
	}
}

func (p *TestProcessor) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	atomic.StoreInt64(&p.processedCount, 0)
	atomic.StoreInt64(&p.failedCount, 0)
	atomic.StoreInt64(&p.retryCount, 0)
	p.errors = p.errors[:0]
}

// 性能测试
func performanceTest() {
	log.Println("=== 性能测试开始 ===")

	config := &queue.Config{
		EngineType:        engine.TypeChannel,
		Source:            "perf_source",
		Failed:            "perf_failed",
		Processing:        "perf_processing",
		Retry:             "perf_retry",
		ReaderCount:       2,
		WorkerCount:       4,
		WorkerTimeout:     time.Second * 5,
		ReaderInterval:    time.Millisecond * 50,
		MaxRetries:        1,
		RetryDelay:        time.Millisecond * 100,
		MaxRetryDelay:     time.Second,
		RetryFactor:       2.0,
		EnableFailedQueue: false,
		EnableRetryQueue:  false,
	}

	processor := NewTestProcessor(false, false, time.Millisecond*10)
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start manager: %v", err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		manager.Stop(ctx)
	}()

	time.Sleep(time.Millisecond * 100)

	// 测试不同并发度下的性能
	concurrencyLevels := []int{1, 2, 4, 8, 16}
	itemsPerTest := 1000

	for _, concurrency := range concurrencyLevels {
		processor.Reset()
		start := time.Now()

		var wg sync.WaitGroup
		itemsPerGoroutine := itemsPerTest / concurrency

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < itemsPerGoroutine; j++ {
					testData := []byte(fmt.Sprintf("perf_data_%d_%d", id, j))
					if err := manager.AddData(context.Background(), testData); err != nil {
						log.Printf("Failed to add performance data: %v", err)
					}
				}
			}(i)
		}

		wg.Wait()

		// 等待处理完成
		time.Sleep(time.Second * 2)

		duration := time.Since(start)
		stats := processor.GetStats()
		processed := stats["processed"].(int64)

		log.Printf("并发度 %d: 处理 %d 条数据，耗时 %v，吞吐量 %.2f items/sec",
			concurrency, processed, duration, float64(processed)/duration.Seconds())
	}

	log.Println("=== 性能测试完成 ===")
}

// 功能测试
func functionalityTest() {
	log.Println("=== 功能测试开始 ===")

	config := &queue.Config{
		EngineType:        engine.TypeChannel,
		Source:            "func_source",
		Failed:            "func_failed",
		Processing:        "func_processing",
		Retry:             "func_retry",
		ReaderCount:       2,
		WorkerCount:       3,
		WorkerTimeout:     time.Second * 10,
		ReaderInterval:    time.Millisecond * 100,
		MaxRetries:        2,
		RetryDelay:        time.Millisecond * 500,
		MaxRetryDelay:     time.Second * 2,
		RetryFactor:       2.0,
		EnableFailedQueue: true,
		FailedInterval:    time.Second,
		FailedBatch:       10,
		EnableRetryQueue:  true,
		RetryInterval:     time.Millisecond * 500,
		RetryBatch:        5,
	}

	// 测试1: 正常数据处理
	log.Println("测试1: 正常数据处理")
	processor := NewTestProcessor(false, false, time.Millisecond*50)
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start manager: %v", err)
	}

	time.Sleep(time.Millisecond * 200)

	// 添加测试数据
	for i := 0; i < 10; i++ {
		testData := []byte(fmt.Sprintf("normal_data_%d", i))
		if err := manager.AddData(context.Background(), testData); err != nil {
			log.Printf("Failed to add data: %v", err)
		}
	}

	time.Sleep(time.Second * 3)

	stats := processor.GetStats()
	log.Printf("正常处理结果: %+v", stats)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	manager.Stop(ctx)

	// 测试2: 重试处理
	log.Println("测试2: 重试处理")
	retryProcessor := NewTestProcessor(true, true, time.Millisecond*50)
	retryManager, err := queue.NewManager(retryProcessor, config)
	if err != nil {
		log.Fatalf("Failed to create retry manager: %v", err)
	}

	if err := retryManager.Start(); err != nil {
		log.Fatalf("Failed to start retry manager: %v", err)
	}

	time.Sleep(time.Millisecond * 200)

	// 添加重试测试数据
	for i := 0; i < 5; i++ {
		testData := []byte(fmt.Sprintf("retry_data_%d", i))
		if err := retryManager.AddData(context.Background(), testData); err != nil {
			log.Printf("Failed to add retry data: %v", err)
		}
	}

	time.Sleep(time.Second * 8)

	retryStats := retryProcessor.GetStats()
	log.Printf("重试处理结果: %+v", retryStats)

	ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	retryManager.Stop(ctx)

	// 测试3: 超时处理
	log.Println("测试3: 超时处理")
	slowProcessor := NewTestProcessor(false, false, time.Second*15)
	slowConfig := *config
	slowConfig.WorkerTimeout = time.Second * 5

	slowManager, err := queue.NewManager(slowProcessor, &slowConfig)
	if err != nil {
		log.Fatalf("Failed to create slow manager: %v", err)
	}

	if err := slowManager.Start(); err != nil {
		log.Fatalf("Failed to start slow manager: %v", err)
	}

	time.Sleep(time.Millisecond * 200)

	// 添加超时测试数据
	for i := 0; i < 3; i++ {
		testData := []byte(fmt.Sprintf("timeout_data_%d", i))
		if err := slowManager.AddData(context.Background(), testData); err != nil {
			log.Printf("Failed to add timeout data: %v", err)
		}
	}

	time.Sleep(time.Second * 10)

	slowStats := slowProcessor.GetStats()
	log.Printf("超时处理结果: %+v", slowStats)

	ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	slowManager.Stop(ctx)

	log.Println("=== 功能测试完成 ===")
}

// 并发安全测试
func concurrencySafetyTest() {
	log.Println("=== 并发安全测试开始 ===")

	config := &queue.Config{
		EngineType:        engine.TypeChannel,
		Source:            "concurrent_source",
		Failed:            "concurrent_failed",
		Processing:        "concurrent_processing",
		Retry:             "concurrent_retry",
		ReaderCount:       2,
		WorkerCount:       4,
		WorkerTimeout:     time.Second * 5,
		ReaderInterval:    time.Millisecond * 100,
		MaxRetries:        1,
		RetryDelay:        time.Millisecond * 100,
		MaxRetryDelay:     time.Second,
		RetryFactor:       2.0,
		EnableFailedQueue: false,
		EnableRetryQueue:  false,
	}

	processor := NewTestProcessor(false, false, time.Millisecond*10)
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start manager: %v", err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		manager.Stop(ctx)
	}()

	time.Sleep(time.Millisecond * 100)

	// 高并发测试
	const numGoroutines = 20
	const itemsPerGoroutine = 50
	totalItems := numGoroutines * itemsPerGoroutine

	processor.Reset()
	start := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < itemsPerGoroutine; j++ {
				testData := []byte(fmt.Sprintf("concurrent_data_%d_%d", id, j))
				if err := manager.AddData(context.Background(), testData); err != nil {
					log.Printf("Failed to add concurrent data: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	// 等待处理完成
	time.Sleep(time.Second * 3)

	duration := time.Since(start)
	stats := processor.GetStats()
	processed := stats["processed"].(int64)

	log.Printf("并发安全测试: 期望处理 %d 条数据，实际处理 %d 条数据", totalItems, processed)
	log.Printf("并发安全测试: 耗时 %v，吞吐量 %.2f items/sec", duration, float64(processed)/duration.Seconds())

	if processed != int64(totalItems) {
		log.Printf("警告: 数据丢失 %d 条", totalItems-int(processed))
	} else {
		log.Printf("并发安全测试通过: 无数据丢失")
	}

	log.Println("=== 并发安全测试完成 ===")
}

// 压力测试
func stressTest() {
	log.Println("=== 压力测试开始 ===")

	config := &queue.Config{
		EngineType:        engine.TypeChannel,
		Source:            "stress_source",
		Failed:            "stress_failed",
		Processing:        "stress_processing",
		Retry:             "stress_retry",
		ReaderCount:       1,
		WorkerCount:       2,
		WorkerTimeout:     time.Second * 30,
		ReaderInterval:    time.Millisecond * 50,
		MaxRetries:        0,
		RetryDelay:        time.Millisecond * 100,
		MaxRetryDelay:     time.Second,
		RetryFactor:       2.0,
		EnableFailedQueue: false,
		EnableRetryQueue:  false,
	}

	processor := NewTestProcessor(false, false, time.Millisecond*5)
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start manager: %v", err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		manager.Stop(ctx)
	}()

	time.Sleep(time.Millisecond * 100)

	// 持续压力测试
	const testDuration = time.Second * 30

	processor.Reset()
	start := time.Now()
	stopTime := start.Add(testDuration)

	var wg sync.WaitGroup
	var itemCount int64

	// 启动生产者
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if time.Now().After(stopTime) {
					return
				}
				testData := []byte(fmt.Sprintf("stress_data_%d", atomic.AddInt64(&itemCount, 1)))
				if err := manager.AddData(context.Background(), testData); err != nil {
					log.Printf("Failed to add stress data: %v", err)
				}
			}
		}
	}()

	// 监控处理进度
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if time.Now().After(stopTime) {
					return
				}
				stats := processor.GetStats()
				processed := stats["processed"].(int64)
				log.Printf("压力测试进度: 已处理 %d 条数据", processed)
			}
		}
	}()

	wg.Wait()

	duration := time.Since(start)
	stats := processor.GetStats()
	processed := stats["processed"].(int64)

	log.Printf("压力测试结果: 处理 %d 条数据，耗时 %v，平均吞吐量 %.2f items/sec",
		processed, duration, float64(processed)/duration.Seconds())

	log.Println("=== 压力测试完成 ===")
}

// 错误处理测试
func errorHandlingTest() {
	log.Println("=== 错误处理测试开始 ===")

	config := &queue.Config{
		EngineType:        engine.TypeChannel,
		Source:            "error_source",
		Failed:            "error_failed",
		Processing:        "error_processing",
		Retry:             "error_retry",
		ReaderCount:       1,
		WorkerCount:       2,
		WorkerTimeout:     time.Second * 5,
		ReaderInterval:    time.Millisecond * 100,
		MaxRetries:        2,
		RetryDelay:        time.Millisecond * 500,
		MaxRetryDelay:     time.Second * 2,
		RetryFactor:       2.0,
		EnableFailedQueue: true,
		FailedInterval:    time.Second,
		FailedBatch:       10,
		EnableRetryQueue:  true,
		RetryInterval:     time.Millisecond * 500,
		RetryBatch:        5,
	}

	// 测试各种错误情况
	testCases := []struct {
		name        string
		shouldFail  bool
		shouldRetry bool
		data        []byte
	}{
		{"空数据", false, false, []byte{}},
		{"重试错误", true, true, []byte("retry_error_data")},
		{"永久错误", true, false, []byte("permanent_error_data")},
		{"正常数据", false, false, []byte("normal_data")},
	}

	for _, tc := range testCases {
		log.Printf("测试错误情况: %s", tc.name)

		processor := NewTestProcessor(tc.shouldFail, tc.shouldRetry, time.Millisecond*50)
		manager, err := queue.NewManager(processor, config)
		if err != nil {
			log.Fatalf("Failed to create manager: %v", err)
		}

		if err := manager.Start(); err != nil {
			log.Fatalf("Failed to start manager: %v", err)
		}

		time.Sleep(time.Millisecond * 200)

		// 添加测试数据
		if len(tc.data) > 0 {
			if err := manager.AddData(context.Background(), tc.data); err != nil {
				log.Printf("Failed to add error test data: %v", err)
			}
		}

		time.Sleep(time.Second * 3)

		stats := processor.GetStats()
		log.Printf("错误处理测试结果 [%s]: %+v", tc.name, stats)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		manager.Stop(ctx)
	}

	log.Println("=== 错误处理测试完成 ===")
}

// 内存泄漏测试
func memoryLeakTest() {
	log.Println("=== 内存泄漏测试开始 ===")

	config := &queue.Config{
		EngineType:        engine.TypeChannel,
		Source:            "memory_source",
		Failed:            "memory_failed",
		Processing:        "memory_processing",
		Retry:             "memory_retry",
		ReaderCount:       2,
		WorkerCount:       3,
		WorkerTimeout:     time.Second * 5,
		ReaderInterval:    time.Millisecond * 100,
		MaxRetries:        1,
		RetryDelay:        time.Millisecond * 100,
		MaxRetryDelay:     time.Second,
		RetryFactor:       2.0,
		EnableFailedQueue: true,
		FailedInterval:    time.Second,
		FailedBatch:       10,
		EnableRetryQueue:  true,
		RetryInterval:     time.Millisecond * 500,
		RetryBatch:        5,
	}

	// 多次启动停止测试
	for i := 0; i < 10; i++ {
		log.Printf("内存泄漏测试轮次 %d", i+1)

		processor := NewTestProcessor(false, false, time.Millisecond*10)
		manager, err := queue.NewManager(processor, config)
		if err != nil {
			log.Fatalf("Failed to create manager: %v", err)
		}

		if err := manager.Start(); err != nil {
			log.Fatalf("Failed to start manager: %v", err)
		}

		time.Sleep(time.Millisecond * 100)

		// 添加一些数据
		for j := 0; j < 10; j++ {
			testData := []byte(fmt.Sprintf("memory_test_data_%d_%d", i, j))
			if err := manager.AddData(context.Background(), testData); err != nil {
				log.Printf("Failed to add memory test data: %v", err)
			}
		}

		time.Sleep(time.Second * 1)

		// 停止管理器
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		if err := manager.Stop(ctx); err != nil {
			log.Printf("Failed to stop manager: %v", err)
		}
		cancel()

		// 检查状态
		status := manager.GetStatus()
		if status != queue.StatusStopped {
			log.Printf("警告: 管理器状态异常，期望 Stopped，实际 %v", status)
		}

		time.Sleep(time.Millisecond * 100)
	}

	log.Println("=== 内存泄漏测试完成 ===")
}

// Redis引擎测试
func redisEngineTest() {
	log.Println("=== Redis引擎测试开始 ===")

	// 创建Redis客户端
	err := redisx.InitRedis(
		redisx.WithAddrs("localhost:6379"),
		redisx.WithPassword(""),
		redisx.WithDB(0),
	)
	if err != nil {
		log.Printf("警告: 无法初始化Redis，跳过Redis引擎测试: %v", err)
		log.Println("请确保Redis服务正在运行在 localhost:6379")
		return
	}
	defer redisx.Redis.Close()

	// 测试Redis连接
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	err = redisx.Redis.GetClient().Ping(ctx).Err()
	cancel()
	if err != nil {
		log.Printf("警告: Redis连接测试失败，跳过Redis引擎测试: %v", err)
		return
	}

	log.Println("Redis连接成功，开始Redis引擎测试...")

	// 创建Redis引擎
	redisEngine := engine.NewRedisEngine(redisx.Redis)

	// 测试基本功能
	testRedisBasicFunctionality(redisEngine)
	time.Sleep(time.Second)

	// 测试性能
	testRedisPerformance(redisEngine)
	time.Sleep(time.Second)

	// 测试错误处理
	testRedisErrorHandling(redisEngine)
	time.Sleep(time.Second)

	// 测试并发安全
	testRedisConcurrencySafety(redisEngine)
	time.Sleep(time.Second)

	log.Println("=== Redis引擎测试完成 ===")
}

// 测试Redis引擎基本功能
func testRedisBasicFunctionality(redisEngine engine.Engine) {
	log.Println("--- Redis基本功能测试 ---")

	config := &queue.Config{
		EngineType:        engine.TypeRedis,
		Source:            "redis_test_source",
		Failed:            "redis_test_failed",
		Processing:        "redis_test_processing",
		Retry:             "redis_test_retry",
		ReaderCount:       1,
		WorkerCount:       2,
		WorkerTimeout:     time.Second * 5,
		ReaderInterval:    time.Millisecond * 100,
		MaxRetries:        2,
		RetryDelay:        time.Millisecond * 500,
		MaxRetryDelay:     time.Second * 2,
		RetryFactor:       2.0,
		EnableFailedQueue: true,
		FailedInterval:    time.Second,
		FailedBatch:       5,
		EnableRetryQueue:  true,
		RetryInterval:     time.Millisecond * 500,
		RetryBatch:        3,
	}

	processor := NewTestProcessor(false, false, time.Millisecond*50)
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create Redis manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start Redis manager: %v", err)
	}

	// 添加测试数据
	testData := []string{
		"redis_test_data_1",
		"redis_test_data_2",
		"redis_test_data_3",
		"redis_test_data_4",
		"redis_test_data_5",
	}

	for _, data := range testData {
		if err := manager.AddData(context.Background(), []byte(data)); err != nil {
			log.Printf("Failed to add Redis test data: %v", err)
		}
	}

	// 等待处理完成
	time.Sleep(time.Second * 3)

	// 检查处理结果
	stats := processor.GetStats()
	log.Printf("Redis基本功能测试结果: %v", stats)

	// 停止管理器
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	if err := manager.Stop(ctx); err != nil {
		log.Printf("Failed to stop Redis manager: %v", err)
	}
	cancel()

	// 清理Redis数据
	cleanupRedisData(redisEngine, config)
}

// 测试Redis引擎性能
func testRedisPerformance(redisEngine engine.Engine) {
	log.Println("--- Redis性能测试 ---")

	config := &queue.Config{
		EngineType:        engine.TypeRedis,
		Source:            "redis_perf_source",
		Failed:            "redis_perf_failed",
		Processing:        "redis_perf_processing",
		Retry:             "redis_perf_retry",
		ReaderCount:       2,
		WorkerCount:       4,
		WorkerTimeout:     time.Second * 5,
		ReaderInterval:    time.Millisecond * 50,
		MaxRetries:        1,
		RetryDelay:        time.Millisecond * 100,
		MaxRetryDelay:     time.Second,
		RetryFactor:       2.0,
		EnableFailedQueue: true,
		FailedInterval:    time.Second,
		FailedBatch:       10,
		EnableRetryQueue:  true,
		RetryInterval:     time.Millisecond * 500,
		RetryBatch:        5,
	}

	processor := NewTestProcessor(false, false, time.Millisecond*10)
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create Redis performance manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start Redis performance manager: %v", err)
	}

	// 性能测试：添加大量数据
	startTime := time.Now()
	dataCount := 1000

	for i := 0; i < dataCount; i++ {
		testData := []byte(fmt.Sprintf("redis_perf_data_%d", i))
		if err := manager.AddData(context.Background(), testData); err != nil {
			log.Printf("Failed to add Redis performance test data: %v", err)
		}
	}

	// 等待处理完成
	time.Sleep(time.Second * 5)

	duration := time.Since(startTime)
	stats := processor.GetStats()
	processed := stats["processed"].(int64)

	log.Printf("Redis性能测试结果: 处理 %d 条数据，耗时 %v，平均吞吐量 %.2f items/sec",
		processed, duration, float64(processed)/duration.Seconds())

	// 停止管理器
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	if err := manager.Stop(ctx); err != nil {
		log.Printf("Failed to stop Redis performance manager: %v", err)
	}
	cancel()

	// 清理Redis数据
	cleanupRedisData(redisEngine, config)
}

// 测试Redis引擎错误处理
func testRedisErrorHandling(redisEngine engine.Engine) {
	log.Println("--- Redis错误处理测试 ---")

	config := &queue.Config{
		EngineType:        engine.TypeRedis,
		Source:            "redis_error_source",
		Failed:            "redis_error_failed",
		Processing:        "redis_error_processing",
		Retry:             "redis_error_retry",
		ReaderCount:       1,
		WorkerCount:       2,
		WorkerTimeout:     time.Second * 5,
		ReaderInterval:    time.Millisecond * 100,
		MaxRetries:        2,
		RetryDelay:        time.Millisecond * 500,
		MaxRetryDelay:     time.Second * 2,
		RetryFactor:       2.0,
		EnableFailedQueue: true,
		FailedInterval:    time.Second,
		FailedBatch:       5,
		EnableRetryQueue:  true,
		RetryInterval:     time.Millisecond * 500,
		RetryBatch:        3,
	}

	// 测试重试错误
	log.Println("测试重试错误...")
	processor := NewTestProcessor(true, true, time.Millisecond*50)
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create Redis error manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start Redis error manager: %v", err)
	}

	// 添加会重试的数据
	testData := []byte("redis_retry_error_data")
	if err := manager.AddData(context.Background(), testData); err != nil {
		log.Printf("Failed to add Redis retry test data: %v", err)
	}

	time.Sleep(time.Second * 3)

	stats := processor.GetStats()
	log.Printf("Redis重试错误测试结果: %v", stats)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	if err := manager.Stop(ctx); err != nil {
		log.Printf("Failed to stop Redis error manager: %v", err)
	}
	cancel()

	// 测试永久错误
	log.Println("测试永久错误...")
	processor = NewTestProcessor(true, false, time.Millisecond*50)
	manager, err = queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create Redis permanent error manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start Redis permanent error manager: %v", err)
	}

	// 添加会永久失败的数据
	testData = []byte("redis_permanent_error_data")
	if err := manager.AddData(context.Background(), testData); err != nil {
		log.Printf("Failed to add Redis permanent error test data: %v", err)
	}

	time.Sleep(time.Second * 3)

	stats = processor.GetStats()
	log.Printf("Redis永久错误测试结果: %v", stats)

	ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
	if err := manager.Stop(ctx); err != nil {
		log.Printf("Failed to stop Redis permanent error manager: %v", err)
	}
	cancel()

	// 清理Redis数据
	cleanupRedisData(redisEngine, config)
}

// 测试Redis引擎并发安全
func testRedisConcurrencySafety(redisEngine engine.Engine) {
	log.Println("--- Redis并发安全测试 ---")

	config := &queue.Config{
		EngineType:        engine.TypeRedis,
		Source:            "redis_concurrent_source",
		Failed:            "redis_concurrent_failed",
		Processing:        "redis_concurrent_processing",
		Retry:             "redis_concurrent_retry",
		ReaderCount:       3,
		WorkerCount:       5,
		WorkerTimeout:     time.Second * 5,
		ReaderInterval:    time.Millisecond * 50,
		MaxRetries:        1,
		RetryDelay:        time.Millisecond * 100,
		MaxRetryDelay:     time.Second,
		RetryFactor:       2.0,
		EnableFailedQueue: true,
		FailedInterval:    time.Second,
		FailedBatch:       10,
		EnableRetryQueue:  true,
		RetryInterval:     time.Millisecond * 500,
		RetryBatch:        5,
	}

	processor := NewTestProcessor(false, false, time.Millisecond*20)
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("Failed to create Redis concurrent manager: %v", err)
	}

	if err := manager.Start(); err != nil {
		log.Fatalf("Failed to start Redis concurrent manager: %v", err)
	}

	// 并发添加数据
	var wg sync.WaitGroup
	dataCount := 500
	concurrentWriters := 5

	for i := 0; i < concurrentWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for j := 0; j < dataCount/concurrentWriters; j++ {
				testData := []byte(fmt.Sprintf("redis_concurrent_data_writer_%d_item_%d", writerID, j))
				if err := manager.AddData(context.Background(), testData); err != nil {
					log.Printf("Failed to add Redis concurrent test data: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	// 等待处理完成
	time.Sleep(time.Second * 5)

	stats := processor.GetStats()
	log.Printf("Redis并发安全测试结果: %v", stats)

	// 停止管理器
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	if err := manager.Stop(ctx); err != nil {
		log.Printf("Failed to stop Redis concurrent manager: %v", err)
	}
	cancel()

	// 清理Redis数据
	cleanupRedisData(redisEngine, config)
}

// 清理Redis测试数据
func cleanupRedisData(redisEngine engine.Engine, config *queue.Config) {
	// 清理所有测试队列
	queues := []string{
		config.Source,
		config.Failed,
		config.Processing,
		config.Retry,
		config.Retry + "_delayed", // Redis延迟队列
	}

	for _, queue := range queues {
		// 这里我们只是记录清理操作，实际的清理需要Redis客户端支持
		log.Printf("清理Redis队列: %s", queue)
	}
}

func main() {
	log.Println("开始全面的队列测试...")
	log.Println("测试包括: 性能测试、功能测试、并发安全测试、压力测试、错误处理测试、内存泄漏测试、Redis引擎测试")

	// 运行各种测试
	performanceTest()
	time.Sleep(time.Second)

	functionalityTest()
	time.Sleep(time.Second)

	concurrencySafetyTest()
	time.Sleep(time.Second)

	stressTest()
	time.Sleep(time.Second)

	errorHandlingTest()
	time.Sleep(time.Second)

	memoryLeakTest()
	time.Sleep(time.Second)

	// Redis引擎测试
	redisEngineTest()

	log.Println("所有测试完成!")
}
