// Package scanx 提供与 Redis 客户端无关的 SCAN 结果去重计数，便于单元测试。
package scanx

// Fn 是一次 SCAN 调用
type Fn func(cursor uint64) (keys []string, next uint64, err error)

// CountUnique 遍历完整 SCAN 并按键名去重计数：SCAN 允许在迭代过程中重复返回同一个键。
func CountUnique(scan Fn) (int64, error) {
	seen := map[string]struct{}{}
	var cursor uint64
	for {
		keys, next, err := scan(cursor)
		if err != nil {
			return 0, err
		}
		for _, k := range keys {
			seen[k] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			return int64(len(seen)), nil
		}
	}
}
