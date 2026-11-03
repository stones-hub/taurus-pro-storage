// 可靠队列外部调用示例：只使用公开 API，演示成功、panic、重试、失败留存/查询/重放。
//
// 需要一个可访问的 Redis（单机或主从直连，不支持 Cluster；本仓库仅在 Redis 8.10.2 上验收）：
//
//	REDIS_ADDR=127.0.0.1:6379 go run ./examples/reliable_queue_example
//
// 注意：它会在该 Redis 的 taurus:q2:{...} 命名空间下创建数据，请只在测试环境运行。
// 设置 QUEUE_NAME 可以指定队列名称（默认带随机后缀）；设置 CRASH_AFTER_CLAIM=1 时，
// 进程在领取任务后阻塞，供外部脚本 kill 后再用同一 QUEUE_NAME 启动另一个进程恢复。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/engine"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

var succeeded, flakyCalls atomic.Int64

// processor 必须幂等：同一任务在崩溃恢复、租约被接管时可能被重复投递。
type processor struct{}

func (processor) Process(ctx context.Context, data []byte) error {
	switch string(data) {
	case "panic":
		panic("processor bug")
	case "flaky": // 前两次可重试失败，第三次成功
		if flakyCalls.Add(1) < 3 {
			return common.NewRetryableError(errors.New("downstream busy"))
		}
	case "permanent":
		return errors.New("invalid business state") // 非可重试错误：直接失败留存
	case "block":
		if os.Getenv("CRASH_AFTER_CLAIM") == "1" {
			fmt.Println("CLAIMED")
			select {} // 等待被 kill
		}
	}
	succeeded.Add(1)
	return nil
}

func main() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		log.Fatal("REDIS_ADDR is required (refusing to guess a default Redis)")
	}
	name := os.Getenv("QUEUE_NAME")
	if name == "" {
		name = fmt.Sprintf("example-%d", time.Now().UnixNano())
	}
	if err := redisx.InitRedis(redisx.WithAddrs(addr)); err != nil {
		log.Fatal(err)
	}

	m, err := queue.NewManager(processor{}, &queue.Config{
		EngineType:    engine.REDIS,
		Source:        name,
		WorkerCount:   2,
		MaxRetries:    2,
		RetryDelay:    200 * time.Millisecond,
		MaxRetryDelay: time.Second,
		LeaseDuration: 3 * time.Second,
		Timeout:       10 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Start(); err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	if os.Getenv("CRASH_AFTER_CLAIM") == "1" {
		// 稳定ID入队：超时/断连后可用同一ID安全重试
		if err := m.AddDataWithID(ctx, "crash-demo", []byte("block")); err != nil {
			log.Fatal(err)
		}
		select {}
	}

	for _, d := range []struct{ id, body string }{
		{"ok-1", "hello"}, {"ok-2", "world"}, {"panic-1", "panic"}, {"flaky-1", "flaky"}, {"perm-1", "permanent"},
	} {
		if err := m.AddDataWithID(ctx, d.id, []byte(d.body)); err != nil {
			log.Fatal(err)
		}
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		failed, err := m.ListFailed(ctx, 100)
		if err != nil {
			log.Fatal(err)
		}
		if succeeded.Load() == 3 && len(failed) == 2 { // ok-1, ok-2, flaky-1 成功；panic-1, perm-1 失败留存
			for _, f := range failed {
				fmt.Printf("failed: id=%s attempts=%d reason=%q\n", f.ID, f.Attempts, f.Reason)
			}
			// 显式重放：此处 permanent 仍会再次失败，仅演示 API
			if err := m.ReplayFailed(ctx, "perm-1"); err != nil {
				log.Fatal(err)
			}
			fmt.Println("RESULT ok succeeded=3 failed=2 replayed=perm-1")
			if err := m.Stop(ctx); err != nil {
				log.Fatal(err)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Fatalf("timeout: succeeded=%d", succeeded.Load())
}
