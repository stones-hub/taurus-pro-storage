package main

// 注意：这个文件需要单独运行，不能与其他main函数一起编译
// 运行方式：go run advanced_example.go

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
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// 订单数据结构
type OrderData struct {
	OrderID    string    `json:"order_id"`
	UserID     int       `json:"user_id"`
	ProductID  int       `json:"product_id"`
	Quantity   int       `json:"quantity"`
	Amount     float64   `json:"amount"`
	Status     string    `json:"status"`   // pending, processing, completed, failed
	Priority   int       `json:"priority"` // 1-高，2-中，3-低
	CreatedAt  time.Time `json:"created_at"`
	RetryCount int       `json:"retry_count"`
}

// 高级订单处理器
type AdvancedOrderProcessor struct {
	processedCount   int
	failedCount      int
	retryCount       int
	completedOrders  int
	processingOrders int
}

// Process 实现 Processor 接口
func (p *AdvancedOrderProcessor) Process(ctx context.Context, data []byte) error {
	// 解析订单数据
	var orderData OrderData
	if err := json.Unmarshal(data, &orderData); err != nil {
		log.Printf("AdvancedOrderProcessor.Process(): [Error] 解析订单数据失败: %v", err)
		return common.NewErrorableError(fmt.Errorf("订单数据格式错误: %v", err))
	}

	log.Printf("AdvancedOrderProcessor.Process(): [Info] 开始处理订单: ID=%s, UserID=%d, Amount=%.2f, Status=%s",
		orderData.OrderID, orderData.UserID, orderData.Amount, orderData.Status)

	// 根据订单状态处理
	switch orderData.Status {
	case "pending":
		return p.handlePendingOrder(ctx, orderData)
	case "processing":
		return p.handleProcessingOrder(ctx, orderData)
	case "completed":
		return p.handleCompletedOrder(ctx, orderData)
	case "failed":
		return p.handleFailedOrder(ctx, orderData)
	default:
		return common.NewErrorableError(fmt.Errorf("不支持的订单状态: %s", orderData.Status))
	}
}

// handlePendingOrder 处理待处理订单
func (p *AdvancedOrderProcessor) handlePendingOrder(ctx context.Context, orderData OrderData) error {
	// 模拟库存检查
	time.Sleep(200 * time.Millisecond)

	// 模拟随机失败（15%概率）
	if time.Now().UnixNano()%7 == 0 {
		log.Printf("AdvancedOrderProcessor.handlePendingOrder(): [Error] 库存检查失败: %s", orderData.OrderID)
		return common.NewRetryableError(fmt.Errorf("库存服务暂时不可用"))
	}

	// 模拟支付处理
	time.Sleep(300 * time.Millisecond)

	// 模拟支付失败（10%概率）
	if time.Now().UnixNano()%10 == 0 {
		log.Printf("AdvancedOrderProcessor.handlePendingOrder(): [Error] 支付处理失败: %s", orderData.OrderID)
		return common.NewRetryableError(fmt.Errorf("支付网关超时"))
	}

	p.processingOrders++
	log.Printf("AdvancedOrderProcessor.handlePendingOrder(): [Success] 订单开始处理: %s", orderData.OrderID)
	return nil
}

// handleProcessingOrder 处理处理中订单
func (p *AdvancedOrderProcessor) handleProcessingOrder(ctx context.Context, orderData OrderData) error {
	// 模拟发货处理
	time.Sleep(500 * time.Millisecond)

	// 模拟发货失败（5%概率）
	if time.Now().UnixNano()%20 == 0 {
		log.Printf("AdvancedOrderProcessor.handleProcessingOrder(): [Error] 发货失败: %s", orderData.OrderID)
		return common.NewRetryableError(fmt.Errorf("物流服务异常"))
	}

	p.completedOrders++
	p.processingOrders--
	log.Printf("AdvancedOrderProcessor.handleProcessingOrder(): [Success] 订单处理完成: %s", orderData.OrderID)
	return nil
}

// handleCompletedOrder 处理已完成订单
func (p *AdvancedOrderProcessor) handleCompletedOrder(ctx context.Context, orderData OrderData) error {
	// 模拟发送通知
	time.Sleep(100 * time.Millisecond)

	// 模拟通知失败（3%概率）
	if time.Now().UnixNano()%33 == 0 {
		log.Printf("AdvancedOrderProcessor.handleCompletedOrder(): [Error] 发送通知失败: %s", orderData.OrderID)
		return common.NewRetryableError(fmt.Errorf("通知服务不可用"))
	}

	log.Printf("AdvancedOrderProcessor.handleCompletedOrder(): [Success] 订单通知发送成功: %s", orderData.OrderID)
	return nil
}

