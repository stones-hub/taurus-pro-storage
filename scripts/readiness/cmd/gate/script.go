package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"

	"taurus-readiness/ledger"
)

// action 是脚本化 Processor 对一次调用的决定
type action int

const (
	actOK        action = iota // 返回 nil
	actRetryable               // 返回 common.NewRetryableError
	actFatal                   // 返回普通错误（直接失败留存）
	actPanic                   // panic（按可重试处理）
	actBlockCoop               // 阻塞直到 ctx 取消（合作式），返回 ctx.Err()
	actBlockHard               // 忽略 ctx，阻塞直到 release 关闭后返回 nil
)

// scriptProc 按 (ID, 本进程内第几次调用) 决定行为；每次调用的起止都追加到账本（panic 也先写 proc_end）。
// 载荷照常逐字节校验。calls 只在本进程内计数，跨进程场景不要用它。
type scriptProc struct {
	L       *ledger.Writer
	Work    func(id string) time.Duration
	Decide  func(id string, call int) action
	Release chan struct{} // actBlockHard 的放行信号

	mu    sync.Mutex
	calls map[string]int
}

func (p *scriptProc) nth(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[id]++
	return p.calls[id]
}

// Calls 返回本进程内某ID的调用次数
func (p *scriptProc) Calls(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[id]
}

var errScriptedFatal = errors.New("scripted fatal error")

func (p *scriptProc) Process(ctx context.Context, data []byte) error {
	id, ok := ledger.VerifyPayload(data)
	call := p.L.NextCall()
	n := p.nth(id)
	_ = p.L.Append(ledger.Rec{K: ledger.KProcStart, ID: id, Sha: ledger.SHA256Hex(data), Len: len(data), Pid: os.Getpid(), Call: call, POK: ledger.Bool(ok)})
	end := func(ret string) {
		_ = p.L.Append(ledger.Rec{K: ledger.KProcEnd, ID: id, Pid: os.Getpid(), Call: call, Ret: ret})
	}
	if p.Work != nil {
		if w := p.Work(id); w > 0 {
			t := time.NewTimer(w)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				end("ctx: " + ctx.Err().Error())
				return ctx.Err()
			}
		}
	}
	act := actOK
	if p.Decide != nil {
		act = p.Decide(id, n)
	}
	switch act {
	case actRetryable:
		end("retryable")
		return common.NewRetryableError(fmt.Errorf("scripted retryable (call %d)", n))
	case actFatal:
		end("fatal")
		return fmt.Errorf("%w (call %d)", errScriptedFatal, n)
	case actPanic:
		end("panic")
		panic(fmt.Sprintf("scripted panic %s call %d", id, n))
	case actBlockCoop:
		<-ctx.Done()
		end("ctx: " + ctx.Err().Error())
		return ctx.Err()
	case actBlockHard:
		<-p.Release
		end("nil")
		return nil
	}
	end("nil")
	return nil
}
