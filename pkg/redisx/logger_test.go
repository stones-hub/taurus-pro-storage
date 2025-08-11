package redisx

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestRedisLogger(t *testing.T) {
	// 清理测试文件
	testLogFile := "./testdata/redis_test.log"
	os.Remove(testLogFile)
	os.MkdirAll("./testdata", 0755)

	t.Run("Create Logger with File", func(t *testing.T) {
		logger, err := NewRedisLogger(
			WithLogFilePath(testLogFile),
			WithLogMaxSize(1),
			WithLogMaxBackups(1),
			WithLogMaxAge(1),
			WithLogCompress(false),
			WithLogLevel(LogLevelDebug),
		)
		assert.NoError(t, err)
		assert.NotNil(t, logger)

		// 测试日志记录
		logger.log(LogLevelInfo, "Test info message")
		logger.log(LogLevelError, "Test error message")

		// 检查日志文件是否创建
		_, err = os.Stat(testLogFile)
		assert.NoError(t, err)
	})

	t.Run("Create Logger with Console", func(t *testing.T) {
		logger, err := NewRedisLogger(
			WithLogFilePath(""),
			WithLogLevel(LogLevelInfo),
		)
		assert.NoError(t, err)
		assert.NotNil(t, logger)

		// 测试日志记录
		logger.log(LogLevelInfo, "Console test message")
	})

	t.Run("Log Level Filtering", func(t *testing.T) {
		logger, err := NewRedisLogger(
			WithLogFilePath(testLogFile),
			WithLogLevel(LogLevelWarn),
		)
		assert.NoError(t, err)

		// 这些应该被过滤掉
		logger.log(LogLevelDebug, "Debug message")
		logger.log(LogLevelInfo, "Info message")

		// 这些应该被记录
		logger.log(LogLevelWarn, "Warn message")
		logger.log(LogLevelError, "Error message")
	})
}

func TestRedisLogHook(t *testing.T) {
	// 清理测试文件
	testLogFile := "./testdata/redis_hook_test.log"
	os.Remove(testLogFile)
	os.MkdirAll("./testdata", 0755)

	logger, err := NewRedisLogger(
		WithLogFilePath(testLogFile),
		WithLogLevel(LogLevelDebug),
	)
	assert.NoError(t, err)

	hook := NewRedisLogHook(logger)
	assert.NotNil(t, hook)

	// 测试Hook实现了redis.Hook接口
	var _ redis.Hook = hook
}

func TestRedisWithLogging(t *testing.T) {
	// 清理测试文件
	testLogFile := "./testdata/redis_integration_test.log"
	os.Remove(testLogFile)
	os.MkdirAll("./testdata", 0755)

	// 创建日志记录器
	logger, err := NewRedisLogger(
		WithLogFilePath(testLogFile),
		WithLogLevel(LogLevelInfo),
	)
	assert.NoError(t, err)

	// 检查全局Redis客户端是否可用
	if Redis == nil {
		t.Skipf("跳过Redis集成测试：Redis客户端未初始化")
		return
	}

	// 为当前测试创建一个临时的Redis客户端
	err = InitRedis(
		WithAddrs("localhost:6379"),
		WithPassword(""),
		WithDB(0),
		WithLogging(logger),
	)
	if err != nil {
		t.Skipf("跳过Redis集成测试：Redis连接失败: %v", err)
		return
	}

	// 保存原始的Redis客户端
	originalRedis := Redis

	// 测试结束后恢复原始客户端
	defer func() {
		if Redis != nil && Redis != originalRedis {
			Redis.Close()
		}
		Redis = originalRedis
	}()

	// 执行一些Redis操作来测试日志
	ctx := context.Background()

	// 测试基本操作
	err = Redis.Set(ctx, "test_log_key", "test_value", time.Minute)
	assert.NoError(t, err)

	value, err := Redis.Get(ctx, "test_log_key")
	assert.NoError(t, err)
	assert.Equal(t, "test_value", value)

	// 测试哈希操作
	err = Redis.HSet(ctx, "test_log_hash", "field1", "value1")
	assert.NoError(t, err)

	hashValue, err := Redis.HGet(ctx, "test_log_hash", "field1")
	assert.NoError(t, err)
	assert.Equal(t, "value1", hashValue)

	// 清理测试数据
	Redis.Del(ctx, "test_log_key", "test_log_hash")

	// 检查日志文件是否包含操作记录
	_, err = os.Stat(testLogFile)
	assert.NoError(t, err)
}

func TestLogLevelString(t *testing.T) {
	assert.Equal(t, "DEBUG", LogLevelDebug.String())
	assert.Equal(t, "INFO", LogLevelInfo.String())
	assert.Equal(t, "WARN", LogLevelWarn.String())
	assert.Equal(t, "ERROR", LogLevelError.String())
}

func TestLogFormatters(t *testing.T) {
	t.Run("DefaultLogFormatter", func(t *testing.T) {
		result := DefaultLogFormatter(LogLevelInfo, "test message")
		// 检查格式是否包含必要的信息
		assert.Contains(t, result, "[INFO]")
		assert.Contains(t, result, "test message")
	})

	t.Run("JSONLogFormatter", func(t *testing.T) {
		result := JSONLogFormatter(LogLevelError, "error message")
		// 检查JSON格式是否正确
		assert.Contains(t, result, `"level":"ERROR"`)
		assert.Contains(t, result, `"message":"error message"`)
		assert.Contains(t, result, `"timestamp"`)
	})

}

func TestCustomLogFormatter(t *testing.T) {
	// 清理测试文件
	testLogFile := "./testdata/custom_formatter_test.log"
	os.Remove(testLogFile)
	os.MkdirAll("./testdata", 0755)

	// 自定义格式化函数
	customFormatter := func(level LogLevel, message string) string {
		return fmt.Sprintf("CUSTOM[%s] %s at %s", level.String(), message, time.Now().Format("15:04:05"))
	}

	logger, err := NewRedisLogger(
		WithLogFilePath(testLogFile),
		WithLogLevel(LogLevelInfo),
		WithLogFormatter(customFormatter),
	)
	assert.NoError(t, err)
	assert.NotNil(t, logger)

	// 测试自定义格式化
	logger.log(LogLevelInfo, "custom formatted message")

	// 检查日志文件是否创建
	_, err = os.Stat(testLogFile)
	assert.NoError(t, err)
}
