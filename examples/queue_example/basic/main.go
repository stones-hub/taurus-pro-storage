package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// 业务数据结构
type UserData struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Action   string `json:"action"`   // 操作类型：create, update, delete
	Priority int    `json:"priority"` // 优先级：1-高，2-中，3-低
}

// 业务处理器实现
type UserProcessor struct {
	processedCount int
	failedCount    int
	retryCount     int
}

// Process 实现 Processor 接口
func (p *UserProcessor) Process(ctx context.Context, data []byte) error {
	// 解析业务数据
	var userData UserData
	if err := json.Unmarshal(data, &userData); err != nil {
		log.Printf("UserProcessor.Process(): [Error] 解析用户数据失败: %v", err)
		return common.NewErrorableError(fmt.Errorf("数据格式错误: %v", err))
	}

	log.Printf("UserProcessor.Process(): [Info] 开始处理用户数据: ID=%d, Name=%s, Action=%s",
		userData.ID, userData.Name, userData.Action)

	// 模拟业务处理逻辑
	switch userData.Action {
	case "create":
		return p.handleUserCreate(ctx, userData)
	case "update":
		return p.handleUserUpdate(ctx, userData)
	case "delete":
		return p.handleUserDelete(ctx, userData)
	default:
		return common.NewErrorableError(fmt.Errorf("不支持的操作类型: %s", userData.Action))
	}
}

// handleUserCreate 处理用户创建
func (p *UserProcessor) handleUserCreate(ctx context.Context, userData UserData) error {
	// 模拟网络延迟
	time.Sleep(100 * time.Millisecond)

	// 模拟随机失败（20%概率）
	if time.Now().UnixNano()%5 == 0 {
		log.Printf("UserProcessor.handleUserCreate(): [Error] 创建用户失败: %s", userData.Name)
		return common.NewRetryableError(fmt.Errorf("数据库连接超时"))
	}

	p.processedCount++
	log.Printf("UserProcessor.handleUserCreate(): [Success] 用户创建成功: %s", userData.Name)
	return nil
}

// handleUserUpdate 处理用户更新
func (p *UserProcessor) handleUserUpdate(ctx context.Context, userData UserData) error {
	// 模拟网络延迟
	time.Sleep(150 * time.Millisecond)

	// 模拟随机失败（10%概率）
	if time.Now().UnixNano()%10 == 0 {
		log.Printf("UserProcessor.handleUserUpdate(): [Error] 更新用户失败: %s", userData.Name)
		return common.NewRetryableError(fmt.Errorf("网络超时"))
	}

	p.processedCount++
	log.Printf("UserProcessor.handleUserUpdate(): [Success] 用户更新成功: %s", userData.Name)
	return nil
}

// handleUserDelete 处理用户删除
func (p *UserProcessor) handleUserDelete(ctx context.Context, userData UserData) error {
	// 模拟网络延迟
	time.Sleep(80 * time.Millisecond)

	// 模拟随机失败（5%概率）
	if time.Now().UnixNano()%20 == 0 {
		log.Printf("UserProcessor.handleUserDelete(): [Error] 删除用户失败: %s", userData.Name)
		return common.NewErrorableError(fmt.Errorf("用户不存在"))
	}

	p.processedCount++
	log.Printf("UserProcessor.handleUserDelete(): [Success] 用户删除成功: %s", userData.Name)
	return nil
}

// GetStats 获取处理统计信息
func (p *UserProcessor) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"processed_count": p.processedCount,
		"failed_count":    p.failedCount,
		"retry_count":     p.retryCount,
	}
}

