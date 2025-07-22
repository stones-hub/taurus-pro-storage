// Copyright (c) 2025 Taurus Team. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Author: yelei
// Email: 61647649@qq.com
// Date: 2025-07-22

package redisx

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/natefinch/lumberjack"
	"github.com/redis/go-redis/v9"
)

// LogFormatter 日志格式化函数类型
type LogFormatter func(level LogLevel, message string) string

// RedisLogger Redis日志记录器
type RedisLogger struct {
	logger    *log.Logger
	level     LogLevel
	formatter LogFormatter
}

// LogLevel 日志级别
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

// String 返回日志级别的字符串表示
func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "DEBUG"
	case LogLevelInfo:
		return "INFO"
	case LogLevelWarn:
		return "WARN"
	case LogLevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// RedisLoggerOptions Redis日志配置选项
type RedisLoggerOptions struct {
	FilePath   string       // 日志文件路径（为空时输出到控制台）
	MaxSize    int          // 单个日志文件的最大大小（单位：MB）
	MaxBackups int          // 保留的旧日志文件的最大数量
	MaxAge     int          // 日志文件的最大保存天数
	Compress   bool         // 是否压缩旧日志文件
	Level      LogLevel     // 日志级别
	Formatter  LogFormatter // 日志格式化函数
}

// RedisLoggerOption 定义配置选项函数类型
type RedisLoggerOption func(*RedisLoggerOptions)

// WithLogFilePath 设置日志文件路径
func WithLogFilePath(filePath string) RedisLoggerOption {
	return func(o *RedisLoggerOptions) {
		o.FilePath = filePath
	}
}

// WithLogMaxSize 设置单个日志文件的最大大小
func WithLogMaxSize(maxSize int) RedisLoggerOption {
	return func(o *RedisLoggerOptions) {
		o.MaxSize = maxSize
	}
}

// WithLogMaxBackups 设置保留的旧日志文件的最大数量
func WithLogMaxBackups(maxBackups int) RedisLoggerOption {
	return func(o *RedisLoggerOptions) {
		o.MaxBackups = maxBackups
	}
}

// WithLogMaxAge 设置日志文件的最大保存天数
func WithLogMaxAge(maxAge int) RedisLoggerOption {
	return func(o *RedisLoggerOptions) {
		o.MaxAge = maxAge
	}
}

// WithLogCompress 设置是否压缩旧日志文件
func WithLogCompress(compress bool) RedisLoggerOption {
	return func(o *RedisLoggerOptions) {
		o.Compress = compress
	}
}

// WithLogLevel 设置日志级别
func WithLogLevel(level LogLevel) RedisLoggerOption {
	return func(o *RedisLoggerOptions) {
		o.Level = level
	}
}

// WithLogFormatter 设置日志格式化函数
func WithLogFormatter(formatter LogFormatter) RedisLoggerOption {
	return func(o *RedisLoggerOptions) {
		o.Formatter = formatter
	}
}

// DefaultLogFormatter 默认日志格式化函数
func DefaultLogFormatter(level LogLevel, message string) string {
	return fmt.Sprintf("%s [%s] %s", time.Now().Format(time.DateTime), level.String(), message)
}

// JSONLogFormatter JSON格式的日志格式化函数
func JSONLogFormatter(level LogLevel, message string) string {
	return fmt.Sprintf(`{"level":"%s","message":"%s","timestamp":"%s"}`,
		level.String(), message, time.Now().Format(time.DateTime))
}

// DefaultRedisLoggerOptions 返回默认配置
func DefaultRedisLoggerOptions() *RedisLoggerOptions {
	return &RedisLoggerOptions{
		FilePath:   "./logs/redis/redis.log",
		MaxSize:    100,
		MaxBackups: 10,
		MaxAge:     30,
		Compress:   true,
		Level:      LogLevelInfo,
		Formatter:  DefaultLogFormatter,
	}
}

