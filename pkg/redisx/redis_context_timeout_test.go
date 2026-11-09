package redisx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRESPServer 监听本地回环的最小RESP模拟服务，只用于验证客户端读写deadline，不是真实Redis。
// 收到 silentOn 指定的命令后不再回复，模拟服务端静默；其余命令按固定应答处理。
type fakeRESPServer struct {
	ln       net.Listener
	silentOn string
	silenced chan struct{} // 首次收到静默命令时关闭
	once     sync.Once

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// startFakeRESPServer 启动模拟服务，并在测试结束时关闭监听和全部连接、等待服务goroutine退出
func startFakeRESPServer(t *testing.T, silentOn string) *fakeRESPServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &fakeRESPServer{
		ln:       ln,
		silentOn: silentOn,
		silenced: make(chan struct{}),
		conns:    make(map[net.Conn]struct{}),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.close)
	return s
}

func (s *fakeRESPServer) addr() string { return s.ln.Addr().String() }

func (s *fakeRESPServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}

func (s *fakeRESPServer) serve(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	rd := bufio.NewReader(conn)
	for {
		args, err := readRESPCommand(rd)
		if err != nil {
			return
		}

		name := strings.ToUpper(args[0])
		if name == s.silentOn {
			s.once.Do(func() { close(s.silenced) })
			// 不回复，直到客户端断开或服务关闭
			io.Copy(io.Discard, rd)
			return
		}

		var reply string
		switch name {
		case "HELLO":
			// 模拟不支持HELLO的服务端，客户端回落RESP2
			reply = "-ERR unknown command 'HELLO'\r\n"
		case "PING":
			reply = "+PONG\r\n"
		case "GET":
			reply = "$-1\r\n"
		default:
			reply = "-ERR unsupported command\r\n"
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func (s *fakeRESPServer) close() {
	s.mu.Lock()
	s.closed = true
	for conn := range s.conns {
		conn.Close()
	}
	s.mu.Unlock()

	s.ln.Close()
	s.wg.Wait()
}

// readRESPCommand 读取一条客户端发送的RESP命令（bulk string数组）
func readRESPCommand(rd *bufio.Reader) ([]string, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("unexpected RESP header %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("invalid RESP array header %q", line)
	}

	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		header, err := rd.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(header, "$") {
			return nil, fmt.Errorf("unexpected RESP bulk header %q", header)
		}
		size, err := strconv.Atoi(strings.TrimSpace(header[1:]))
		if err != nil || size < 0 {
			return nil, fmt.Errorf("invalid RESP bulk header %q", header)
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(rd, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

// fakeServerOptions 返回指向模拟服务的配置，关闭最小空闲连接，避免后台预建连接
func fakeServerOptions(addrs []string, opts ...Option) *Options {
	options := DefaultOptions()
	WithAddrs(addrs...)(options)
	WithMinIdleConns(0)(options)
	for _, opt := range opts {
		opt(options)
	}
	return options
}

// ==================== ContextTimeoutEnabled 透传测试 ====================

func TestNewUniversalClientContextTimeoutPassthrough(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("Single enabled=%v", enabled), func(t *testing.T) {
			srv := startFakeRESPServer(t, "")

			client := newUniversalClient(fakeServerOptions(
				[]string{srv.addr()}, WithContextTimeoutEnabled(enabled)))
			t.Cleanup(func() { client.Close() })

			single, ok := client.(*redis.Client)
			require.True(t, ok, "单地址应构造单机客户端，实际为 %T", client)
			assert.Equal(t, enabled, single.Options().ContextTimeoutEnabled)
		})

		t.Run(fmt.Sprintf("Multi enabled=%v", enabled), func(t *testing.T) {
			srv1 := startFakeRESPServer(t, "")
			srv2 := startFakeRESPServer(t, "")

			client := newUniversalClient(fakeServerOptions(
				[]string{srv1.addr(), srv2.addr()}, WithContextTimeoutEnabled(enabled)))
			t.Cleanup(func() { client.Close() })

			cluster, ok := client.(*redis.ClusterClient)
			require.True(t, ok, "多地址应构造集群客户端，实际为 %T", client)
			assert.Equal(t, enabled, cluster.Options().ContextTimeoutEnabled)
		})
	}

	t.Run("Default", func(t *testing.T) {
		srv := startFakeRESPServer(t, "")

		client := newUniversalClient(fakeServerOptions([]string{srv.addr()}))
		t.Cleanup(func() { client.Close() })

		single, ok := client.(*redis.Client)
		require.True(t, ok)
		assert.False(t, single.Options().ContextTimeoutEnabled)
	})
}

// ==================== ctx deadline 读写语义测试 ====================

// TestContextTimeoutBoundsSilentServer 验证单机客户端在模拟服务静默时：
// 开启后有界ctx限制等待时间；关闭时ctx deadline不作用于socket读，仍按ReadTimeout等待。
// 覆盖两个场景：已建好连接上的GET回复读取，以及冷连接初始化握手（HELLO）回复读取。
func TestContextTimeoutBoundsSilentServer(t *testing.T) {
	const (
		socketTimeout = time.Second
		ctxTimeout    = 100 * time.Millisecond
	)

	cases := []struct {
		name       string
		silentOn   string
		warmUp     bool
		enabled    bool
		maxRetries int
	}{
		// 开启时保留默认重试，验证重试不会越过ctx deadline
		{name: "GET enabled", silentOn: "GET", warmUp: true, enabled: true, maxRetries: 3},
		{name: "GET disabled", silentOn: "GET", warmUp: true, enabled: false, maxRetries: -1},
		{name: "Cold HELLO enabled", silentOn: "HELLO", enabled: true, maxRetries: 3},
		{name: "Cold HELLO disabled", silentOn: "HELLO", enabled: false, maxRetries: -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFakeRESPServer(t, tc.silentOn)

			client := newUniversalClient(fakeServerOptions(
				[]string{srv.addr()},
				WithTimeout(socketTimeout, socketTimeout, socketTimeout),
				WithMaxRetries(tc.maxRetries),
				WithContextTimeoutEnabled(tc.enabled),
			))
			// 晚于模拟服务注册，先关闭客户端再关闭服务
			t.Cleanup(func() { client.Close() })

			if tc.warmUp {
				// 先完成握手，确保下面只测GET回复的读取
				pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := client.Ping(pingCtx).Err()
				cancel()
				require.NoError(t, err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
			defer cancel()

			start := time.Now()
			err := client.Get(ctx, "silent_key").Err()
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.NotErrorIs(t, err, redis.Nil)
			var netErr net.Error
			assert.True(t, errors.As(err, &netErr) && netErr.Timeout(), "应为超时错误，实际: %v", err)

			select {
			case <-srv.silenced:
			case <-time.After(time.Second):
				t.Fatalf("模拟服务未收到 %s，阻塞点不是预期的回复读取", tc.silentOn)
			}

			if tc.enabled {
				assert.GreaterOrEqual(t, elapsed, ctxTimeout-10*time.Millisecond, "不应早于ctx deadline返回")
				assert.Less(t, elapsed, 700*time.Millisecond, "开启后应受ctx deadline限制，耗时: %v", elapsed)
			} else {
				assert.GreaterOrEqual(t, elapsed, socketTimeout-100*time.Millisecond,
					"关闭时ctx deadline不作用于socket读，应等待ReadTimeout，耗时: %v", elapsed)
				assert.Less(t, elapsed, 3*socketTimeout, "关闭时也应在ReadTimeout附近返回，耗时: %v", elapsed)
			}
		})
	}
}
