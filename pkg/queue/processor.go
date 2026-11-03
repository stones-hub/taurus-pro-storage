package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
)

// DataItem 表示队列中的数据项
type DataItem struct {
	// 数据ID
	ID string `json:"id"`
	// 数据内容
	Data []byte `json:"data"`
	// 重试次数
	RetryCount int `json:"retry_count"`
	// 最后处理时间
	LastProcessTime time.Time `json:"last_process_time"`
	// 下次重试时间
	NextRetryTime time.Time `json:"next_retry_time"`
}

// GenerateID 生成唯一的ID
func GenerateID() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// Validate 验证数据项
func (item *DataItem) Validate() error {
	// 如果ID为空，自动生成一个
	if item.ID == "" {
		item.ID = GenerateID()
	}
	if len(item.Data) == 0 {
		return common.ErrItemEmpty
	}
	if item.RetryCount < 0 {
		return common.ErrItemRetryCountNegative
	}
	return nil
}

// ToJSON 将 DataItem 转换为 JSON
func (item *DataItem) ToJSON() ([]byte, error) {
	if err := item.Validate(); err != nil {
		return nil, common.ErrItemValidateFailed
	}
	return json.Marshal(item)
}

// ShouldRetry 判断是否应该重试
func (item *DataItem) ShouldRetry(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	return item.RetryCount < cfg.MaxRetries
}

// CalculateNextRetryTime 计算下次重试时间
func (item *DataItem) CalculateNextRetryTime(cfg *Config) time.Time {
	if cfg == nil {
		return time.Now().Add(time.Second)
	}

	delay := cfg.RetryDelay
	for i := 0; i < item.RetryCount+1; i++ {
		delay = time.Duration(float64(delay) * cfg.RetryFactor)
		if delay > cfg.MaxRetryDelay {
			delay = cfg.MaxRetryDelay
			break
		}
	}
	return time.Now().Add(delay)
}

// FromJSON 从 JSON 解析 DataItem
func FromJSON(data []byte) (*DataItem, error) {
	if len(data) == 0 {
		return nil, common.ErrItemEmpty
	}

	var item DataItem
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, common.ErrItemJsonUnmarshalFailed
	}

	if err := item.Validate(); err != nil {
		return nil, common.ErrItemValidateFailed
	}

	return &item, nil
}

// Processor 定义数据处理器接口
type Processor interface {
	// Process 处理数据。投递语义是至少一次，同一任务可能被重复投递（崩溃恢复、租约被接管等），
	// 实现必须幂等。
	//
	//   - 返回 nil：任务成功，被确认并删除。
	//   - 返回 common.RetryableError（可被 errors.As 识别）：在 MaxRetries 内按退避延迟重试，
	//     耗尽后进入失败留存。
	//   - panic：被捕获并按可重试错误处理，worker 继续服务。
	//   - 返回其它错误：直接进入失败留存（不会被自动再次处理，可用 Manager.ListFailed/ReplayFailed 查询重放）。
	//
	// ctx 在 Manager 停止排空超时、或租约丢失（被其它实例接管）时被取消，长任务应响应取消；
	// 不响应取消的 Processor 无法被强制终止，可能与接管它的实例重叠执行。
	Process(ctx context.Context, data []byte) error
}
