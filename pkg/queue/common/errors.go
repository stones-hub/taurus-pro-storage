package common

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// RetryableError 表示可以重试的错误
type RetryableError struct {
	err error
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("可重试错误: %v", e.err)
}

// NewRetryableError 创建一个可重试的错误
func NewRetryableError(err error) error {
	return &RetryableError{err: err}
}

// IsRetryableError 判断是否是可重试的错误
func IsRetryableError(err error) bool {
	_, ok := err.(*RetryableError)
	return ok
}

// ErrorableError 表示可放入失败队列的错误
type ErrorableError struct {
	err error
}

func (e *ErrorableError) Error() string {
	return fmt.Sprintf("可放入失败队列的错误: %v", e.err)
}

// NewErrorableError 创建一个错误
func NewErrorableError(err error) error {
	return &ErrorableError{err: err}
}

// IsErrorableError 判断是否是可放入失败队列的错误
func IsErrorableError(err error) bool {
	_, ok := err.(*ErrorableError)
	return ok
}

// PanicError 表示panic错误， 用于记录panic错误信息
type PanicError struct {
	err       error
	stack     string
	timestamp time.Time
	goroutine string
}

func (e *PanicError) Error() string {
	return e.formatError()
}

// formatError 格式化panic错误信息，提供多种输出格式
func (e *PanicError) formatError() string {
	var sb strings.Builder

	// 添加分隔线和标题
	sb.WriteString("╔══════════════════════════════════════════════════════════════════════════════════╗\n")
	sb.WriteString("║                                    PANIC ERROR                                   ║\n")
	sb.WriteString("╚══════════════════════════════════════════════════════════════════════════════════╝\n\n")

	// 基本信息
	sb.WriteString("📅 时间: ")
	sb.WriteString(e.timestamp.Format("2006-01-02 15:04:05.000"))
	sb.WriteString("\n")

	if e.goroutine != "" {
		sb.WriteString("🧵 Goroutine: ")
		sb.WriteString(e.goroutine)
		sb.WriteString("\n")
	}

	sb.WriteString("💥 Panic信息: ")
	sb.WriteString(fmt.Sprintf("%v", e.err))
	sb.WriteString("\n\n")

	// Stack信息
	sb.WriteString("📚 调用栈:\n")
	sb.WriteString("┌─────────────────────────────────────────────────────────────────────────────────┐\n")

	// 格式化stack信息，添加行号和缩进
	lines := strings.Split(strings.TrimSpace(e.stack), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// 添加行号
		sb.WriteString(fmt.Sprintf("│ %2d │ %s\n", i+1, line))
	}

	sb.WriteString("└─────────────────────────────────────────────────────────────────────────────────┘\n")

	return sb.String()
}

// formatErrorSimple 简单格式，适合日志输出
func (e *PanicError) formatErrorSimple() string {
	return fmt.Sprintf("[PANIC] %s | Goroutine: %s | Error: %v | Stack: %s",
		e.timestamp.Format("2006-01-02 15:04:05.000"),
		e.goroutine,
		e.err,
		strings.ReplaceAll(e.stack, "\n", " | "))
}

// formatErrorJSON JSON格式，适合结构化日志
func (e *PanicError) formatErrorJSON() string {
	stackLines := strings.Split(strings.TrimSpace(e.stack), "\n")
	return fmt.Sprintf(`{"timestamp":"%s","goroutine":"%s","panic":"%v","stack":[%s]}`,
		e.timestamp.Format(time.RFC3339Nano),
		e.goroutine,
		e.err,
		strings.Join(stackLines, ","))
}

// NewPanicError 创建一个新的PanicError
func NewPanicError(panic interface{}, stack string) *PanicError {
	return &PanicError{
		err:       fmt.Errorf("%v", panic),
		stack:     stack,
		timestamp: time.Now(),
		goroutine: fmt.Sprintf("G%d", runtime.NumGoroutine()),
	}
}

// NewPanicErrorWithGoroutine 创建一个带指定goroutine ID的PanicError
func NewPanicErrorWithGoroutine(panic interface{}, stack string, goroutineID string) *PanicError {
	return &PanicError{
		err:       fmt.Errorf("%v", panic),
		stack:     stack,
		timestamp: time.Now(),
		goroutine: goroutineID,
	}
}

// FormatAsSimple 返回简单格式的错误信息
func (e *PanicError) FormatAsSimple() string {
	return e.formatErrorSimple()
}