// NewRedisLogger 创建新的Redis日志记录器
func NewRedisLogger(opts ...RedisLoggerOption) (*RedisLogger, error) {
	options := DefaultRedisLoggerOptions()

	// 应用自定义配置
	for _, opt := range opts {
		opt(options)
	}

	var writer *log.Logger

	if options.FilePath == "" {
		// 使用控制台输出
		writer = log.New(os.Stdout, "", log.LstdFlags)
	} else {
		// 处理日志文件路径
		logFilePath := options.FilePath
		if !filepath.IsAbs(logFilePath) {
			baseDIR, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("failed to get current working directory: %v", err)
			}
			logFilePath = filepath.Join(baseDIR, logFilePath)
		}

		// 确保日志目录存在
		logDir := filepath.Dir(logFilePath)
		if err := os.MkdirAll(logDir, os.ModePerm); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %v", err)
		}

		// 配置日志轮转
		writer = log.New(&lumberjack.Logger{
			Filename:   logFilePath,
			MaxSize:    options.MaxSize,
			MaxBackups: options.MaxBackups,
			MaxAge:     options.MaxAge,
			Compress:   options.Compress,
		}, "", log.LstdFlags)
	}

	// 如果没有设置格式化函数，使用默认的
	if options.Formatter == nil {
		options.Formatter = DefaultLogFormatter
	}

	return &RedisLogger{
		logger:    writer,
		level:     options.Level,
		formatter: options.Formatter,
	}, nil
}

// log 记录日志
func (rl *RedisLogger) log(level LogLevel, format string, args ...interface{}) {
	if level >= rl.level {
		message := fmt.Sprintf(format, args...)
		formattedMessage := rl.formatter(level, message)
		// rl.logger.Print(formattedMessage)
		rl.logger.Writer().Write([]byte(formattedMessage + "\n"))
	}
}

// RedisLogHook Redis日志Hook实现
// 给redis添加hook，如果要实现hook的能力，必须实现redis.Hook接口
// hook接口定义了以下方法：
// DialHook: 连接Hook
// ProcessHook: 命令处理Hook
// ProcessPipelineHook: 管道命令处理Hook
type RedisLogHook struct {
	logger *RedisLogger
}

// NewRedisLogHook 创建新的Redis日志Hook
func NewRedisLogHook(logger *RedisLogger) *RedisLogHook {
	return &RedisLogHook{
		logger: logger,
	}
}

// DialHook 连接Hook
func (h *RedisLogHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// 记录连接开始
		h.logger.log(LogLevelDebug, "Redis dialing %s %s", network, addr)
		start := time.Now()

		// 调用下一个hook
		conn, err := next(ctx, network, addr)

		// 记录连接结束
		duration := time.Since(start)

		// 记录连接结果
		if err != nil {
			h.logger.log(LogLevelError, "Redis dial failed %s %s after %v: %v", network, addr, duration, err)
		} else {
			h.logger.log(LogLevelInfo, "Redis dialed %s %s in %v", network, addr, duration)
		}

		return conn, err
	}
}

// ProcessHook 命令处理Hook
func (h *RedisLogHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		start := time.Now()
		args := cmd.Args()

		// 记录命令开始
		h.logger.log(LogLevelDebug, "Redis command starting: %v", args)

		// 调用下一个hook
		err := next(ctx, cmd)

		// 记录命令结束
		duration := time.Since(start)

		// 根据结果记录不同级别的日志
		if err != nil {
			if err == redis.Nil {
				h.logger.log(LogLevelDebug, "Redis command completed (key not found): %v in %v", args, duration)
			} else {
				h.logger.log(LogLevelError, "Redis command failed: %v in %v, error: %v", args, duration, err)
			}
		} else {
			h.logger.log(LogLevelInfo, "Redis command completed: %v in %v", args, duration)
		}

		return err
	}
}

// ProcessPipelineHook 管道命令处理Hook
func (h *RedisLogHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		start := time.Now()

		// 收集所有命令的参数
		argsList := make([]interface{}, 0, len(cmds))
		for _, cmd := range cmds {
			argsList = append(argsList, cmd.Args())
		}

		// 记录管道命令开始
		h.logger.log(LogLevelDebug, "Redis pipeline starting with %d commands: %v", len(cmds), argsList)

		// 调用下一个hook
		err := next(ctx, cmds)

		// 记录管道命令结束
		duration := time.Since(start)

		// 记录管道命令结果
		if err != nil {
			h.logger.log(LogLevelError, "Redis pipeline failed with %d commands in %v, error: %v", len(cmds), duration, err)
		} else {
			h.logger.log(LogLevelInfo, "Redis pipeline completed with %d commands in %v", len(cmds), duration)
		}

		return err
	}
}

// 确保RedisLogHook实现了redis.Hook接口, 只是用于检测RedisLogHook是否实现了redis.Hook接口, 虽然我觉得我们已经实现了，但是还是需要检测一下，严谨
// 将nil强制转换为RedisLogHook类型， 赋值给 _ redis.Hook 变量， 确保RedisLogHook实现了redis.Hook接口
// 多用于检测代码是否实现了某个接口, 如果实现了, 则不会报错, 否则会报错, 又不会额外添加内存的消耗
var _ redis.Hook = (*RedisLogHook)(nil)
