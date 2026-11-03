// Package ledger 实现逐ID append-only JSONL 账本及其严格校验器。
//
// 账本是验收的唯一事实来源：提交、每次 Processor 调用的起止、完成观察、检查结果都以
// 追加方式落盘；校验器只读取磁盘上的文件重新推导结论，不依赖进程内计数。
// 本包不依赖被验收的候选模块，可独立 go test。
package ledger

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 记录类型
const (
	KSubmit    = "submit"     // 一次提交尝试（同一ID可有多次，必须逐次追加）
	KProcStart = "proc_start" // 一次 Processor 调用开始
	KProcEnd   = "proc_end"   // 一次 Processor 调用结束
	KFinal     = "final"      // 对账端观察到的最终状态
	KCheck     = "check"      // 场景检查结果（含查询失败）
	KEvent     = "event"      // 故障注入等时间点
)

// 提交结果
const (
	ResAccepted  = "accepted"  // 服务端明确接收（Enqueue 返回 nil）
	ResRejected  = "rejected"  // 服务端明确拒绝（Redis 错误回复，命令未生效）
	ResUncertain = "uncertain" // 超时/断连等，不能断言是否已生效
)

// 最终状态
const (
	StateDone           = "done"            // m:<id> 不存在且墓碑摘要与账本一致
	StateFailed         = "failed"          // 留存在失败集合
	StateDigestMismatch = "digest_mismatch" // 墓碑内容摘要与账本不符
)

// Rec 是账本中的一行。零值字段在 JSON 中省略。
type Rec struct {
	K string `json:"k"`
	T int64  `json:"t"` // 记录追加时刻 unix 纳秒

	ID  string `json:"id,omitempty"`
	Sha string `json:"sha,omitempty"` // 载荷 SHA256（十六进制）
	Len int    `json:"len,omitempty"`

	// submit
	Res string `json:"res,omitempty"`
	Err string `json:"err,omitempty"`
	TB  int64  `json:"tb,omitempty"` // 提交调用前
	TA  int64  `json:"ta,omitempty"` // 提交调用返回后
	Try int    `json:"try,omitempty"`
	Tag string `json:"tag,omitempty"` // 场景自定义标记（如 after_snapshot）

	// proc_*
	Pid  int    `json:"pid,omitempty"`
	Call string `json:"call,omitempty"` // pid:序号，唯一标识一次调用
	POK  *bool  `json:"pok,omitempty"`  // 载荷是否按ID重新生成后逐字节一致
	Ret  string `json:"ret,omitempty"`  // proc_end：nil 或错误文本

	// final
	State string `json:"state,omitempty"`
	UB    int64  `json:"ub,omitempty"` // 观察上界（纳秒）：真实完成时刻 ∈ (T-UB, T]

	// check / event
	Name   string `json:"name,omitempty"`
	Pass   *bool  `json:"pass,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// SHA256Hex 返回数据的十六进制 SHA256
func SHA256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Bool 取地址的小工具
func Bool(b bool) *bool { return &b }

// Writer 是 append-only 账本写入器。文件以 O_EXCL 新建，拒绝覆盖已有账本。
type Writer struct {
	mu     sync.Mutex
	f      *os.File
	path   string
	err    error // 首个写错误，之后一律失败
	nCalls int64
	pid    int
}

// Create 新建账本文件（已存在则失败）
func Create(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create ledger %s: %w", path, err)
	}
	return &Writer{f: f, path: path, pid: os.Getpid()}, nil
}

// Path 账本路径
func (w *Writer) Path() string { return w.path }

// Append 追加一行（单次 write），返回写入错误；写错误会被记住，Err 可在场景末断言。
func (w *Writer) Append(r Rec) error {
	if r.K == "" {
		return errors.New("ledger: record without kind")
	}
	if r.T == 0 {
		r.T = time.Now().UnixNano()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return w.fail(fmt.Errorf("marshal: %w", err))
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if n, err := w.f.Write(b); err != nil || n != len(b) {
		if err == nil {
			err = io.ErrShortWrite
		}
		w.err = fmt.Errorf("append ledger %s: %w", w.path, err)
		return w.err
	}
	return nil
}

func (w *Writer) fail(err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
	}
	return w.err
}

// NextCall 返回本进程内唯一的调用标识 pid:序号
func (w *Writer) NextCall() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nCalls++
	return strconv.Itoa(w.pid) + ":" + strconv.FormatInt(w.nCalls, 10)
}

// Err 返回首个写错误
func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Close 同步并关闭
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	serr := w.f.Sync()
	cerr := w.f.Close()
	if w.err != nil {
		return w.err
	}
	if serr != nil {
		return serr
	}
	return cerr
}

// ReadFile 严格解析账本：未知字段、坏行、缺少类型都是错误（不会被悄悄跳过）。
// 最后一行若缺少换行（被 SIGKILL 截断）同样视为错误。
func ReadFile(path string) ([]Rec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("ledger %s: truncated last line", path)
	}
	var out []Rec
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	line := 0
	for sc.Scan() {
		line++
		txt := strings.TrimSpace(sc.Text())
		if txt == "" {
			return nil, fmt.Errorf("ledger %s:%d: blank line", path, line)
		}
		dec := json.NewDecoder(strings.NewReader(txt))
		dec.DisallowUnknownFields()
		var r Rec
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("ledger %s:%d: %w", path, line, err)
		}
		if r.K == "" {
			return nil, fmt.Errorf("ledger %s:%d: missing kind", path, line)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ledger %s: %w", path, err)
	}
	return out, nil
}

// ReadFiles 读取并按时间合并多个账本文件（多进程各写各的）
func ReadFiles(paths ...string) ([]Rec, error) {
	var all []Rec
	for _, p := range paths {
		rs, err := ReadFile(p)
		if err != nil {
			return nil, err
		}
		all = append(all, rs...)
	}
	sortByTime(all)
	return all, nil
}
