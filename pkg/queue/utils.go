package queue

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// handleRetry 处理重试逻辑（共享函数）
// 将数据移到延迟队列或者失败队列
func handleRetry(engine engine.Engine, config *Config, item *DataItem, originalData []byte) error {
	// 更新重试信息
	item.RetryCount++
	item.LastProcessTime = time.Now()
	item.NextRetryTime = item.CalculateNextRetryTime(config)

	// 计算当前时间到下次重试时间的时间差
	delay := time.Until(item.NextRetryTime)
	if delay < 0 {
		delay = 0
	}

	// 转换为JSON
	itemData, err := item.ToJSON()
	if err != nil {
		log.Printf("Error marshaling retry item: %v", err)
		return moveToFailedQueue(engine, config, originalData)
	}

	// 创建上下文用于推送数据
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// 放入延迟重试队列
	err = engine.PushDelayed(ctx, config.Retry, itemData, delay)
	if err != nil {
		log.Printf("Error pushing to retry queue: %v", err)
		return moveToFailedQueue(engine, config, originalData)
	}

	log.Printf("Scheduled retry %d for item %s at %v (delay: %v)", item.RetryCount, item.ID, item.NextRetryTime, delay)
	return nil
}

// moveToFailedQueue 将数据移动到失败队列（共享函数）
func moveToFailedQueue(engine engine.Engine, config *Config, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	err := engine.Push(ctx, config.Failed, data)
	if err != nil {
		log.Printf("Error moving to failed queue: %v", err)
		return fmt.Errorf("move to failed queue: %w", err)
	}
	return nil
}
