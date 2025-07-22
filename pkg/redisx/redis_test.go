package redisx

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 测试主函数，用于初始化Redis客户端和清理测试文件
// 特殊函数，测试前和测试后都会执行
func TestMain(m *testing.M) {
	// 清理测试日志文件
	os.Remove("./testdata/redis_test.log")
	os.Remove("./testdata/redis_json_test.log")
	os.Remove("./testdata/redis_simple_test.log")
	os.MkdirAll("./testdata", 0755)

	// 初始化 Redis 客户端（用于基础测试）
	// 注意：基础Redis操作测试使用这个全局Redis客户端
	// 日志功能测试会创建自己的Redis客户端实例，因为需要特殊的日志配置
	err := InitRedis(
		WithAddrs("localhost:6379"),
		WithPassword(""),
		WithDB(0),
		WithPoolSize(10),
		WithMinIdleConns(5),
		WithTimeout(time.Second, time.Second, time.Second),
		WithMaxRetries(3),
	)
	if err != nil {
		fmt.Printf("跳过基础测试：Redis连接失败: %v\n", err)
		// 即使Redis连接失败，也继续运行测试（带日志的测试会单独初始化）
	}

	// 运行测试
	code := m.Run()

	// 清理资源
	if Redis != nil {
		Redis.Close()
	}

	os.Exit(code)
}

func TestRedisBasicOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}

	ctx := context.Background()

	t.Run("Set and Get", func(t *testing.T) {
		err := Redis.Set(ctx, "test_key", "test_value", time.Minute)
		assert.NoError(t, err)

		value, err := Redis.Get(ctx, "test_key")
		assert.NoError(t, err)
		assert.Equal(t, "test_value", value)

		// 测试不存在的键
		value, err = Redis.Get(ctx, "non_existent_key")
		assert.NoError(t, err)
		assert.Empty(t, value)

		// 清理测试数据
		Redis.client.Del(ctx, "test_key")
	})

	t.Run("Incr and Decr", func(t *testing.T) {
		// 测试递增
		val, err := Redis.Incr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)

		val, err = Redis.Incr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(2), val)

		// 测试递减
		val, err = Redis.Decr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)

		// 清理测试数据
		Redis.client.Del(ctx, "counter")
	})
}

func TestRedisHashOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("HSet and HGet with Field-Value Pairs", func(t *testing.T) {
		err := Redis.HSet(ctx, "user:1", "name", "张三", "age", "25")
		assert.NoError(t, err)

		value, err := Redis.HGet(ctx, "user:1", "name")
		assert.NoError(t, err)
		assert.Equal(t, "张三", value)

		value, err = Redis.HGet(ctx, "user:1", "age")
		assert.NoError(t, err)
		assert.Equal(t, "25", value)

		// 清理测试数据
		Redis.client.Del(ctx, "user:1")
	})

	t.Run("HSet and HGet with Map", func(t *testing.T) {
		userData := map[string]interface{}{
			"name": "李四",
			"age":  30,
		}
		err := Redis.HSet(ctx, "user:2", userData)
		assert.NoError(t, err)

		value, err := Redis.HGet(ctx, "user:2", "name")
		assert.NoError(t, err)
		assert.Equal(t, "李四", value)

		// 清理测试数据
		Redis.client.Del(ctx, "user:2")
	})

	t.Run("HGetAll", func(t *testing.T) {
		// 先设置测试数据
		err := Redis.HSet(ctx, "user:3", "name", "王五", "age", "35")
		assert.NoError(t, err)

		values, err := Redis.HGetList(ctx, "user:3")
		assert.NoError(t, err)
		assert.Equal(t, "王五", values["name"])
		assert.Equal(t, "35", values["age"])

		// 测试不存在的键
		values, err = Redis.HGetList(ctx, "non_existent_hash")
		assert.NoError(t, err)
		assert.Empty(t, values)

		// 清理测试数据
		Redis.client.Del(ctx, "user:3")
	})
}

func TestRedisListOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("LPush and RPop", func(t *testing.T) {
		// 测试推入元素
		err := Redis.LPush(ctx, "list", "first", "second", "third")
		assert.NoError(t, err)

		// 测试弹出元素
		value, err := Redis.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "first", value)

		value, err = Redis.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "second", value)

		value, err = Redis.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "third", value)

		// 测试空列表
		value, err = Redis.RPop(ctx, "list")
		assert.Error(t, err)
		assert.Empty(t, value)

		// 清理测试数据
		Redis.client.Del(ctx, "list")
	})
}

