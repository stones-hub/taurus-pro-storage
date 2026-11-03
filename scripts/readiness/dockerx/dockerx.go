// Package dockerx 封装验收用到的 docker 操作，并强制“只操作本轮自己创建的资源”：
//
//   - 每个资源创建后立即登记到 manifest（完整容器ID / 卷名 + 期望的 owner/run 标签）；
//   - kill/start/exec/cp/rm 之前都会 inspect，核验完整ID与 owner/run 标签；不符一律拒绝，
//     并且不会发出任何改变对象的命令；
//   - 不提供按前缀/标签枚举删除，不调用任何 prune；清理只遍历 manifest 的显式清单。
package dockerx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// remedyTimeout 是补救动作（认领/精确删除）的独立时限：补救发生在 run ctx 可能已取消（如收到信号）之后，
// 因此不能沿用 run ctx，也不能无限等待。
const remedyTimeout = 30 * time.Second

// remedyCtx 返回不随 run 取消的有限 ctx，仅用于对本轮资源的精确认领/删除。
func remedyCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), remedyTimeout)
}

// 标签键
const (
	LabelOwner = "taurus.strict.owner"
	LabelRun   = "taurus.strict.run"
)

// ErrNotOwned 资源标签/ID 与登记不符，拒绝操作
var ErrNotOwned = errors.New("dockerx: resource is not owned by this run (refusing to touch it)")

// Runner 执行 docker 子命令
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout string, err error)
}

// ExecRunner 通过 docker CLI 执行；不经过 shell。
type ExecRunner struct{ Bin string }

// Run 实现 Runner。拒绝任何含 prune 的参数（本工具不允许广域清理）。
func (e ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	for _, a := range args {
		if strings.Contains(strings.ToLower(a), "prune") {
			return "", fmt.Errorf("dockerx: refusing docker %s: prune is forbidden", strings.Join(args, " "))
		}
	}
	bin := e.Bin
	if bin == "" {
		bin = "docker"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Res 是登记的一个资源
type Res struct {
	Kind        string `json:"kind"` // container | volume
	ID          string `json:"id"`   // 容器：完整64位ID；卷：卷名
	Name        string `json:"name"`
	ExpectOwner string `json:"expect_owner"`
	ExpectRun   string `json:"expect_run"`
	Purpose     string `json:"purpose,omitempty"`
}

// Manifest 持久化资源清单，每次登记立即落盘
type Manifest struct {
	mu   sync.Mutex
	Path string
	Res  []Res
}

// NewManifest 新建清单文件（已存在则失败）
func NewManifest(path string) (*Manifest, error) {
	m := &Manifest{Path: path}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("manifest %s already exists", path)
	}
	return m, m.save()
}

// LoadManifest 读取已有清单
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &Manifest{Path: path}
	if err := json.Unmarshal(b, &m.Res); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return m, nil
}

func (m *Manifest) save() error {
	b, err := json.MarshalIndent(m.Res, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.Path)
}

// Add 登记并落盘
func (m *Manifest) Add(r Res) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Res = append(m.Res, r)
	// 历史清单只增不减：即使条目被精确删除，也能事后逐个核验“确实已不存在”
	if h, err := os.OpenFile(m.Path+".history", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(h, "%s\t%s\t%s\n", r.Kind, r.ID, r.Name)
		h.Close()
	} else {
		return err
	}
	return m.save()
}

// Snapshot 返回当前清单副本
func (m *Manifest) Snapshot() []Res {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Res(nil), m.Res...)
}

// Remove 从清单移除（资源已被精确删除后）
func (m *Manifest) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.Res[:0:0]
	for _, r := range m.Res {
		if r.ID != id {
			out = append(out, r)
		}
	}
	m.Res = out
	return m.save()
}

// Docker 带归属校验的操作集合
type Docker struct {
	R        Runner
	Owner    string // 本轮期望的 owner 标签值
	Run      string // 本轮 run ID
	Manifest *Manifest
}