// FormatAsJSON 返回JSON格式的错误信息
func (e *PanicError) FormatAsJSON() string {
	return e.formatErrorJSON()
}

// FormatAsPretty 返回美化格式的错误信息（默认格式）
func (e *PanicError) FormatAsPretty() string {
	return e.formatError()
}

// GetPanicInfo 获取panic的详细信息，用于程序化处理
func (e *PanicError) GetPanicInfo() map[string]interface{} {
	stackLines := strings.Split(strings.TrimSpace(e.stack), "\n")
	return map[string]interface{}{
		"timestamp":   e.timestamp,
		"goroutine":   e.goroutine,
		"panic":       e.err.Error(),
		"stack":       stackLines,
		"stack_count": len(stackLines),
	}
}

var (
	// ================================ 队列错误 ================================
	// ErrQueueEmpty 队列为空的错误
	ErrQueueEmpty = errors.New("队列数据为空")
	// ErrQueueFull 队列已满错误
	ErrQueueFull = errors.New("队列已满")
	// ErrQueueNotExist 队列不存在错误
	ErrQueueNotExist = errors.New("队列不存在")

	// ================================ 数据项错误 ================================
	// ErrItemEmpty 数据项为空错误
	ErrItemEmpty = errors.New("队列中的数据项不能为空")
	// ErrRetryCountNegative 重试次数为负数错误
	ErrItemRetryCountNegative = errors.New("队列中的数据项已重试的次数不能为负数")
	// ErrValidateFailed 验证失败错误
	ErrItemValidateFailed = errors.New("队列中的数据项验证失败，请及时检查队列是否正常")
	// ErrItemJsonUnmarshalFailed 数据项JSON解析失败错误
	ErrItemJsonUnmarshalFailed = errors.New("队列中的数据项JSON解析失败，请及时检查队列是否正常")
	// ErrItemJsonMarshalFailed 数据项JSON序列化失败错误
	ErrItemJsonMarshalFailed = errors.New("队列中的数据项JSON序列化失败，请及时检查队列是否正常")

	// ================================ 队列操作错误 ================================
	// ErrPopFromSourceQueue 从源队列读取数据失败错误
	ErrPopFromSourceQueue = errors.New("从源队列读取数据失败，请及时检查队列是否正常")
	// ErrPopFromProcessingQueue 从处理中队列读取数据失败错误
	ErrPopFromProcessingQueue = errors.New("从处理中队列读取数据失败，请及时检查队列是否正常")
	// ErrPopFromRetryQueue 从重试队列读取数据失败错误
	ErrPopFromRetryQueue = errors.New("从重试队列读取数据失败，请及时检查队列是否正常")
	// ErrPopFromFailedQueue 从失败队列读取数据失败错误
	ErrPopFromFailedQueue = errors.New("从失败队列读取数据失败，请及时检查队列是否正常")
	// ErrPushToFailedQueue 推送到失败队列失败错误
	ErrPushToFailedQueue = errors.New("推送到失败队列失败，请及时检查队列是否正常")
	// ErrPushToFailedQueueTimeout 推送到失败队列超时错误
	ErrPushToFailedQueueTimeout = errors.New("推送到失败队列超时，请及时检查队列是否正常")
	// ErrPushToRetryQueue 推送到重试队列失败错误
	ErrPushToRetryQueue = errors.New("推送到重试队列失败，请及时检查队列是否正常")
	// ErrPushToRetryQueueTimeout 推送到重试队列超时错误
	ErrPushToRetryQueueTimeout = errors.New("推送到重试队列超时，请及时检查队列是否正常")
	// ErrPushToProcessingQueue 推送到处理中队列失败错误
	ErrPushToProcessingQueue = errors.New("推送数据到处理中队列失败，请及时检查队列是否正常")
	// ErrPushToProcessingQueueTimeout 推送到处理中队列超时错误
	ErrPushToProcessingQueueTimeout = errors.New("推送到处理中队列超时，请及时检查队列是否正常")

	// ================================ 队列管理器错误 ================================
	// ErrManagerNotRunning 队列管理器未运行错误
	ErrManagerNotRunning = errors.New("队列管理器未运行")
	// ErrManagerAlreadyRunning 队列管理器已运行错误
	ErrManagerAlreadyRunning = errors.New("队列管理器已运行")

	// ErrContextCanceled 上下文取消错误
	ErrContextCanceled = errors.New("上下文取消")
	// ErrContextTimeout 上下文超时错误
	ErrContextTimeout = errors.New("上下文超时")
)
