package queue

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
)

// PushToFailedQueueNonBlocking 将数据推送到失败队列
func PushToFailedQueueNonBlocking(ctx context.Context, engine engine.Engine, config *Config, data []byte) error {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	if !config.EnableFailedQueue {
		log.Printf("PushToFailedQueueNonBlocking(): [Info] 失败队列未启用, 将数据(%s)丢弃。", string(data))
		return nil
	}

	// 使用 channel 和 time.After 来处理超时，而不是 context
	done := make(chan error, 1)
	go func() {
		err := engine.PushNonBlocking(ctx, config.Failed, data)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			return common.ErrPushToFailedQueue
		}
		return nil
	case <-time.After(config.Timeout):
		return common.ErrPushToFailedQueueTimeout
	}
}

// PushToProcessingQueueNonBlocking 将数据推送到处理中队列
func PushToProcessingQueueNonBlocking(ctx context.Context, engine engine.Engine, config *Config, data []byte) error {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	// 使用 channel 和 time.After 来处理超时，而不是 context
	done := make(chan error, 1)
	go func() {
		err := engine.PushNonBlocking(ctx, config.Processing, data)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			return common.ErrPushToProcessingQueue
		}
		return nil
	case <-time.After(config.Timeout):
		return common.ErrPushToProcessingQueueTimeout
	}
}

// HandleRetry 处理重试逻辑
func HandleRetry(ctx context.Context, engine engine.Engine, config *Config, item *DataItem, originalData []byte) error {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	if !config.EnableRetryQueue {
		log.Printf("HandleRetry(): [Info] 重试队列未启用, 将数据(%s)丢弃。", string(originalData))
		return nil
	}

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
		log.Printf("HandleRetry(): [Error] 转换为JSON错误(%v), 将数据(%s)移动到失败队列。", err, string(originalData))
		return PushToFailedQueueNonBlocking(ctx, engine, config, originalData)
	}

	// 放入延迟重试队列
	err = pushToRetryQueueNonBlocking(ctx, engine, config, itemData, delay)
	if err != nil {
		log.Printf("HandleRetry(): [Error] 放入延迟重试队列错误(%v), 将数据(%s)移动到失败队列。", err, string(originalData))
		return PushToFailedQueueNonBlocking(ctx, engine, config, originalData)
	}
	log.Printf("HandleRetry(): [Info] 成功处理重试(item: %s), 下次重试时间: %v, 重试次数: %d", item.ID, item.NextRetryTime, item.RetryCount)
	return nil
}

// pushToRetryQueueNonBlocking 将数据推送到重试队列
func pushToRetryQueueNonBlocking(ctx context.Context, engine engine.Engine, config *Config, data []byte, delay time.Duration) error {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	// 使用 channel 和 time.After 来处理超时，而不是 context
	done := make(chan error, 1)
	go func() {
		err := engine.PushDelayed(ctx, config.Retry, data, delay)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			return common.ErrPushToRetryQueue
		}
		return nil
	case <-time.After(config.Timeout):
		return common.ErrPushToRetryQueueTimeout
	}
}

// HandleFailedProcessorBatch 处理失败队列逻辑
func HandleFailedProcessorBatch(ctx context.Context, engine engine.Engine, processor Processor, config *Config) error {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	// 1. 批量从失败队列读取数据
	items, err := engine.BatchPopNonBlocking(ctx, config.Failed, config.FailedBatch)
	if err != nil {
		return err
	}

	if len(items) == 0 {
		return nil
	}

	// 2. 循环或者开协程处理数据
	for _, data := range items {
		// 解析失败队列数据
		item, err := FromJSON(data)
		if err != nil {
			log.Printf("HandleFailed(): [Error] 解析失败队列数据错误(%v), 将数据(%s)丢弃。", err, string(data))
			continue
		}

		err = processor.Process(ctx, item.Data)
		if err != nil {
			// 错误队列的处理不需要重试，直接丢弃
			log.Printf("HandleFailed(): [Error] 处理失败队列数据错误(%v), 将数据(%s)丢弃。", err, string(data))
			continue
		}
	}
	return nil
}

// HandleRetryProcessorBatch 处理重试队列逻辑
func HandleRetryProcessorBatch(ctx context.Context, engine engine.Engine, processor Processor, config *Config) error {
	defer func() {
		if r := recover(); r != nil {
			panicError := common.NewPanicError(r, string(debug.Stack()))
			fmt.Println(panicError.FormatAsPretty())
		}
	}()

	// 1. 批量从重试队列读取数据
	items, err := engine.PopDelayed(ctx, config.Retry, config.RetryBatch)
	if err != nil {
		return err
	}

	if len(items) == 0 {
		return nil
	}

	// 2. 循环或者开协程处理数据
	for _, data := range items {
		// 解析重试队列数据
		item, err := FromJSON(data)
		if err != nil {
			log.Printf("HandleRetryProcessorBatch(): [Error] 解析重试队列数据错误(%v), 将数据(%s)丢弃。", err, string(data))
			continue
		}

		// 处理数据
		err = processor.Process(ctx, item.Data)
		if err != nil {
			if common.IsRetryableError(err) && item.ShouldRetry(config) {
				log.Printf("HandleRetryProcessorBatch(): [Error] 处理重试队列数据错误(%v), 将数据(%s)重试。", err, string(data))
				return HandleRetry(ctx, engine, config, item, data)
			}
			log.Printf("HandleRetryProcessorBatch(): [Error] 处理重试队列数据错误(%v), 将数据(%s)丢弃。", err, string(data))
			continue
		}
	}

	return nil
}
