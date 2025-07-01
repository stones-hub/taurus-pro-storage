package db

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/natefinch/lumberjack"
	"gorm.io/gorm/logger"
)

// CustomLogger 定义日志创建接口
type DbLoggerInterface interface {
	// Build 创建新的日志记录器
	Build() (logger.Interface, error)
}

var loggerRegistry = make(map[string]DbLoggerInterface)

func RegisterLogger(name string, logger DbLoggerInterface) error {
	if _, ok := loggerRegistry[name]; ok {
		return fmt.Errorf("logger %s already registered", name)
	}

	loggerRegistry[name] = logger
	return nil
}

func GetLogger(name string) DbLoggerInterface {
	if logger, ok := loggerRegistry[name]; ok {
		return logger
	}
	return &DefaultLogger{
		MaxSize:       100,
		MaxBackups:    10,
		MaxAge:        30,
		Compress:      true,
		Level:         logger.Info,
		SlowThreshold: 200 * time.Millisecond,
	}
}

// Options 定义日志配置选项
type DefaultLogger struct {
	FilePath      string          // 日志文件路径（为空时输出到控制台）
	MaxSize       int             // 单个日志文件的最大大小（单位：MB）
	MaxBackups    int             // 保留的旧日志文件的最大数量
	MaxAge        int             // 日志文件的最大保存天数
	Compress      bool            // 是否压缩旧日志文件
	Level         logger.LogLevel // 日志等级  Silent Error Warn Info
	SlowThreshold time.Duration   // 慢查询阈值
}

type defaultLoggerOption func(*DefaultLogger)

// WithFilePath 设置日志文件路径
func WithFilePath(filePath string) defaultLoggerOption {
	return func(l *DefaultLogger) {
		l.FilePath = filePath
	}
}

// WithMaxSize 设置单个日志文件的最大大小
func WithMaxSize(maxSize int) defaultLoggerOption {
	return func(l *DefaultLogger) {
		l.MaxSize = maxSize
	}
}

// WithMaxBackups 设置保留的旧日志文件的最大数量
func WithMaxBackups(maxBackups int) defaultLoggerOption {
	return func(l *DefaultLogger) {
		l.MaxBackups = maxBackups
	}
}

// WithMaxAge 设置日志文件的最大保存天数
func WithMaxAge(maxAge int) defaultLoggerOption {
	return func(l *DefaultLogger) {
		l.MaxAge = maxAge
	}
}

// WithCompress 设置是否压缩旧日志文件
func WithCompress(compress bool) defaultLoggerOption {
	return func(l *DefaultLogger) {
		l.Compress = compress
	}
}

// WithLevel 设置日志等级
func WithLevel(level logger.LogLevel) defaultLoggerOption {
	return func(l *DefaultLogger) {
		l.Level = level
	}
}

// WithSlowThreshold 设置慢查询阈值
func WithSlowThreshold(slowThreshold time.Duration) defaultLoggerOption {
	return func(l *DefaultLogger) {
		l.SlowThreshold = slowThreshold
	}
}

func NewDefaultLogger(opts ...defaultLoggerOption) *DefaultLogger {
	logger := &DefaultLogger{
		MaxSize:       100,
		MaxBackups:    10,
		MaxAge:        30,
		Compress:      true,
		Level:         logger.Info,
		SlowThreshold: 200 * time.Millisecond,
	}

	for _, opt := range opts {
		opt(logger)
	}

	return logger
}

// Build 实现 Logger 接口
func (opt *DefaultLogger) Build() (logger.Interface, error) {
	var writer *log.Logger

	if opt.FilePath == "" {
		// 使用控制台输出
		writer = log.New(os.Stdout, "\r\n", log.LstdFlags)
		return logger.New(
			writer,
			logger.Config{
				SlowThreshold:             opt.SlowThreshold,
				LogLevel:                  opt.Level,
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
			},
		), nil
	}

	// 处理日志文件路径
	logFilePath := opt.FilePath
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
		MaxSize:    opt.MaxSize,
		MaxBackups: opt.MaxBackups,
		MaxAge:     opt.MaxAge,
		Compress:   opt.Compress,
	}, "\r\n", log.LstdFlags)

	return logger.New(
		writer,
		logger.Config{
			SlowThreshold:             opt.SlowThreshold,
			LogLevel:                  opt.Level,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	), nil
}
