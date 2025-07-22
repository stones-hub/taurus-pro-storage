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

package db

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/natefinch/lumberjack"
	"gorm.io/gorm/logger"
)

// DbLogger 数据库日志记录器，直接实现GORM的logger.Interface接口
type DbLogger struct {
	writer                    *log.Logger
	level                     logger.LogLevel
	slowThreshold             time.Duration
	ignoreRecordNotFoundError bool
	colorful                  bool
	formatter                 LogFormatter
}

// LogFormatter 日志格式化函数类型
type LogFormatter func(level logger.LogLevel, message string) string

// LogLevelToString 将logger.LogLevel转换为字符串
func LogLevelToString(level logger.LogLevel) string {
	switch level {
	case logger.Silent:
		return "SILENT"
	case logger.Error:
		return "ERROR"
	case logger.Warn:
		return "WARN"
	case logger.Info:
		return "INFO"
	default:
		return "UNKNOWN"
	}
}

// logLevelToString 将logger.LogLevel转换为字符串（私有版本，内部使用）
func logLevelToString(level logger.LogLevel) string {
	return LogLevelToString(level)
}

// DefaultLogFormatter 默认日志格式化函数
func DefaultLogFormatter(level logger.LogLevel, message string) string {
	return fmt.Sprintf("[%s] %s", logLevelToString(level), message)
}

// JSONLogFormatter JSON格式的日志格式化函数
func JSONLogFormatter(level logger.LogLevel, message string) string {
	return fmt.Sprintf(`{"level":"%s","message":"%s","timestamp":"%s","service":"database"}`,
		logLevelToString(level), message, time.Now().Format(time.DateTime))
}

// DbLoggerOption 配置选项函数类型
type DbLoggerOption func(*DbLogger)

// WithLogFilePath 设置日志文件路径
func WithLogFilePath(filePath string) DbLoggerOption {
	return func(l *DbLogger) {
		if filePath == "" {
			// 使用控制台输出
			l.writer = log.New(os.Stdout, "", log.LstdFlags)
		} else {
			// 处理日志文件路径
			if !filepath.IsAbs(filePath) {
				baseDIR, err := os.Getwd()
				if err == nil {
					filePath = filepath.Join(baseDIR, filePath)
				}
			}

			// 确保日志目录存在
			logDir := filepath.Dir(filePath)
			os.MkdirAll(logDir, os.ModePerm)

			// 配置日志轮转
			l.writer = log.New(&lumberjack.Logger{
				Filename:   filePath,
				MaxSize:    100, // 100MB
				MaxBackups: 10,
				MaxAge:     30, // 30天
				Compress:   true,
			}, "", log.LstdFlags)
		}
	}
}

// WithLogLevel 设置日志级别
func WithLogLevel(level logger.LogLevel) DbLoggerOption {
	return func(l *DbLogger) {
		l.level = level
	}
}

// WithSlowThreshold 设置慢查询阈值
func WithSlowThreshold(threshold time.Duration) DbLoggerOption {
	return func(l *DbLogger) {
		l.slowThreshold = threshold
	}
}

// WithIgnoreRecordNotFoundError 设置是否忽略记录未找到错误
func WithIgnoreRecordNotFoundError(ignore bool) DbLoggerOption {
	return func(l *DbLogger) {
		l.ignoreRecordNotFoundError = ignore
	}
}

// WithColorful 设置是否使用彩色输出
func WithColorful(colorful bool) DbLoggerOption {
	return func(l *DbLogger) {
		l.colorful = colorful
	}
}

// WithLogFormatter 设置日志格式化函数
func WithLogFormatter(formatter LogFormatter) DbLoggerOption {
	return func(l *DbLogger) {
		l.formatter = formatter
	}
}

// NewDbLogger 创建新的数据库日志记录器
func NewDbLogger(opts ...DbLoggerOption) *DbLogger {
	l := &DbLogger{
		writer:                    log.New(os.Stdout, "", log.LstdFlags), // 默认输出到控制台
		level:                     logger.Info,
		slowThreshold:             200 * time.Millisecond,
		ignoreRecordNotFoundError: true,
		colorful:                  false,
		formatter:                 DefaultLogFormatter,
	}

	// 应用自定义配置
	for _, opt := range opts {
		opt(l)
	}

	return l
}

// LogMode 实现gorm.io/gorm/logger.Interface接口
func (l *DbLogger) LogMode(level logger.LogLevel) logger.Interface {
	newLogger := *l
	newLogger.level = level
	return &newLogger
}

// Info 实现gorm.io/gorm/logger.Interface接口
func (l *DbLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.level >= logger.Info {
		message := fmt.Sprintf(msg, data...)
		formattedMessage := l.formatter(logger.Info, message)
		// l.writer.Print(formattedMessage)
		l.writer.Writer().Write([]byte(formattedMessage))
	}
}

// Warn 实现gorm.io/gorm/logger.Interface接口
func (l *DbLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.level >= logger.Warn {
		message := fmt.Sprintf(msg, data...)
		formattedMessage := l.formatter(logger.Warn, message)
		// l.writer.Print(formattedMessage)
		l.writer.Writer().Write([]byte(formattedMessage))
	}
}

// Error 实现gorm.io/gorm/logger.Interface接口
func (l *DbLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.level >= logger.Error {
		message := fmt.Sprintf(msg, data...)
		formattedMessage := l.formatter(logger.Error, message)
		// l.writer.Print(formattedMessage)
		l.writer.Writer().Write([]byte(formattedMessage))
	}
}

// Trace 实现gorm.io/gorm/logger.Interface接口
func (l *DbLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.level <= logger.Silent {
		return
	}

	elapsed := time.Since(begin)
	sql, rows := fc()

	// 根据执行时间和错误情况决定日志级别
	var level logger.LogLevel
	var message string

	switch {
	case err != nil && l.level >= logger.Error:
		level = logger.Error
		message = fmt.Sprintf("[%.3fms] [rows:%v] %s; %v", float64(elapsed.Nanoseconds())/1e6, rows, sql, err)
	case elapsed > l.slowThreshold && l.level >= logger.Warn:
		level = logger.Warn
		message = fmt.Sprintf("[%.3fms] [rows:%v] %s; slow sql >= %v", float64(elapsed.Nanoseconds())/1e6, rows, sql, l.slowThreshold)
	case l.level >= logger.Info:
		level = logger.Info
		message = fmt.Sprintf("[%.3fms] [rows:%v] %s", float64(elapsed.Nanoseconds())/1e6, rows, sql)
	default:
		return
	}

	formattedMessage := l.formatter(level, message)
	// l.writer.Print(formattedMessage)
	l.writer.Writer().Write([]byte(formattedMessage))
}

// 确保DbLogger实现了gorm.io/gorm/logger.Interface接口, 只是用于检测DbLogger是否实现了gorm.io/gorm/logger.Interface接口, 虽然我觉得我们已经实现了，但是还是需要检测一下，严谨
var _ logger.Interface = (*DbLogger)(nil)
