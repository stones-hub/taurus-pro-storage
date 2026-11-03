package ledger

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 合成载荷：  TPS1|<id>|<size>|<确定性填充>
// Processor 只收到载荷字节，所以任务ID与长度写在头部；填充由 id 派生，
// 任何进程都能据此重新生成并逐字节核对，不需要共享内存。

const payloadMagic = "TPS1"

// MakePayload 生成指定长度的合成载荷（size 必须足以容纳头部）
func MakePayload(id string, size int) ([]byte, error) {
	hdr := payloadMagic + "|" + id + "|" + strconv.Itoa(size) + "|"
	if size < len(hdr) {
		return nil, fmt.Errorf("payload size %d smaller than header %d", size, len(hdr))
	}
	out := make([]byte, 0, size)
	out = append(out, hdr...)
	seed := sha256.Sum256([]byte(id))
	for len(out) < size {
		out = append(out, seed[:]...)
		seed = sha256.Sum256(seed[:])
	}
	return out[:size], nil
}

// ParsePayload 从载荷头部取出ID与声明的长度
func ParsePayload(data []byte) (id string, size int, err error) {
	parts := strings.SplitN(string(data[:min(len(data), 300)]), "|", 4)
	if len(parts) < 4 || parts[0] != payloadMagic {
		return "", 0, errors.New("not a synthetic payload")
	}
	size, err = strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, fmt.Errorf("bad size: %w", err)
	}
	return parts[1], size, nil
}

// VerifyPayload 解析并重新生成，逐字节比较；返回载荷中的ID。
func VerifyPayload(data []byte) (id string, ok bool) {
	id, size, err := ParsePayload(data)
	if err != nil || size != len(data) {
		return id, false
	}
	want, err := MakePayload(id, size)
	if err != nil {
		return id, false
	}
	return id, bytes.Equal(want, data)
}
