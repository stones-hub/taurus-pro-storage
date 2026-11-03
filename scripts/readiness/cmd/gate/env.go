package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"

	"taurus-readiness/dockerx"
)

const (
	// 固定镜像摘要（Redis 8.10.2）。不拉取：本机没有就失败，避免隐式网络副作用。
	redisImage   = "redis@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0"
	redisVersion = "8.10.2"
	ownerValue   = "taurus-pro-storage-strict"
)

// Env 一轮验收的环境。所有 docker 资源通过 D 创建与操作。
type Env struct {
	RunID string
	Dir   string // 证据目录
	D     *dockerx.Docker

	kidsMu sync.Mutex
	kids   []*child // 本轮启动的消费者子进程（精确 PID 管理）
}

// Inst 是一个私有 Redis 实例
type Inst struct {
	C    dockerx.Res
	Vol  dockerx.Res
	Addr string
	Tag  string
}

// redisOpts 实例参数
type redisOpts struct {
	Tag         string
	MaxMemory   string // 如 128mb
	AppendOnly  bool
	AofAlways   bool
	Vol         *dockerx.Res // 复用已有卷（崩溃恢复）；nil 则新建
	CreateOnly  bool         // 只 create 不 start
	ExtraBefore []string
}

func (e *Env) name(tag string) string { return "tps-" + e.RunID + "-" + tag }

// newRedis 创建并（默认）启动一个私有 Redis，核验版本与关键配置。
func (e *Env) newRedis(ctx context.Context, o redisOpts) (*Inst, error) {
	in := &Inst{Tag: o.Tag}
	if o.Vol != nil {
		in.Vol = *o.Vol
	} else {
		v, err := e.D.CreateVolume(ctx, e.name(o.Tag+"-vol"), o.Tag)
		if err != nil {
			return nil, err
		}
		in.Vol = v
	}
	if o.MaxMemory == "" {
		o.MaxMemory = "128mb"
	}
	args := []string{
		"--cpus", "1", "--memory", "384m", "--memory-swap", "384m",
		"-p", "127.0.0.1::6379", "-v", in.Vol.ID + ":/data", redisImage,
		"redis-server", "--maxmemory", o.MaxMemory, "--maxmemory-policy", "noeviction",
		"--save", "", "--appendonly", yesNo(o.AppendOnly),
	}
	if o.AofAlways {
		args = append(args, "--appendfsync", "always")
	}
	// create -> 登记完整ID -> start：start 失败时容器已在清单里，由精确清理处理
	c, err := e.D.CreateContainer(ctx, e.name(o.Tag), o.Tag, args...)
	if c.ID != "" {
		in.C = c // 即使 create 报错但已登记（adopt），也要把资源交还给调用方
	}
	if err != nil {
		return in, err
	}
	if o.CreateOnly {
		return in, nil
	}
	if err := e.D.Start(ctx, in.C); err != nil {
		return in, fmt.Errorf("start %s: %w", in.C.Name, err)
	}
	if err := e.waitReady(ctx, in, 20*time.Second); err != nil {
		return in, err
	}
	return in, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// waitReady 等待端口映射出现且 PING 成功，并核验版本/配置。返回前 in.Addr 已刷新。
func (e *Env) waitReady(ctx context.Context, in *Inst, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		st, err := e.D.Inspect(ctx, in.C)
		if err != nil {
			return err
		}
		if st.Running && st.HostPort != "" {
			addr := net.JoinHostPort("127.0.0.1", st.HostPort)
			if err := pingOnce(ctx, addr); err == nil {
				in.Addr = addr
				return e.verifyServer(ctx, in)
			} else {
				last = err
			}
		} else if !st.Running && st.Status == "exited" {
			return fmt.Errorf("container %s exited (code %d) before becoming ready", in.C.Name, st.ExitCode)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("redis %s not ready within %v: %v", in.C.Name, d, last)
}

func pingOnce(ctx context.Context, addr string) error {
	r := rawClient(addr)
	defer r.Close()
	c, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return r.Ping(c).Err()
}

// rawClient 观测用客户端：无自动重试，错误原样暴露
func rawClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: addr, MaxRetries: -1, Protocol: 2, DisableIdentity: true,
		DialTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, PoolSize: 4,
	})
}

func (e *Env) verifyServer(ctx context.Context, in *Inst) error {
	r := rawClient(in.Addr)
	defer r.Close()
	info, err := r.Info(ctx, "server").Result()
	if err != nil {
		return fmt.Errorf("INFO server: %w", err)
	}
	if v := infoField(info, "redis_version"); v != redisVersion {
		return fmt.Errorf("redis_version=%q, want %s", v, redisVersion)
	}
	for _, kv := range [][2]string{{"maxmemory-policy", "noeviction"}} {
		res, err := r.ConfigGet(ctx, kv[0]).Result()
		if err != nil {
			return fmt.Errorf("CONFIG GET %s: %w", kv[0], err)
		}
		if res[kv[0]] != kv[1] {
			return fmt.Errorf("config %s=%q want %q", kv[0], res[kv[0]], kv[1])
		}
	}
	return nil
}

func infoField(info, key string) string {
	for _, ln := range strings.Split(info, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, key+":") {
			return strings.TrimPrefix(ln, key+":")
		}
	}
	return ""
}

func infoInt(info, key string) (int64, error) {
	v := infoField(info, key)
	if v == "" {
		return 0, fmt.Errorf("INFO field %q missing", key)
	}
	return strconv.ParseInt(v, 10, 64)
}

// queueClient 创建被测库使用的 Redis 客户端（redisx）。InitRedis 写全局变量，
// 这里立刻取走指针；各场景串行创建，不并发调用。
func queueClient(addr string) (*redisx.RedisClient, error) {
	if err := redisx.InitRedis(redisx.WithAddrs(addr), redisx.WithPoolSize(32), redisx.WithMinIdleConns(2),
		redisx.WithTimeout(2*time.Second, 3*time.Second, 3*time.Second)); err != nil {
		return nil, err
	}
	c := redisx.Redis
	redisx.Redis = nil // 防止被测代码意外使用全局默认客户端
	return c, nil
}

// ---- manifest 与 preflight ----

func (e *Env) preflight(ctx context.Context) error {
	if _, err := e.D.R.Run(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("docker not reachable: %w", err)
	}
	out, err := e.D.R.Run(ctx, "image", "inspect", "--format", "{{.Id}}", redisImage)
	if err != nil {
		return fmt.Errorf("pinned redis image is not present locally (the gate never pulls): %w", err)
	}
	if got := strings.TrimSpace(out); got != strings.TrimPrefix(redisImage, "redis@") {
		return fmt.Errorf("image id %s != pinned digest", got)
	}
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func (e *Env) path(parts ...string) string {
	return filepath.Join(append([]string{e.Dir}, parts...)...)
}

var errTimeout = errors.New("timeout")
