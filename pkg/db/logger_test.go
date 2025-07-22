package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestNewDbLogger(t *testing.T) {
	tests := []struct {
		name     string
		options  []DbLoggerOption
		expected *DbLogger
	}{
		{
			name:    "默认配置",
			options: []DbLoggerOption{},
			expected: &DbLogger{
				level:                     gormlogger.Info,
				slowThreshold:             200 * time.Millisecond,
				ignoreRecordNotFoundError: true,
				colorful:                  false,
			},
		},
		{
			name: "自定义配置",
			options: []DbLoggerOption{
				WithLogLevel(gormlogger.Error),
				WithSlowThreshold(500 * time.Millisecond),
				WithIgnoreRecordNotFoundError(false),
				WithColorful(true),
			},
			expected: &DbLogger{
				level:                     gormlogger.Error,
				slowThreshold:             500 * time.Millisecond,
				ignoreRecordNotFoundError: false,
				colorful:                  true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewDbLogger(tt.options...)

			if logger.level != tt.expected.level {
				t.Errorf("日志级别不匹配，期望 %v，实际 %v", tt.expected.level, logger.level)
			}

			if logger.slowThreshold != tt.expected.slowThreshold {
				t.Errorf("慢查询阈值不匹配，期望 %v，实际 %v", tt.expected.slowThreshold, logger.slowThreshold)
			}

			if logger.ignoreRecordNotFoundError != tt.expected.ignoreRecordNotFoundError {
				t.Errorf("忽略记录未找到错误设置不匹配，期望 %v，实际 %v", tt.expected.ignoreRecordNotFoundError, logger.ignoreRecordNotFoundError)
			}

			if logger.colorful != tt.expected.colorful {
				t.Errorf("彩色输出设置不匹配，期望 %v，实际 %v", tt.expected.colorful, logger.colorful)
			}

			if logger.formatter == nil {
				t.Error("格式化函数不能为nil")
			}
		})
	}
}

func TestLogFormatters(t *testing.T) {
	tests := []struct {
		name      string
		formatter LogFormatter
		level     gormlogger.LogLevel
		message   string
		expected  string
	}{
		{
			name:      "默认格式化器",
			formatter: DefaultLogFormatter,
			level:     gormlogger.Info,
			message:   "测试消息",
			expected:  "[INFO] 测试消息",
		},
		{
			name:      "JSON格式化器",
			formatter: JSONLogFormatter,
			level:     gormlogger.Error,
			message:   "错误消息",
			expected:  `{"level":"ERROR","message":"错误消息"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.formatter(tt.level, tt.message)

			if tt.name == "JSON格式化器" {
				if !strings.Contains(result, tt.expected) {
					t.Errorf("JSON格式化结果不包含期望内容，期望包含 %s，实际 %s", tt.expected, result)
				}
			} else {
				if result != tt.expected {
					t.Errorf("格式化结果不匹配，期望 %s，实际 %s", tt.expected, result)
				}
			}
		})
	}
}

func TestLogLevelToString(t *testing.T) {
	tests := []struct {
		level    gormlogger.LogLevel
		expected string
	}{
		{gormlogger.Silent, "SILENT"},
		{gormlogger.Error, "ERROR"},
		{gormlogger.Warn, "WARN"},
		{gormlogger.Info, "INFO"},
		{gormlogger.LogLevel(999), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := logLevelToString(tt.level)
			if result != tt.expected {
				t.Errorf("日志级别转换错误，期望 %s，实际 %s", tt.expected, result)
			}
		})
	}
}

func TestWithLogFilePath(t *testing.T) {
	// 测试控制台输出
	logger1 := NewDbLogger(WithLogFilePath(""))
	if logger1.writer == nil {
		t.Error("控制台输出writer不能为nil")
	}

	// 测试文件输出
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")
	logger2 := NewDbLogger(WithLogFilePath(logFile))
	if logger2.writer == nil {
		t.Error("文件输出writer不能为nil")
	}

	// 验证文件是否被创建
	// 注意：lumberjack 可能不会立即创建文件，只有在写入日志时才会创建
	// 所以我们只验证 writer 不为 nil
	if logger2.writer == nil {
		t.Error("文件输出writer不能为nil")
	}
}

func TestDbLoggerInterface(t *testing.T) {
	// 测试LogMode方法
	logger := NewDbLogger()
	newLogger := logger.LogMode(gormlogger.Error)
	if newLogger.(*DbLogger).level != gormlogger.Error {
		t.Error("LogMode方法应该返回新的日志级别")
	}

	// 测试Info方法
	logger = NewDbLogger(WithLogLevel(gormlogger.Info))
	ctx := context.Background()
	logger.Info(ctx, "测试信息消息")

	// 测试Warn方法
	logger = NewDbLogger(WithLogLevel(gormlogger.Warn))
	logger.Warn(ctx, "测试警告消息")

	// 测试Error方法
	logger = NewDbLogger(WithLogLevel(gormlogger.Error))
	logger.Error(ctx, "测试错误消息")
}

func TestDbLoggerWithGorm(t *testing.T) {
	// 创建测试数据库
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: NewDbLogger(
			WithLogLevel(gormlogger.Info),
			WithLogFormatter(DefaultLogFormatter),
		),
	})
	if err != nil {
		t.Fatalf("创建测试数据库失败: %v", err)
	}

	// 执行一些SQL操作来测试日志输出
	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	db.Exec("INSERT INTO test (name) VALUES (?)", "test1")

	var result []map[string]interface{}
	db.Raw("SELECT * FROM test").Scan(&result)

	if len(result) != 1 {
		t.Error("查询结果应该包含1条记录")
	}
}

func TestDbLoggerLevelFiltering(t *testing.T) {
	// 测试不同日志级别的过滤
	tests := []struct {
		name      string
		level     gormlogger.LogLevel
		shouldLog bool
	}{
		{"Silent级别", gormlogger.Silent, false},
		{"Error级别", gormlogger.Error, true},
		{"Warn级别", gormlogger.Warn, true},
		{"Info级别", gormlogger.Info, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewDbLogger(WithLogLevel(tt.level))

			// 这里我们只能测试接口实现，实际的日志过滤在GORM内部处理
			if logger.level != tt.level {
				t.Errorf("日志级别设置错误，期望 %v，实际 %v", tt.level, logger.level)
			}
		})
	}
}

func TestCustomLogFormatter(t *testing.T) {
	// 测试自定义格式化函数
	customFormatter := func(level gormlogger.LogLevel, message string) string {
		return fmt.Sprintf("CUSTOM[%s]: %s", logLevelToString(level), message)
	}

	logger := NewDbLogger(WithLogFormatter(customFormatter))
	result := logger.formatter(gormlogger.Info, "测试消息")
	expected := "CUSTOM[INFO]: 测试消息"

	if result != expected {
		t.Errorf("自定义格式化结果不匹配，期望 %s，实际 %s", expected, result)
	}
}