func TestRedisLock(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Lock and Unlock", func(t *testing.T) {
		// 测试获取锁
		locked, err := Redis.Lock(ctx, "test_lock", "process1", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 测试重复获取锁
		locked, err = Redis.Lock(ctx, "test_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.False(t, locked)

		// 测试解锁
		err = Redis.Unlock(ctx, "test_lock", "process1")
		assert.NoError(t, err)

		// 测试锁已释放
		locked, err = Redis.Lock(ctx, "test_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 清理测试数据
		Redis.client.Del(ctx, "test_lock")
	})

	t.Run("Lock Expiration", func(t *testing.T) {
		// 测试锁过期
		locked, err := Redis.Lock(ctx, "expire_lock", "process1", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 等待锁过期
		time.Sleep(2 * time.Second)

		// 测试可以重新获取锁
		locked, err = Redis.Lock(ctx, "expire_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 清理测试数据
		Redis.client.Del(ctx, "expire_lock")
	})
}

// TestRedisWithDifferentLogFormats 测试不同日志格式的Redis操作
func TestRedisWithDifferentLogFormats(t *testing.T) {
	t.Run("Default Log Format", func(t *testing.T) {
		testRedisWithLogFormat(t, "default", DefaultLogFormatter)
	})

	t.Run("JSON Log Format", func(t *testing.T) {
		testRedisWithLogFormat(t, "json", JSONLogFormatter)
	})

	t.Run("Custom Log Format", func(t *testing.T) {
		customFormatter := func(level LogLevel, message string) string {
			return fmt.Sprintf("CUSTOM[%s] %s", level.String(), message)
		}
		testRedisWithLogFormat(t, "custom", customFormatter)
	})
}

// testRedisWithLogFormat 使用指定日志格式测试Redis操作
func testRedisWithLogFormat(t *testing.T, formatName string, formatter LogFormatter) {
	// 创建日志记录器
	logger, err := NewRedisLogger(
		WithLogFilePath(fmt.Sprintf("./testdata/redis_%s_test.log", formatName)),
		WithLogLevel(LogLevelInfo),
		WithLogFormatter(formatter),
	)
	assert.NoError(t, err)
	assert.NotNil(t, logger)

	// 为日志测试创建新的Redis客户端（因为需要特殊的日志配置）
	err = InitRedis(
		WithAddrs("localhost:6379"),
		WithPassword(""),
		WithDB(1), // 使用不同的数据库避免冲突
		WithPoolSize(5),
		WithMinIdleConns(2),
		WithTimeout(time.Second, time.Second, time.Second),
		WithMaxRetries(2),
		WithLogging(logger),
	)
	if err != nil {
		t.Skipf("跳过%s格式测试：Redis连接失败: %v", formatName, err)
		return
	}
	defer Redis.Close()

	ctx := context.Background()

	// 执行一些Redis操作来测试日志记录
	testKey := fmt.Sprintf("test_log_%s", formatName)

	// 测试SET操作
	err = Redis.Set(ctx, testKey, "test_value", time.Minute)
	assert.NoError(t, err)

	// 测试GET操作
	value, err := Redis.Get(ctx, testKey)
	assert.NoError(t, err)
	assert.Equal(t, "test_value", value)

	// 测试HSET操作
	err = Redis.HSet(ctx, fmt.Sprintf("hash_%s", testKey), "field1", "value1")
	assert.NoError(t, err)

	// 测试HGET操作
	hashValue, err := Redis.HGet(ctx, fmt.Sprintf("hash_%s", testKey), "field1")
	assert.NoError(t, err)
	assert.Equal(t, "value1", hashValue)

	// 测试INCR操作
	val, err := Redis.Incr(ctx, fmt.Sprintf("counter_%s", testKey))
	assert.NoError(t, err)
	assert.Equal(t, int64(1), val)

	// 清理测试数据
	Redis.Del(ctx, testKey, fmt.Sprintf("hash_%s", testKey), fmt.Sprintf("counter_%s", testKey))

	// 检查日志文件是否创建
	logFile := fmt.Sprintf("./testdata/redis_%s_test.log", formatName)
	_, err = os.Stat(logFile)
	assert.NoError(t, err, "日志文件应该被创建: %s", logFile)
}

// TestRedisLogLevels 测试不同日志级别的过滤
func TestRedisLogLevels(t *testing.T) {
	t.Run("Debug Level", func(t *testing.T) {
		testRedisWithLogLevel(t, LogLevelDebug)
	})

	t.Run("Info Level", func(t *testing.T) {
		testRedisWithLogLevel(t, LogLevelInfo)
	})

	t.Run("Warn Level", func(t *testing.T) {
		testRedisWithLogLevel(t, LogLevelWarn)
	})

	t.Run("Error Level", func(t *testing.T) {
		testRedisWithLogLevel(t, LogLevelError)
	})
}

// testRedisWithLogLevel 使用指定日志级别测试Redis操作
func testRedisWithLogLevel(t *testing.T, level LogLevel) {
	// 创建日志记录器
	logger, err := NewRedisLogger(
		WithLogFilePath(fmt.Sprintf("./testdata/redis_level_%s_test.log", level.String())),
		WithLogLevel(level),
		WithLogFormatter(DefaultLogFormatter),
	)
	assert.NoError(t, err)
	assert.NotNil(t, logger)

	// 为日志级别测试创建新的Redis客户端
	err = InitRedis(
		WithAddrs("localhost:6379"),
		WithPassword(""),
		WithDB(2), // 使用不同的数据库避免冲突
		WithPoolSize(5),
		WithMinIdleConns(2),
		WithTimeout(time.Second, time.Second, time.Second),
		WithMaxRetries(2),
		WithLogging(logger),
	)
	if err != nil {
		t.Skipf("跳过%s级别测试：Redis连接失败: %v", level.String(), err)
		return
	}
	defer Redis.Close()

	ctx := context.Background()

	// 执行一些Redis操作
	testKey := fmt.Sprintf("test_level_%s", level.String())

	// 测试基本操作
	err = Redis.Set(ctx, testKey, "test_value", time.Minute)
	assert.NoError(t, err)

	value, err := Redis.Get(ctx, testKey)
	assert.NoError(t, err)
	assert.Equal(t, "test_value", value)

	// 清理测试数据
	Redis.Del(ctx, testKey)

	// 检查日志文件是否创建
	logFile := fmt.Sprintf("./testdata/redis_level_%s_test.log", level.String())
	_, err = os.Stat(logFile)
	assert.NoError(t, err, "日志文件应该被创建: %s", logFile)
}

// TestRedisLogContent 测试日志内容格式
func TestRedisLogContent(t *testing.T) {
	// 清理测试文件
	testLogFile := "./testdata/redis_content_test.log"
	os.Remove(testLogFile)

	// 创建JSON格式日志记录器
	logger, err := NewRedisLogger(
		WithLogFilePath(testLogFile),
		WithLogLevel(LogLevelInfo),
		WithLogFormatter(JSONLogFormatter),
	)
	assert.NoError(t, err)

	// 为日志内容测试创建新的Redis客户端
	err = InitRedis(
		WithAddrs("localhost:6379"),
		WithPassword(""),
		WithDB(3), // 使用不同的数据库避免冲突
		WithPoolSize(5),
		WithMinIdleConns(2),
		WithTimeout(time.Second, time.Second, time.Second),
		WithMaxRetries(2),
		WithLogging(logger),
	)
	if err != nil {
		t.Skipf("跳过日志内容测试：Redis连接失败: %v", err)
		return
	}
	defer Redis.Close()

	ctx := context.Background()

	// 执行一些Redis操作
	err = Redis.Set(ctx, "content_test_key", "content_test_value", time.Minute)
	assert.NoError(t, err)

	value, err := Redis.Get(ctx, "content_test_key")
	assert.NoError(t, err)
	assert.Equal(t, "content_test_value", value)

	// 清理测试数据
	Redis.Del(ctx, "content_test_key")

	// 检查日志文件内容
	content, err := os.ReadFile(testLogFile)
	assert.NoError(t, err)
	assert.NotEmpty(t, content, "日志文件应该包含内容")

	// 验证JSON格式
	logContent := string(content)
	assert.Contains(t, logContent, `"level":"INFO"`, "日志应该包含INFO级别")
	assert.Contains(t, logContent, `"message"`, "日志应该包含message字段")
	assert.Contains(t, logContent, `"timestamp"`, "日志应该包含timestamp字段")
	assert.Contains(t, logContent, "set", "日志应该包含SET命令")
	assert.Contains(t, logContent, "get", "日志应该包含GET命令")
}
