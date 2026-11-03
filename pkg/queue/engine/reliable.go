package engine

import (
	"context"
	"regexp"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
)

// Delivery 表示一次领取。每次领取（包括租约到期后的重新领取）都有独立的 Token，
// Ack/Renew/Retry/Fail 必须出示当前 Token，被接管后旧 Token 不能改变新的领取。
type Delivery struct {
	// ID 任务ID（AddDataWithID 指定或自动生成）
	ID string
	// Data 任务内容
	Data []byte
	// Token 本次领取的凭证
	Token string
	// Attempt 累计投递次数（含本次，从 1 开始）；大于 1 说明曾经被领取过但未确认
	Attempt int
	// Retries 已经历的重试（延迟重投）次数，用于重试预算
	Retries int
	// Recovered 为 true 表示本次领取接管了一个租约已到期的任务
	Recovered bool
}

// FailedMessage 表示失败留存中的任务
type FailedMessage struct {
	ID       string
	Data     []byte
	Attempts int // 累计投递次数
	Reason   string
	FailedAt time.Time
}

// ReliableOptions 可靠引擎参数
type ReliableOptions struct {
	// Name 队列名称，同名队列共享同一份数据
	Name string
	// LeaseDuration 领取租约时长，到期未续期/未确认的任务可被重新领取
	LeaseDuration time.Duration
	// DedupWindow 稳定ID任务完成后的去重保留时长；未完成的任务不受此窗口限制
	DedupWindow time.Duration
	// FailedRetention 失败留存保留时长，0 表示不自动清理
	FailedRetention time.Duration
	// Capacity 仅内存引擎使用：未清理任务总数上限；0 表示不限制
	Capacity int
}

// ReliableEngine 至少一次投递引擎。与旧的 Engine（破坏性 Pop）不同，领取后的任务
// 在被 Ack/Retry/Fail 之前一直保留，租约到期后可被其它领取者恢复。
//
// 约定：
//   - Receive 非阻塞，无任务时返回 common.ErrQueueEmpty。到期的延迟重试会先被转回就绪队列，
//     租约到期的任务会先于新任务被重新领取（恢复），因此没有单独的恢复入口。
//   - Ack 对已不存在的任务幂等返回 nil；Renew/Retry/Fail 凭证失效时返回 common.ErrLeaseLost。
//   - 同一ID内容相同的 Enqueue 视为重复（duplicate=true），内容不同返回 common.ErrIDConflict。
type ReliableEngine interface {
	// Enqueue 入队。id 必须合法；返回 duplicate=true 表示该ID已存在（未完成、失败留存或在去重窗口内）
	Enqueue(ctx context.Context, id string, data []byte, dedup bool) (duplicate bool, err error)
	// Receive 领取一个任务
	Receive(ctx context.Context) (*Delivery, error)
	// Ack 确认成功
	Ack(ctx context.Context, d *Delivery) error
	// Renew 续期租约
	Renew(ctx context.Context, d *Delivery) error
	// Retry 原子转入延迟重试并释放当前领取
	Retry(ctx context.Context, d *Delivery, delay time.Duration, reason string) error
	// Fail 原子转入失败留存并释放当前领取
	Fail(ctx context.Context, d *Delivery, reason string) error
	// ListFailed 返回最早失败的至多 limit 条（1..MaxListFailed）
	ListFailed(ctx context.Context, limit int) ([]FailedMessage, error)
	// ReplayFailed 将失败任务转回就绪队列（重试预算重置，ID 不变）
	ReplayFailed(ctx context.Context, id string) error
	// Close 释放引擎自有资源（不关闭外部传入的 Redis 客户端）
	Close() error
}

// MaxListFailed ListFailed 单次返回的上限
const MaxListFailed = 1000

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidateID 校验任务ID/队列名称
func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return common.ErrInvalidID
	}
	return nil
}