type inspected struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running  bool   `json:"Running"`
		Status   string `json:"Status"`
		ExitCode int    `json:"ExitCode"`
		OOM      bool   `json:"OOMKilled"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

type volInspected struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// State 是容器状态快照
type State struct {
	Running   bool
	Status    string
	ExitCode  int
	OOMKilled bool
	HostPort  string // 6379/tcp 映射的宿主端口（运行中才有）
}

// check 同时要求：登记的期望 owner/run 等于当前 Docker 上下文的 owner/run（不能靠串用/篡改 manifest
// 越权），且资源实际标签等于登记值。
func (d *Docker) check(labels map[string]string, r Res) error {
	switch {
	case r.ExpectOwner == "" || r.ExpectRun == "" || d.Owner == "" || d.Run == "":
		return fmt.Errorf("%w: %s %s has empty owner/run expectation", ErrNotOwned, r.Kind, r.Name)
	case r.ExpectOwner != d.Owner || r.ExpectRun != d.Run:
		return fmt.Errorf("%w: %s %s is registered for owner=%q run=%q but the current context is owner=%q run=%q",
			ErrNotOwned, r.Kind, r.Name, r.ExpectOwner, r.ExpectRun, d.Owner, d.Run)
	case labels[LabelOwner] != r.ExpectOwner || labels[LabelRun] != r.ExpectRun:
		return fmt.Errorf("%w: %s %s labels owner=%q run=%q, expected owner=%q run=%q",
			ErrNotOwned, r.Kind, r.Name, labels[LabelOwner], labels[LabelRun], r.ExpectOwner, r.ExpectRun)
	}
	return nil
}

// Verify inspect 并核验归属（只读命令）。资源不存在返回 ErrGone。
func (d *Docker) Verify(ctx context.Context, r Res) (*State, error) {
	switch r.Kind {
	case "container":
		if len(r.ID) != 64 {
			return nil, fmt.Errorf("%w: container %s has no full 64-hex id recorded", ErrNotOwned, r.Name)
		}
		out, err := d.R.Run(ctx, "inspect", "--type", "container", r.ID)
		if err != nil {
			if isNoSuch(err) {
				return nil, ErrGone
			}
			return nil, err
		}
		var arr []inspected
		if err := json.Unmarshal([]byte(out), &arr); err != nil || len(arr) != 1 {
			return nil, fmt.Errorf("inspect %s: unexpected output (%v)", r.Name, err)
		}
		c := arr[0]
		if c.ID != r.ID {
			return nil, fmt.Errorf("%w: inspected id %.12s != recorded %.12s", ErrNotOwned, c.ID, r.ID)
		}
		if err := d.check(c.Config.Labels, r); err != nil {
			return nil, err
		}
		st := &State{Running: c.State.Running, Status: c.State.Status, ExitCode: c.State.ExitCode, OOMKilled: c.State.OOM}
		if p := c.NetworkSettings.Ports["6379/tcp"]; len(p) > 0 {
			st.HostPort = p[0].HostPort
		}
		return st, nil
	case "volume":
		out, err := d.R.Run(ctx, "volume", "inspect", r.ID)
		if err != nil {
			if isNoSuch(err) {
				return nil, ErrGone
			}
			return nil, err
		}
		var arr []volInspected
		if err := json.Unmarshal([]byte(out), &arr); err != nil || len(arr) != 1 {
			return nil, fmt.Errorf("volume inspect %s: unexpected output (%v)", r.Name, err)
		}
		if arr[0].Name != r.ID {
			return nil, fmt.Errorf("%w: volume name mismatch", ErrNotOwned)
		}
		return nil, d.check(arr[0].Labels, r)
	}
	return nil, fmt.Errorf("unknown resource kind %q", r.Kind)
}

// isNoSuch 判断 docker 报告对象不存在（容器报 "No such container"，卷报 "no such volume"，大小写不同）
func isNoSuch(err error) bool { return strings.Contains(strings.ToLower(err.Error()), "no such") }

// ErrGone 资源已不存在
var ErrGone = errors.New("dockerx: resource no longer exists")

// verified 先核验，再执行 docker 子命令
func (d *Docker) verified(ctx context.Context, r Res, args ...string) (string, error) {
	if _, err := d.Verify(ctx, r); err != nil {
		return "", err
	}
	return d.R.Run(ctx, args...)
}

// Kill 向容器发送信号（先核验）
func (d *Docker) Kill(ctx context.Context, r Res, signal string) error {
	_, err := d.verified(ctx, r, "kill", "--signal", signal, r.ID)
	return err
}

// Start 启动已存在的容器
func (d *Docker) Start(ctx context.Context, r Res) error {
	_, err := d.verified(ctx, r, "start", r.ID)
	return err
}

// Exec 在容器内执行命令（不经 shell）
func (d *Docker) Exec(ctx context.Context, r Res, cmd ...string) (string, error) {
	return d.verified(ctx, r, append([]string{"exec", r.ID}, cmd...)...)
}

// CopyFrom 从容器复制到宿主
func (d *Docker) CopyFrom(ctx context.Context, r Res, src, dst string) error {
	_, err := d.verified(ctx, r, "cp", r.ID+":"+src, dst)
	return err
}

// CopyTo 从宿主复制进容器
func (d *Docker) CopyTo(ctx context.Context, r Res, src, dst string) error {
	_, err := d.verified(ctx, r, "cp", src, r.ID+":"+dst)
	return err
}

// Inspect 核验后返回状态
func (d *Docker) Inspect(ctx context.Context, r Res) (*State, error) { return d.Verify(ctx, r) }

// Logs 读取容器日志（核验后）
func (d *Docker) Logs(ctx context.Context, r Res) (string, error) {
	return d.verified(ctx, r, "logs", r.ID)
}

// Remove 精确删除一个资源（先核验）；成功后从清单移除。资源已不存在视为已清理。
func (d *Docker) Remove(ctx context.Context, r Res) error {
	var args []string
	switch r.Kind {
	case "container":
		args = []string{"rm", "-f", r.ID}
	case "volume":
		args = []string{"volume", "rm", r.ID}
	default:
		return fmt.Errorf("unknown resource kind %q", r.Kind)
	}
	if _, err := d.verified(ctx, r, args...); err != nil {
		if errors.Is(err, ErrGone) {
			return d.Manifest.Remove(r.ID)
		}
		return err
	}
	return d.Manifest.Remove(r.ID)
}

// CleanupAll 逆序删除清单中的显式资源。返回每项结果；任何失败（含标签不符被拒绝）都会体现在 error。
func (d *Docker) CleanupAll(ctx context.Context) ([]string, error) {
	var log []string
	var errs []error
	rs := d.Manifest.Snapshot()
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if err := d.Remove(ctx, r); err != nil {
			errs = append(errs, err)
			log = append(log, fmt.Sprintf("FAILED %s %s (%.12s): %v", r.Kind, r.Name, r.ID, err))
			continue
		}
		log = append(log, fmt.Sprintf("removed %s %s (%.12s)", r.Kind, r.Name, r.ID))
	}
	return log, errors.Join(errs...)
}

// Labels 返回本轮应附加的标签参数
func (d *Docker) Labels(purpose string) []string {
	return []string{"--label", LabelOwner + "=" + d.Owner, "--label", LabelRun + "=" + d.Run, "--label", "taurus.strict.purpose=" + purpose}
}

// CreateVolume 创建带标签的卷并登记
func (d *Docker) CreateVolume(ctx context.Context, name, purpose string) (Res, error) {
	args := append([]string{"volume", "create"}, d.Labels(purpose)...)
	args = append(args, name)
	out, err := d.R.Run(ctx, args...)
	if err != nil {
		if r, ok := d.adoptVolume(name, purpose); ok {
			return r, fmt.Errorf("docker volume create reported an error but the volume exists and was registered for cleanup: %w", err)
		}
		return Res{}, err
	}
	r := Res{Kind: "volume", ID: strings.TrimSpace(out), Name: name, ExpectOwner: d.Owner, ExpectRun: d.Run, Purpose: purpose}
	if r.ID != name {
		return Res{}, fmt.Errorf("volume create returned %q, want %q", r.ID, name)
	}
	if err := d.Manifest.Add(r); err != nil {
		// 登记失败：卷已存在但清单里没有；经核验后按精确卷名删除（独立 ctx，不随 run 取消）
		rctx, rcancel := remedyCtx()
		defer rcancel()
		if _, verr := d.verified(rctx, r, "volume", "rm", r.ID); verr != nil && !errors.Is(verr, ErrGone) {
			return Res{}, fmt.Errorf("register volume %s failed (%v) and exact removal also failed: %w", r.ID, err, verr)
		}
		return Res{}, fmt.Errorf("register volume %s failed (%v); it was removed by exact name", r.ID, err)
	}
	if _, err := d.Verify(ctx, r); err != nil {
		return Res{}, err
	}
	return r, nil
}

// CreateContainer 执行 `docker create`（不启动），立即登记完整ID，再核验标签。
// 启动由调用方通过 Start 单独完成：即使 start 失败，容器也已在清单里，可被精确清理。
// args 为 create 之后的完整参数（不含 --name 与标签，本函数追加）。
//
// 失败补救（只针对本次创建的、可精确识别的资源）：
//   - create 报错但容器实际已存在：按（本轮唯一的）名字 inspect，ID 完整且标签等于本轮 owner/run 才登记；
//   - 创建成功但登记（落盘）失败：用刚拿到的完整ID经归属核验后精确 rm -f。
func (d *Docker) CreateContainer(ctx context.Context, name, purpose string, args ...string) (Res, error) {
	full := []string{"create", "--name", name}
	full = append(full, d.Labels(purpose)...)
	full = append(full, args...)
	out, err := d.R.Run(ctx, full...)
	if err != nil {
		if r, ok := d.adoptByName(name, purpose); ok {
			return r, fmt.Errorf("docker create reported an error but the container exists and was registered for cleanup: %w", err)
		}
		return Res{}, err
	}
	id := strings.TrimSpace(out)
	if i := strings.LastIndex(id, "\n"); i >= 0 {
		id = id[i+1:]
	}
	if len(id) != 64 {
		return Res{}, fmt.Errorf("docker create returned %q, not a full container id", id)
	}
	r := Res{Kind: "container", ID: id, Name: name, ExpectOwner: d.Owner, ExpectRun: d.Run, Purpose: purpose}
	if err := d.Manifest.Add(r); err != nil {
		// 登记失败：容器已存在但清单里没有；用精确ID经核验后删除，失败则把ID带在错误里供人工精确处理
		if rerr := d.removeUnregistered(r); rerr != nil {
			return Res{}, fmt.Errorf("register container %.12s failed (%v) and exact removal also failed: %w", id, err, rerr)
		}
		return Res{}, fmt.Errorf("register container %.12s failed (%v); it was removed by exact id", id, err)
	}
	if _, err := d.Verify(ctx, r); err != nil {
		return r, err
	}
	return r, nil
}

// adoptByName 在 create 报错后，按本轮唯一名字查找可能已创建的容器；仅当完整ID与归属标签都匹配才登记。
func (d *Docker) adoptByName(name, purpose string) (Res, bool) {
	ctx, cancel := remedyCtx()
	defer cancel()
	out, err := d.R.Run(ctx, "inspect", "--type", "container", name)
	if err != nil {
		return Res{}, false
	}
	var arr []inspected
	if json.Unmarshal([]byte(out), &arr) != nil || len(arr) != 1 || len(arr[0].ID) != 64 {
		return Res{}, false
	}
	l := arr[0].Config.Labels
	if l[LabelOwner] != d.Owner || l[LabelRun] != d.Run {
		return Res{}, false
	}
	r := Res{Kind: "container", ID: arr[0].ID, Name: name, ExpectOwner: d.Owner, ExpectRun: d.Run, Purpose: purpose}
	if d.Manifest.Add(r) != nil {
		_ = d.removeUnregistered(r)
		return Res{}, false
	}
	return r, true
}

// adoptVolume 在 volume create 报错后，按本轮唯一卷名核验；名字与归属标签都匹配才登记。
func (d *Docker) adoptVolume(name, purpose string) (Res, bool) {
	ctx, cancel := remedyCtx()
	defer cancel()
	out, err := d.R.Run(ctx, "volume", "inspect", name)
	if err != nil {
		return Res{}, false
	}
	var arr []volInspected
	if json.Unmarshal([]byte(out), &arr) != nil || len(arr) != 1 || arr[0].Name != name {
		return Res{}, false
	}
	if arr[0].Labels[LabelOwner] != d.Owner || arr[0].Labels[LabelRun] != d.Run {
		return Res{}, false
	}
	r := Res{Kind: "volume", ID: name, Name: name, ExpectOwner: d.Owner, ExpectRun: d.Run, Purpose: purpose}
	if d.Manifest.Add(r) != nil {
		rctx, rcancel := remedyCtx()
		defer rcancel()
		_, _ = d.verified(rctx, r, "volume", "rm", r.ID)
		return Res{}, false
	}
	return r, true
}

// removeUnregistered 删除尚未（成功）登记的、本轮刚创建的容器：仍然先核验完整ID与标签。
func (d *Docker) removeUnregistered(r Res) error {
	ctx, cancel := remedyCtx()
	defer cancel()
	_, err := d.verified(ctx, r, "rm", "-f", r.ID)
	if errors.Is(err, ErrGone) {
		return nil
	}
	return err
}