// handleFailedOrder 处理失败订单
func (p *AdvancedOrderProcessor) handleFailedOrder(ctx context.Context, orderData OrderData) error {
	// 模拟退款处理
	time.Sleep(300 * time.Millisecond)

	// 模拟退款失败（2%概率）
	if time.Now().UnixNano()%50 == 0 {
		log.Printf("AdvancedOrderProcessor.handleFailedOrder(): [Error] 退款处理失败: %s", orderData.OrderID)
		return common.NewErrorableError(fmt.Errorf("退款服务异常"))
	}

	p.failedCount++
	log.Printf("AdvancedOrderProcessor.handleFailedOrder(): [Success] 订单退款成功: %s", orderData.OrderID)
	return nil
}

// GetStats 获取处理统计信息
func (p *AdvancedOrderProcessor) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"processed_count":   p.processedCount,
		"failed_count":      p.failedCount,
		"retry_count":       p.retryCount,
		"completed_orders":  p.completedOrders,
		"processing_orders": p.processingOrders,
	}
}

func main() {
	log.Println("=== 高级异步队列使用示例 ===")

	// 1. 初始化Redis客户端
	log.Println("正在初始化Redis连接...")
	err := redisx.InitRedis(
		redisx.WithAddrs("localhost:6379"), // Redis地址
		redisx.WithPassword(""),            // Redis密码（如果有）
		redisx.WithDB(0),                   // 数据库编号
		redisx.WithPoolSize(20),            // 连接池大小
		redisx.WithTimeout(5*time.Second, 3*time.Second, 3*time.Second), // 超时配置
	)
	if err != nil {
		log.Fatalf("初始化Redis失败: %v", err)
	}
	log.Println("Redis连接初始化成功！")

	// 2. 创建高级业务处理器
	processor := &AdvancedOrderProcessor{}

	// 3. 创建高级队列配置
	config := &queue.Config{
		// 引擎类型：使用Redis引擎（生产环境推荐）
		EngineType: engine.REDIS,

		// 队列配置
		QueueSize:  5000,                     // 更大的队列容量
		Source:     "order_source_queue",     // 源队列名称
		Failed:     "order_failed_queue",     // 失败队列名称
		Processing: "order_processing_queue", // 处理中队列名称
		Retry:      "order_retry_queue",      // 重试队列名称

		// 并发配置（高并发场景）
		ReaderCount: 3,  // 更多读取协程
		WorkerCount: 12, // 更多工作协程

		// 重试配置（更严格的重试策略）
		MaxRetries:    7,               // 更多重试次数
		RetryDelay:    time.Second * 3, // 更长的初始重试延迟
		MaxRetryDelay: time.Minute * 5, // 更长的最大重试延迟
		RetryFactor:   1.8,             // 更温和的重试倍数

		// 失败队列配置
		EnableFailedQueue: true,            // 启用失败队列处理
		FailedInterval:    time.Minute * 3, // 更长的失败队列处理间隔
		FailedBatch:       100,             // 更大的批处理大小

		// 重试队列配置
		EnableRetryQueue: true,             // 启用重试队列处理
		RetryInterval:    time.Second * 20, // 更长的重试队列检查间隔
		RetryBatch:       50,               // 重试批处理大小

		// 数据落盘配置
		EnableFailedDataToFile: true,                      // 启用数据落盘
		FailedDataFilePath:     "./logs/order_failed.log", // 订单失败数据落盘路径

		// 超时配置
		Timeout: time.Minute * 5, // 更长的操作超时时间
	}

	// 4. 创建队列管理器
	manager, err := queue.NewManager(processor, config)
	if err != nil {
		log.Fatalf("创建队列管理器失败: %v", err)
	}

	// 5. 启动队列管理器
	if err := manager.Start(); err != nil {
		log.Fatalf("启动队列管理器失败: %v", err)
	}

	log.Println("高级队列管理器启动成功！")

	// 6. 创建测试订单数据
	testOrders := []OrderData{
		{
			OrderID:   "ORD-001",
			UserID:    1001,
			ProductID: 2001,
			Quantity:  2,
			Amount:    299.99,
			Status:    "pending",
			Priority:  1,
			CreatedAt: time.Now(),
		},
		{
			OrderID:   "ORD-002",
			UserID:    1002,
			ProductID: 2002,
			Quantity:  1,
			Amount:    199.99,
			Status:    "processing",
			Priority:  2,
			CreatedAt: time.Now(),
		},
		{
			OrderID:   "ORD-003",
			UserID:    1003,
			ProductID: 2003,
			Quantity:  3,
			Amount:    599.97,
			Status:    "completed",
			Priority:  1,
			CreatedAt: time.Now(),
		},
		{
			OrderID:   "ORD-004",
			UserID:    1004,
			ProductID: 2004,
			Quantity:  1,
			Amount:    99.99,
			Status:    "failed",
			Priority:  3,
			CreatedAt: time.Now(),
		},
	}

	// 7. 发送测试订单到队列
	ctx := context.Background()
	for i, orderData := range testOrders {
		// 将订单数据序列化为JSON
		data, err := json.Marshal(orderData)
		if err != nil {
			log.Printf("序列化订单数据失败: %v", err)
			continue
		}

		// 发送到队列
		if err := manager.AddData(ctx, data); err != nil {
			log.Printf("发送订单到队列失败: %v", err)
			continue
		}

		log.Printf("已发送订单 %d: %s (%.2f元)", i+1, orderData.OrderID, orderData.Amount)
		time.Sleep(200 * time.Millisecond) // 模拟订单发送间隔
	}

	// 8. 持续发送订单（模拟生产环境）
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()

		counter := 5
		statuses := []string{"pending", "processing", "completed", "failed"}
		priorities := []int{1, 2, 3}

		for range ticker.C {
			orderData := OrderData{
				OrderID:   fmt.Sprintf("ORD-%03d", counter),
				UserID:    1000 + counter,
				ProductID: 2000 + counter,
				Quantity:  (counter % 5) + 1,
				Amount:    float64((counter%10)+1) * 99.99,
				Status:    statuses[counter%len(statuses)],
				Priority:  priorities[counter%len(priorities)],
				CreatedAt: time.Now(),
			}

			data, err := json.Marshal(orderData)
			if err != nil {
				log.Printf("序列化订单数据失败: %v", err)
				continue
			}

			if err := manager.AddData(ctx, data); err != nil {
				log.Printf("发送订单到队列失败: %v", err)
				continue
			}

			log.Printf("持续发送订单: %s (%.2f元, %s)", orderData.OrderID, orderData.Amount, orderData.Status)
			counter++
		}
	}()

	// 9. 定期打印详细统计信息
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			stats := manager.GetStats()
			processorStats := processor.GetStats()

			log.Printf("=== 高级队列统计信息 ===")
			log.Printf("队列状态: %v", stats["status"])
			log.Printf("Reader数量: %v", stats["readers"])
			log.Printf("Worker数量: %v", stats["workers"])
			log.Printf("已处理订单: %v", processorStats["processed_count"])
			log.Printf("失败订单: %v", processorStats["failed_count"])
			log.Printf("重试订单: %v", processorStats["retry_count"])
			log.Printf("完成订单: %v", processorStats["completed_orders"])
			log.Printf("处理中订单: %v", processorStats["processing_orders"])
			log.Printf("========================")
		}
	}()

	// 10. 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Println("高级队列正在运行中... 按 Ctrl+C 停止")
	<-sigChan

	log.Println("收到停止信号，正在优雅关闭高级队列...")

	// 11. 优雅关闭队列
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := manager.Stop(shutdownCtx); err != nil {
		log.Printf("停止队列管理器失败: %v", err)
	} else {
		log.Println("高级队列管理器已成功停止")
	}

	// 12. 关闭Redis连接
	if redisx.Redis != nil {
		if err := redisx.Redis.Close(); err != nil {
			log.Printf("关闭Redis连接失败: %v", err)
		} else {
			log.Println("Redis连接已关闭")
		}
	}

	// 13. 打印最终统计信息
	finalStats := processor.GetStats()
	log.Printf("=== 最终高级统计信息 ===")
	log.Printf("总处理订单: %v", finalStats["processed_count"])
	log.Printf("总失败订单: %v", finalStats["failed_count"])
	log.Printf("总重试订单: %v", finalStats["retry_count"])
	log.Printf("总完成订单: %v", finalStats["completed_orders"])
	log.Printf("处理中订单: %v", finalStats["processing_orders"])
	log.Printf("========================")
}
