package queue

import "fmt"

// RetryableError 表示可以重试的错误
type RetryableError struct {
	err error
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %v", e.err)
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