func main() {
	log.Println("=== 异步队列使用示例 ===")

	// 1. 创建业务处理器
	processor := &UserProcessor{}

	// 2. 创建队列配置
	config := &queue.Config{
		// 引擎类型：CHANNEL（内存队列）或 REDIS（Redis队列）
		EngineType: engine.CHANNEL,

		// 队列配置
		QueueSize:  2000,                    // 队列大小
		Source:     "user_source_queue",     // 源队列名称
		Failed:     "user_failed_queue",     // 失败队列名称
		Processing: "user_processing_queue", // 处理中队列名称
		Retry:      "user_retry_queue",      // 重试队列名称

		// 并发配置
		ReaderCount: 2, // 读取协程数量
		WorkerCount: 8, // 工作协程数量

		// 重试配置
		MaxRetries:    5,               // 最大重试次数
		RetryDelay:    time.Second * 2, // 初始重试延迟
		MaxRetryDelay: time.Minute * 2, // 最大重试延迟
		RetryFactor:   2.0,             // 重试延迟倍数（指数退避）

		// 失败队列配置
		EnableFailedQueue: true,            // 启用失败队列处理
		FailedInterval:    time.Minute * 2, // 失败队列处理间隔
		FailedBatch:       50,              // 每次处理的最大数量

		// 重试队列配置
		EnableRetryQueue: true,             // 启用重试队列处理
		RetryInterval:    time.Second * 15, // 重试队列检查间隔
		RetryBatch:       30,               // 每次处理的重试数据数量

		// 数据落盘配置
		EnableFailedDataToFile: true,                     // 启用数据落盘
		FailedDataFilePath:     "./logs/failed_data.log", // 数据落盘路径

		// 超时配置
		Timeout: time.Minute * 3, // 操作超时时间
	}

	// 3. 创建队列管理器
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("创建队列管理器失败: %v", err)
	}

	// 4. 启动队列管理器
	if err := manager.Start(); err != nil {
		log.Fatalf("启动队列管理器失败: %v", err)
	}

	log.Println("队列管理器启动成功！")

	// 5. 创建测试数据
	testData := []UserData{
		{ID: 1, Name: "张三", Email: "zhangsan@example.com", Action: "create", Priority: 1},
		{ID: 2, Name: "李四", Email: "lisi@example.com", Action: "update", Priority: 2},
		{ID: 3, Name: "王五", Email: "wangwu@example.com", Action: "delete", Priority: 1},
		{ID: 4, Name: "赵六", Email: "zhaoliu@example.com", Action: "create", Priority: 3},
		{ID: 5, Name: "钱七", Email: "qianqi@example.com", Action: "update", Priority: 2},
	}

	// 6. 发送测试数据到队列
	ctx := context.Background()
	for i, userData := range testData {
		// 将业务数据序列化为JSON
		data, err := json.Marshal(userData)
		if err != nil {
			log.Printf("序列化用户数据失败: %v", err)
			continue
		}

		// 发送到队列
		if err := manager.AddData(ctx, data); err != nil {
			log.Printf("发送数据到队列失败: %v", err)
			continue
		}

		log.Printf("已发送用户数据 %d: %s", i+1, userData.Name)
		time.Sleep(100 * time.Millisecond) // 模拟数据发送间隔
	}

	// 7. 持续发送数据（模拟生产环境）
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		counter := 6
		for range ticker.C {
			userData := UserData{
				ID:       counter,
				Name:     fmt.Sprintf("用户%d", counter),
				Email:    fmt.Sprintf("user%d@example.com", counter),
				Action:   []string{"create", "update", "delete"}[counter%3],
				Priority: (counter % 3) + 1,
			}

			data, err := json.Marshal(userData)
			if err != nil {
				log.Printf("序列化用户数据失败: %v", err)
				continue
			}

			if err := manager.AddData(ctx, data); err != nil {
				log.Printf("发送数据到队列失败: %v", err)
				continue
			}

			log.Printf("持续发送用户数据: %s", userData.Name)
			counter++
		}
	}()

	// 8. 定期打印统计信息
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			stats := manager.GetStats()
			processorStats := processor.GetStats()

			log.Printf("=== 队列统计信息 ===")
			log.Printf("队列状态: %v", stats["status"])
			log.Printf("Reader数量: %v", stats["readers"])
			log.Printf("Worker数量: %v", stats["workers"])
			log.Printf("已处理数据: %v", processorStats["processed_count"])
			log.Printf("失败数据: %v", processorStats["failed_count"])
			log.Printf("重试数据: %v", processorStats["retry_count"])
			log.Printf("==================")
		}
	}()

	// 9. 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Println("队列正在运行中... 按 Ctrl+C 停止")
	<-sigChan

	log.Println("收到停止信号，正在优雅关闭队列...")

	// 10. 优雅关闭队列
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := manager.Stop(shutdownCtx); err != nil {
		log.Printf("停止队列管理器失败: %v", err)
	} else {
		log.Println("队列管理器已成功停止")
	}

	// 11. 打印最终统计信息
	finalStats := processor.GetStats()
	log.Printf("=== 最终统计信息 ===")
	log.Printf("总处理数据: %v", finalStats["processed_count"])
	log.Printf("总失败数据: %v", finalStats["failed_count"])
	log.Printf("总重试数据: %v", finalStats["retry_count"])
	log.Printf("==================")
}
