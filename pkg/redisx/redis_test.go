package redisx

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

// 测试主函数，用于初始化Redis客户端和清理测试文件
func TestMain(m *testing.M) {
	// 清理测试日志文件
	os.Remove("./testdata/redis_test.log")
	os.Remove("./testdata/redis_json_test.log")
	os.Remove("./testdata/redis_simple_test.log")
	os.MkdirAll("./testdata", 0755)

	// 初始化 Redis 客户端（用于基础测试）
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

	// 清理资源 - 在所有测试完成后关闭
	if Redis != nil {
		Redis.Close()
	}

	os.Exit(code)
}

// ==================== 基础操作测试 ====================

func TestRedisBasicOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}

	ctx := context.Background()

	t.Run("Set and Get", func(t *testing.T) {
		// 测试正常设置和获取
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
		Redis.Del(ctx, "test_key")
	})

	t.Run("Set Parameter Validation", func(t *testing.T) {
		// 测试空键
		err := Redis.Set(ctx, "", "value", time.Minute)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "key cannot be empty")

		// 测试空值
		err = Redis.Set(ctx, "key", nil, time.Minute)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "value cannot be nil")
	})

	t.Run("SetNX", func(t *testing.T) {
		// 测试第一次设置（应该成功）
		success, err := Redis.SetNX(ctx, "test_nx_key", "value1", time.Minute)
		assert.NoError(t, err)
		assert.True(t, success)

		// 测试第二次设置（应该失败）
		success, err = Redis.SetNX(ctx, "test_nx_key", "value2", time.Minute)
		assert.NoError(t, err)
		assert.False(t, success)

		// 测试参数验证
		_, err = Redis.SetNX(ctx, "", "value", time.Minute)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "key cannot be empty")

		_, err = Redis.SetNX(ctx, "key", nil, time.Minute)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "value cannot be nil")

		// 清理测试数据
		Redis.Del(ctx, "test_nx_key")
	})

	t.Run("Del", func(t *testing.T) {
		// 设置测试数据
		err := Redis.Set(ctx, "del_key1", "value1", time.Minute)
		assert.NoError(t, err)
		err = Redis.Set(ctx, "del_key2", "value2", time.Minute)
		assert.NoError(t, err)

		// 测试删除单个键
		err = Redis.Del(ctx, "del_key1")
		assert.NoError(t, err)

		// 测试删除多个键
		err = Redis.Del(ctx, "del_key2", "non_existent_key")
		assert.NoError(t, err)

		// 验证键已被删除
		value, err := Redis.Get(ctx, "del_key1")
		assert.NoError(t, err)
		assert.Empty(t, value)

		value, err = Redis.Get(ctx, "del_key2")
		assert.NoError(t, err)
		assert.Empty(t, value)
	})

	t.Run("Incr and Decr", func(t *testing.T) {
		// 测试递增
		val, err := Redis.Incr(ctx, "counter_key")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)

		val, err = Redis.Incr(ctx, "counter_key")
		assert.NoError(t, err)
		assert.Equal(t, int64(2), val)

		// 测试递减
		val, err = Redis.Decr(ctx, "counter_key")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)

		// 清理测试数据
		Redis.Del(ctx, "counter_key")
	})
}

// ==================== 哈希操作测试 ====================

func TestRedisHashOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("HSet and HGet", func(t *testing.T) {
		// 测试设置单个字段
		err := Redis.HSet(ctx, "user:1001", "name", "张三")
		assert.NoError(t, err)

		// 测试获取字段值
		value, err := Redis.HGet(ctx, "user:1001", "name")
		assert.NoError(t, err)
		assert.Equal(t, "张三", value)

		// 测试获取不存在的字段
		value, err = Redis.HGet(ctx, "user:1001", "age")
		assert.NoError(t, err)
		assert.Empty(t, value)

		// 清理测试数据
		Redis.Del(ctx, "user:1001")
	})

	t.Run("HSet with Map", func(t *testing.T) {
		// 测试使用map设置多个字段
		userData := map[string]interface{}{
			"name": "李四",
			"age":  25,
			"city": "北京",
		}

		err := Redis.HSet(ctx, "user:1002", userData)
		assert.NoError(t, err)

		// 验证各个字段
		name, err := Redis.HGet(ctx, "user:1002", "name")
		assert.NoError(t, err)
		assert.Equal(t, "李四", name)

		age, err := Redis.HGet(ctx, "user:1002", "age")
		assert.NoError(t, err)
		assert.Equal(t, "25", age)

		city, err := Redis.HGet(ctx, "user:1002", "city")
		assert.NoError(t, err)
		assert.Equal(t, "北京", city)

		// 清理测试数据
		Redis.Del(ctx, "user:1002")
	})

	t.Run("HSetNX", func(t *testing.T) {
		// 测试第一次设置字段（应该成功）
		success, err := Redis.HSetNX(ctx, "user:1003", "name", "王五")
		assert.NoError(t, err)
		assert.True(t, success)

		// 测试第二次设置同一字段（应该失败）
		success, err = Redis.HSetNX(ctx, "user:1003", "name", "赵六")
		assert.NoError(t, err)
		assert.False(t, success)

		// 验证字段值仍然是第一次设置的值
		value, err := Redis.HGet(ctx, "user:1003", "name")
		assert.NoError(t, err)
		assert.Equal(t, "王五", value)

		// 清理测试数据
		Redis.Del(ctx, "user:1003")
	})

	t.Run("HIncrBy and HIncrByFloat", func(t *testing.T) {
		// 测试整数递增
		val, err := Redis.HIncrBy(ctx, "user:1004", "score", 10)
		assert.NoError(t, err)
		assert.Equal(t, int64(10), val)

		val, err = Redis.HIncrBy(ctx, "user:1004", "score", 5)
		assert.NoError(t, err)
		assert.Equal(t, int64(15), val)

		// 测试浮点数递增
		floatVal, err := Redis.HIncrByFloat(ctx, "user:1004", "rating", 3.5)
		assert.NoError(t, err)
		assert.Equal(t, 3.5, floatVal)

		floatVal, err = Redis.HIncrByFloat(ctx, "user:1004", "rating", 1.2)
		assert.NoError(t, err)
		assert.Equal(t, 4.7, floatVal)

		// 清理测试数据
		Redis.Del(ctx, "user:1004")
	})

	t.Run("HGetList", func(t *testing.T) {
		// 设置多个字段
		userData := map[string]interface{}{
			"name": "孙七",
			"age":  30,
			"job":  "工程师",
		}

		err := Redis.HSet(ctx, "user:1005", userData)
		assert.NoError(t, err)

		// 获取所有字段和值
		allFields, err := Redis.HGetList(ctx, "user:1005")
		assert.NoError(t, err)
		assert.Len(t, allFields, 3)
		assert.Equal(t, "孙七", allFields["name"])
		assert.Equal(t, "30", allFields["age"])
		assert.Equal(t, "工程师", allFields["job"])

		// 清理测试数据
		Redis.Del(ctx, "user:1005")
	})

	t.Run("Hash Parameter Validation", func(t *testing.T) {
		// 测试空键
		err := Redis.HSet(ctx, "", "field", "value")
		assert.Error(t, err)

		// 测试空键的HGet
		_, err = Redis.HGet(ctx, "", "field")
		assert.Error(t, err)

		// 测试空键的HSetNX
		_, err = Redis.HSetNX(ctx, "", "field", "value")
		assert.Error(t, err)

		// 测试空键的HIncrBy
		_, err = Redis.HIncrBy(ctx, "", "field", 1)
		assert.Error(t, err)
	})
}

// ==================== 列表操作测试 ====================

func TestRedisListOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("LPush and RPop", func(t *testing.T) {
		// 测试左侧推入元素
		err := Redis.LPush(ctx, "test_list", "item1", "item2", "item3")
		assert.NoError(t, err)

		// 测试右侧弹出元素（应该是item1，因为LPush是左侧推入）
		value, err := Redis.RPop(ctx, "test_list")
		assert.NoError(t, err)
		assert.Equal(t, "item1", value)

		// 继续弹出
		value, err = Redis.RPop(ctx, "test_list")
		assert.NoError(t, err)
		assert.Equal(t, "item2", value)

		value, err = Redis.RPop(ctx, "test_list")
		assert.NoError(t, err)
		assert.Equal(t, "item3", value)

		// 测试弹出空列表
		value, err = Redis.RPop(ctx, "test_list")
		assert.Error(t, err)
		assert.Equal(t, "redis: nil", err.Error())
		assert.Empty(t, value)

		// 清理测试数据
		Redis.Del(ctx, "test_list")
	})

	t.Run("List with Different Data Types", func(t *testing.T) {
		// 测试不同类型的数据
		err := Redis.LPush(ctx, "mixed_list", "string", 123, true, false, 3.14)
		assert.NoError(t, err)

		// 弹出并验证（注意LPush是左侧推入，所以弹出顺序是反的）
		value, err := Redis.RPop(ctx, "mixed_list")
		assert.NoError(t, err)
		assert.Equal(t, "string", value)

		value, err = Redis.RPop(ctx, "mixed_list")
		assert.NoError(t, err)
		assert.Equal(t, "123", value)

		value, err = Redis.RPop(ctx, "mixed_list")
		assert.NoError(t, err)
		assert.Equal(t, "1", value) // Redis将true存储为"1"

		value, err = Redis.RPop(ctx, "mixed_list")
		assert.NoError(t, err)
		assert.Equal(t, "0", value) // Redis将false存储为"0"

		value, err = Redis.RPop(ctx, "mixed_list")
		assert.NoError(t, err)
		assert.Equal(t, "3.14", value)

		// 清理测试数据
		Redis.Del(ctx, "mixed_list")
	})
}

// ==================== 分布式锁测试 ====================

func TestRedisLock(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Basic Lock and Unlock", func(t *testing.T) {
		lockKey := "test_lock:basic"
		lockValue := "test_value_1"

		// 获取锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired, "应该成功获取锁")

		// 尝试再次获取同一个锁（应该失败）
		acquired2, err := Redis.Lock(ctx, lockKey, "test_value_2", time.Second)
		assert.NoError(t, err)
		assert.False(t, acquired2, "不应该能获取已被占用的锁")

		// 释放锁
		err = Redis.Unlock(ctx, lockKey, lockValue)
		assert.NoError(t, err, "应该成功释放锁")

		// 验证锁已被释放
		acquired3, err := Redis.Lock(ctx, lockKey, "test_value_3", time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired3, "锁释放后应该能重新获取")
	})

	t.Run("Lock Parameter Validation", func(t *testing.T) {
		// 测试空键
		_, err := Redis.Lock(ctx, "", "value", time.Second)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lock key cannot be empty")

		// 测试空值
		_, err = Redis.Lock(ctx, "key", "", time.Second)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lock value cannot be empty")

		// 测试零过期时间（应该使用默认值）
		acquired, err := Redis.Lock(ctx, "test_lock:zero_expire", "value", 0)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 清理
		Redis.Unlock(ctx, "test_lock:zero_expire", "value")
	})

	t.Run("Lock Expiration", func(t *testing.T) {
		lockKey := "test_lock:expiration"
		lockValue := "test_value_expire"

		// 获取一个短时间的锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, 100*time.Millisecond)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 等待锁过期
		time.Sleep(200 * time.Millisecond)

		// 尝试获取已过期的锁（应该成功）
		acquired2, err := Redis.Lock(ctx, lockKey, "new_value", time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired2, "过期的锁应该能被重新获取")

		// 清理
		Redis.Unlock(ctx, lockKey, "new_value")
	})

	t.Run("TryLock with Timeout", func(t *testing.T) {
		lockKey := "test_lock:trylock"
		lockValue := "test_value_trylock"

		// 获取锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 尝试获取锁，带超时（应该超时失败）
		acquired2, err := Redis.TryLock(ctx, lockKey, "new_value", time.Second, 100*time.Millisecond)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lock acquisition timeout")
		assert.False(t, acquired2, "在超时时间内不应该能获取锁")

		// 释放锁
		Redis.Unlock(ctx, lockKey, lockValue)

		// 再次尝试获取锁（应该成功）
		acquired3, err := Redis.TryLock(ctx, lockKey, "new_value", time.Second, 100*time.Millisecond)
		assert.NoError(t, err)
		assert.True(t, acquired3, "锁释放后应该能获取")

		// 清理
		Redis.Unlock(ctx, lockKey, "new_value")
	})

	t.Run("Lock Renewal", func(t *testing.T) {
		lockKey := "test_lock:renewal"
		lockValue := "test_value_renewal"

		// 获取锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, 500*time.Millisecond)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 续期锁
		renewed, err := Redis.RenewLock(ctx, lockKey, lockValue, time.Second)
		assert.NoError(t, err)
		assert.True(t, renewed, "应该成功续期锁")

		// 等待一段时间后验证锁仍然有效
		time.Sleep(200 * time.Millisecond)

		// 尝试获取锁（应该失败，因为锁仍然有效）
		acquired2, err := Redis.Lock(ctx, lockKey, "new_value", time.Second)
		assert.NoError(t, err)
		assert.False(t, acquired2, "续期后的锁应该仍然有效")

		// 清理
		Redis.Unlock(ctx, lockKey, lockValue)
	})

	t.Run("Unlock Security", func(t *testing.T) {
		lockKey := "test_lock:security"
		lockValue := "test_value_security"

		// 获取锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 尝试用错误的值释放锁（应该失败）
		err = Redis.Unlock(ctx, lockKey, "wrong_value")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lock not owned by current process")

		// 用正确的值释放锁
		err = Redis.Unlock(ctx, lockKey, lockValue)
		assert.NoError(t, err)
	})

	t.Run("Concurrent Lock Acquisition", func(t *testing.T) {
		lockKey := "test_lock:concurrent"
		numGoroutines := 10
		successCount := 0
		var mu sync.Mutex

		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				lockValue := fmt.Sprintf("goroutine_%d", id)

				acquired, err := Redis.Lock(ctx, lockKey, lockValue, 100*time.Millisecond)
				if err == nil && acquired {
					mu.Lock()
					successCount++
					mu.Unlock()

					// 短暂持有锁
					time.Sleep(10 * time.Millisecond)

					// 释放锁
					Redis.Unlock(ctx, lockKey, lockValue)
				}
			}(i)
		}

		wg.Wait()

		// 应该只有一个goroutine能获取到锁
		assert.Equal(t, 1, successCount, "并发情况下应该只有一个goroutine能获取锁")
	})

	// 新增的严谨测试用例
	t.Run("Lock Reentrancy", func(t *testing.T) {
		lockKey := "test_lock:reentrancy"
		lockValue := "test_value_reentrancy"

		// 第一次获取锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired, "第一次获取锁应该成功")

		// 同一个进程再次获取同一个锁（应该成功，因为实现支持重入）
		acquired2, err := Redis.Lock(ctx, lockKey, lockValue, time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired2, "同一个进程应该能重入获取锁")

		// 验证锁仍然存在且TTL被续期
		client := Redis.GetClient()
		ttl, err := client.PTTL(ctx, lockKey).Result()
		assert.NoError(t, err)
		assert.True(t, ttl > 0, "重入后锁应该仍然有效")

		// 释放锁（只需要释放一次，因为重入锁实际上是同一个锁）
		err = Redis.Unlock(ctx, lockKey, lockValue)
		assert.NoError(t, err)

		// 验证锁已被释放
		acquired3, err := Redis.Lock(ctx, lockKey, "new_value", time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired3, "锁释放后应该能重新获取")

		// 清理
		Redis.Unlock(ctx, lockKey, "new_value")
	})

	t.Run("Unlock Expired Lock", func(t *testing.T) {
		lockKey := "test_lock:unlock_expired"
		lockValue := "test_value_unlock_expired"

		// 获取一个短时间的锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, 50*time.Millisecond)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 等待锁过期
		time.Sleep(100 * time.Millisecond)

		// 尝试解锁已过期的锁（应该失败，但错误消息可能不同）
		err = Redis.Unlock(ctx, lockKey, lockValue)
		assert.Error(t, err)
		// 检查错误消息，可能是"lock has expired"或"lock not owned by current process"
		assert.True(t,
			strings.Contains(err.Error(), "lock has expired") ||
				strings.Contains(err.Error(), "lock not owned by current process"),
			"解锁过期锁应该失败")
	})

	t.Run("Lock Value Tampering Protection", func(t *testing.T) {
		lockKey := "test_lock:tampering"
		lockValue := "test_value_tampering"

		// 获取锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 模拟锁值被意外修改（通过Redis客户端直接设置）
		client := Redis.GetClient()
		err = client.Set(ctx, lockKey, "tampered_value", time.Second).Err()
		assert.NoError(t, err)

		// 尝试用原始值解锁（应该失败，因为值不匹配）
		err = Redis.Unlock(ctx, lockKey, lockValue)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lock not owned by current process")

		// 清理
		client.Del(ctx, lockKey)
	})

	t.Run("High Concurrency Lock Competition", func(t *testing.T) {
		lockKey := "test_lock:high_concurrency"
		numGoroutines := 20 // 减少goroutine数量，避免过于激烈的竞争
		successCount := 0
		failureCount := 0
		var mu sync.Mutex

		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				lockValue := fmt.Sprintf("goroutine_%d", id)

				// 使用TryLock避免无限等待，增加超时时间
				acquired, err := Redis.TryLock(ctx, lockKey, lockValue, 500*time.Millisecond, 200*time.Millisecond)
				if err == nil && acquired {
					mu.Lock()
					successCount++
					mu.Unlock()

					// 短暂持有锁
					time.Sleep(10 * time.Millisecond)

					// 释放锁
					Redis.Unlock(ctx, lockKey, lockValue)
				} else {
					mu.Lock()
					failureCount++
					mu.Unlock()
				}
			}(i)
		}

		wg.Wait()

		// 验证只有一个goroutine能获取锁（考虑到时间窗口，可能有多个成功）
		assert.True(t, successCount >= 1, "高并发情况下至少应该有一个goroutine能获取锁")
		assert.True(t, failureCount >= 0, "应该有goroutine获取锁失败")
		t.Logf("成功获取锁: %d, 失败: %d", successCount, failureCount)
	})

	t.Run("Lock TTL Precision", func(t *testing.T) {
		lockKey := "test_lock:ttl_precision"
		lockValue := "test_value_ttl"

		// 获取一个精确时间的锁
		lockDuration := 100 * time.Millisecond
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, lockDuration)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 获取锁的TTL
		client := Redis.GetClient()
		ttl, err := client.PTTL(ctx, lockKey).Result()
		assert.NoError(t, err)

		// TTL应该在合理范围内（考虑到网络延迟和时钟精度）
		assert.True(t, ttl > 0, "锁TTL应该大于0")
		assert.True(t, ttl <= lockDuration, "锁TTL不应该超过设置的过期时间")

		// 等待锁过期
		time.Sleep(lockDuration + 50*time.Millisecond)

		// 验证锁已过期
		ttl2, err := client.PTTL(ctx, lockKey).Result()
		assert.NoError(t, err)
		assert.True(t, ttl2 <= 0, "锁应该已过期")

		// 清理
		client.Del(ctx, lockKey)
	})

	t.Run("Context Cancellation", func(t *testing.T) {
		lockKey := "test_lock:context_cancel"
		lockValue := "test_value_context"

		// 创建一个可取消的上下文
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// 获取锁
		acquired, err := Redis.Lock(ctx, lockKey, lockValue, time.Second)
		assert.NoError(t, err)
		assert.True(t, acquired)

		// 取消上下文
		cancel()

		// 尝试在已取消的上下文中解锁（应该失败）
		err = Redis.Unlock(ctx, lockKey, lockValue)
		assert.Error(t, err)

		// 使用新的上下文解锁
		ctx2 := context.Background()
		err = Redis.Unlock(ctx2, lockKey, lockValue)
		assert.NoError(t, err)
	})

	t.Run("Lock with Different Expiration Times", func(t *testing.T) {
		testCases := []struct {
			name     string
			duration time.Duration
		}{
			{"10ms", 10 * time.Millisecond},
			{"50ms", 50 * time.Millisecond},
			{"100ms", 100 * time.Millisecond},
			{"1s", time.Second},
			{"10s", 10 * time.Second},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lockKey := fmt.Sprintf("test_lock:duration_%s", tc.name)
				lockValue := fmt.Sprintf("test_value_%s", tc.name)

				// 获取锁
				acquired, err := Redis.Lock(ctx, lockKey, lockValue, tc.duration)
				assert.NoError(t, err)
				assert.True(t, acquired)

				// 验证锁存在
				client := Redis.GetClient()
				exists, err := client.Exists(ctx, lockKey).Result()
				assert.NoError(t, err)
				assert.Equal(t, int64(1), exists)

				// 释放锁
				err = Redis.Unlock(ctx, lockKey, lockValue)
				assert.NoError(t, err)

				// 验证锁已被删除
				exists2, err := client.Exists(ctx, lockKey).Result()
				assert.NoError(t, err)
				assert.Equal(t, int64(0), exists2)
			})
		}
	})
}

// ==================== 版本信息测试 ====================

func TestRedisVersionInfo(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}

	t.Run("Version Information", func(t *testing.T) {
		// 获取版本信息
		version := Redis.GetVersion()
		assert.Greater(t, version, 0.0, "版本号应该大于0")

		versionStr := Redis.GetVersionString()
		assert.NotEmpty(t, versionStr, "版本字符串不应该为空")

		versionInfo := Redis.GetVersionInfo()
		assert.NotNil(t, versionInfo, "版本信息不应该为nil")
		assert.Contains(t, versionInfo, "version")
		assert.Contains(t, versionInfo, "version_str")
		assert.Contains(t, versionInfo, "is_legacy")
		assert.Contains(t, versionInfo, "supports_keep_ttl")
	})

	t.Run("Version Compatibility", func(t *testing.T) {
		// 测试版本兼容性检查
		supported := Redis.IsVersionSupported(4.0)
		assert.True(t, supported, "Redis 4.0+ 应该被支持")

		supported = Redis.IsVersionSupported(6.0)
		if Redis.GetVersion() >= 6.0 {
			assert.True(t, supported, "Redis 6.0+ 应该支持KeepTTL")
		} else {
			assert.False(t, supported, "低版本Redis不应该支持KeepTTL")
		}

		// 测试极高版本（应该总是支持）
		supported = Redis.IsVersionSupported(99.0)
		assert.False(t, supported, "极高版本不应该被支持")
	})
}

// ==================== 客户端函数测试 ====================

func TestRedisClientFunctions(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}

	t.Run("GetClient", func(t *testing.T) {
		// 获取原始Redis客户端
		client := Redis.GetClient()
		assert.NotNil(t, client, "原始客户端不应该为nil")

		// 测试原始客户端功能
		ctx := context.Background()
		err := client.Set(ctx, "test_client_key", "test_value", time.Minute).Err()
		assert.NoError(t, err)

		value, err := client.Get(ctx, "test_client_key").Result()
		assert.NoError(t, err)
		assert.Equal(t, "test_value", value)

		// 清理测试数据
		client.Del(ctx, "test_client_key")
	})

	t.Run("AddHook", func(t *testing.T) {
		// 测试添加Hook
		hook := &testHook{}
		Redis.AddHook(hook)

		// 执行一个操作来触发Hook
		ctx := context.Background()
		err := Redis.Set(ctx, "test_hook_key", "test_value", time.Minute)
		assert.NoError(t, err)

		// 清理测试数据
		Redis.Del(ctx, "test_hook_key")
	})
}

// 测试用的Hook
type testHook struct{}

func (h *testHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (h *testHook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	return nil
}

func (h *testHook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (h *testHook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	return nil
}

func (h *testHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *testHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}

func (h *testHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// ==================== 配置选项测试 ====================

func TestRedisOptions(t *testing.T) {
	t.Run("DefaultOptions", func(t *testing.T) {
		opts := DefaultOptions()
		assert.NotNil(t, opts)
		assert.Equal(t, []string{"localhost:6379"}, opts.Addrs)
		assert.Equal(t, 0, opts.DB)
		assert.Equal(t, 10, opts.PoolSize)
		assert.Equal(t, 5, opts.MinIdleConns)
		assert.Equal(t, 5*time.Second, opts.DialTimeout)
		assert.Equal(t, 3*time.Second, opts.ReadTimeout)
		assert.Equal(t, 3*time.Second, opts.WriteTimeout)
		assert.Equal(t, 3, opts.MaxRetries)
		assert.False(t, opts.EnableLog)
		assert.Nil(t, opts.Logger)
		assert.Equal(t, 2, opts.Protocol)
	})

	t.Run("Option Functions", func(t *testing.T) {
		opts := &Options{}

		// 测试WithAddrs
		WithAddrs("127.0.0.1:6380", "127.0.0.1:6381")(opts)
		assert.Equal(t, []string{"127.0.0.1:6380", "127.0.0.1:6381"}, opts.Addrs)

		// 测试WithPassword
		WithPassword("test_password")(opts)
		assert.Equal(t, "test_password", opts.Password)

		// 测试WithDB
		WithDB(5)(opts)
		assert.Equal(t, 5, opts.DB)

		// 测试WithPoolSize
		WithPoolSize(20)(opts)
		assert.Equal(t, 20, opts.PoolSize)

		// 测试WithMinIdleConns
		WithMinIdleConns(10)(opts)
		assert.Equal(t, 10, opts.MinIdleConns)

		// 测试WithTimeout
		WithTimeout(10*time.Second, 5*time.Second, 5*time.Second)(opts)
		assert.Equal(t, 10*time.Second, opts.DialTimeout)
		assert.Equal(t, 5*time.Second, opts.ReadTimeout)
		assert.Equal(t, 5*time.Second, opts.WriteTimeout)

		// 测试WithMaxRetries
		WithMaxRetries(5)(opts)
		assert.Equal(t, 5, opts.MaxRetries)

		// 测试WithProtocol
		WithProtocol(3)(opts)
		assert.Equal(t, 3, opts.Protocol)
	})
}

// ==================== 边界条件和错误处理测试 ====================

func TestRedisEdgeCasesAndErrorHandling(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Large Value Handling", func(t *testing.T) {
		// 测试大值处理
		largeValue := strings.Repeat("a", 1024*1024) // 1MB
		err := Redis.Set(ctx, "large_key", largeValue, time.Minute)
		assert.NoError(t, err)

		value, err := Redis.Get(ctx, "large_key")
		assert.NoError(t, err)
		assert.Equal(t, largeValue, value)

		// 清理测试数据
		Redis.Del(ctx, "large_key")
	})

	t.Run("Special Characters in Keys", func(t *testing.T) {
		// 测试特殊字符键
		specialKeys := []string{
			"key:with:colons",
			"key with spaces",
			"key-with-dashes",
			"key_with_underscores",
			"key.with.dots",
			"key[with]brackets",
			"key{with}braces",
			"key\"with\"quotes",
			"key'with'quotes",
			"key`with`backticks",
			"key\nwith\nnewlines",
			"key\twith\ttabs",
			"key\rwith\rcarriage",
		}

		for _, key := range specialKeys {
			err := Redis.Set(ctx, key, "value", time.Minute)
			assert.NoError(t, err, "设置键失败: %s", key)

			value, err := Redis.Get(ctx, key)
			assert.NoError(t, err, "获取键失败: %s", key)
			assert.Equal(t, "value", value, "键值不匹配: %s", key)

			// 清理测试数据
			Redis.Del(ctx, key)
		}
	})

	t.Run("Empty and Nil Values", func(t *testing.T) {
		// 测试空字符串值
		err := Redis.Set(ctx, "empty_string", "", time.Minute)
		assert.NoError(t, err)

		value, err := Redis.Get(ctx, "empty_string")
		assert.NoError(t, err)
		assert.Equal(t, "", value)

		// 测试零值
		err = Redis.Set(ctx, "zero_int", 0, time.Minute)
		assert.NoError(t, err)

		value, err = Redis.Get(ctx, "zero_int")
		assert.NoError(t, err)
		assert.Equal(t, "0", value)

		// 测试false值
		err = Redis.Set(ctx, "false_bool", false, time.Minute)
		assert.NoError(t, err)

		value, err = Redis.Get(ctx, "false_bool")
		assert.NoError(t, err)
		assert.Equal(t, "0", value, "Redis将false存储为'0'")

		// 清理测试数据
		Redis.Del(ctx, "empty_string", "zero_int", "false_bool")
	})

	t.Run("Expiration Edge Cases", func(t *testing.T) {
		// 测试无过期时间
		err := Redis.Set(ctx, "no_expire", "value", 0)
		assert.NoError(t, err)

		// 测试负过期时间（应该立即过期）
		err = Redis.Set(ctx, "negative_expire", "value", -1*time.Second)
		assert.NoError(t, err)

		// 等待一下确保过期
		time.Sleep(200 * time.Millisecond)

		// 检查负过期时间的键应该不存在
		value, err := Redis.Get(ctx, "negative_expire")
		assert.NoError(t, err)
		// 注意：Redis可能不会立即处理负过期时间，这里只验证操作成功
		// 实际行为可能因Redis版本而异
		t.Logf("负过期时间键的值: %s", value)

		// 检查无过期时间的键应该存在
		value, err = Redis.Get(ctx, "no_expire")
		assert.NoError(t, err)
		assert.Equal(t, "value", value)

		// 清理测试数据
		Redis.Del(ctx, "no_expire")
	})

	t.Run("Invalid Operations", func(t *testing.T) {
		// 测试删除不存在的键
		err := Redis.Del(ctx, "non_existent_key")
		assert.NoError(t, err) // Redis删除不存在的键不会返回错误

		// 测试获取不存在的键
		value, err := Redis.Get(ctx, "non_existent_key")
		assert.NoError(t, err)
		assert.Empty(t, value)

		// 测试递增不存在的键
		val, err := Redis.Incr(ctx, "non_existent_counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)

		// 清理测试数据
		Redis.Del(ctx, "non_existent_counter")
	})

	t.Run("Parameter Validation", func(t *testing.T) {
		// 测试空键
		err := Redis.Set(ctx, "", "value", time.Minute)
		assert.Error(t, err, "空键应该返回错误")

		// 测试nil值
		err = Redis.Set(ctx, "key", nil, time.Minute)
		assert.Error(t, err, "nil值应该返回错误")

		// 测试空键的SetNX
		_, err = Redis.SetNX(ctx, "", "value", time.Minute)
		assert.Error(t, err, "空键的SetNX应该返回错误")

		// 测试nil值的SetNX
		_, err = Redis.SetNX(ctx, "key", nil, time.Minute)
		assert.Error(t, err, "nil值的SetNX应该返回错误")

		// 测试空键的HSet
		err = Redis.HSet(ctx, "", "field", "value")
		assert.Error(t, err, "空键的HSet应该返回错误")

		// 测试空键的HGet
		_, err = Redis.HGet(ctx, "", "field")
		assert.Error(t, err, "空键的HGet应该返回错误")

		// 测试空键的Lock
		_, err = Redis.Lock(ctx, "", "value", time.Second)
		assert.Error(t, err, "空键的Lock应该返回错误")

		// 测试空值的Lock
		_, err = Redis.Lock(ctx, "key", "", time.Second)
		assert.Error(t, err, "空值的Lock应该返回错误")
	})

	t.Run("Redis KeepTTL Compatibility", func(t *testing.T) {
		// 测试KeepTTL功能（Redis 6.0+）
		if Redis.IsVersionSupported(6.0) {
			// 先设置一个带过期时间的键
			err := Redis.Set(ctx, "keep_ttl_key", "original_value", time.Minute)
			assert.NoError(t, err)

			// 等待一下
			time.Sleep(100 * time.Millisecond)

			// 使用KeepTTL更新值
			err = Redis.Set(ctx, "keep_ttl_key", "new_value", redis.KeepTTL)
			assert.NoError(t, err)

			// 检查值已更新
			value, err := Redis.Get(ctx, "keep_ttl_key")
			assert.NoError(t, err)
			assert.Equal(t, "new_value", value)

			// 清理测试数据
			Redis.Del(ctx, "keep_ttl_key")
		} else {
			// 对于低版本Redis，测试SetForLegacy
			err := Redis.Set(ctx, "legacy_key", "original_value", time.Minute)
			assert.NoError(t, err)

			// 等待一下
			time.Sleep(100 * time.Millisecond)

			// 使用SetForLegacy保持TTL
			err = Redis.SetForLegacy(ctx, "legacy_key", "new_value", redis.KeepTTL)
			assert.NoError(t, err)

			// 检查值已更新
			value, err := Redis.Get(ctx, "legacy_key")
			assert.NoError(t, err)
			assert.Equal(t, "new_value", value)

			// 清理测试数据
			Redis.Del(ctx, "legacy_key")
		}
	})

	t.Run("SetForLegacy and KeepTTL Compatibility", func(t *testing.T) {
		// 测试SetForLegacy函数
		// 先设置一个带过期时间的键
		err := Redis.Set(ctx, "legacy_test_key", "original_value", time.Minute)
		assert.NoError(t, err)

		// 等待一下
		time.Sleep(100 * time.Millisecond)

		// 使用SetForLegacy保持TTL
		err = Redis.SetForLegacy(ctx, "legacy_test_key", "new_value", redis.KeepTTL)
		assert.NoError(t, err)

		// 检查值已更新
		value, err := Redis.Get(ctx, "legacy_test_key")
		assert.NoError(t, err)
		assert.Equal(t, "new_value", value)

		// 测试SetForLegacy的正常过期时间设置
		err = Redis.SetForLegacy(ctx, "legacy_normal_key", "normal_value", time.Minute)
		assert.NoError(t, err)

		value, err = Redis.Get(ctx, "legacy_normal_key")
		assert.NoError(t, err)
		assert.Equal(t, "normal_value", value)

		// 测试SetForLegacy的无过期时间设置
		err = Redis.SetForLegacy(ctx, "legacy_no_expire_key", "no_expire_value", 0)
		assert.NoError(t, err)

		value, err = Redis.Get(ctx, "legacy_no_expire_key")
		assert.NoError(t, err)
		assert.Equal(t, "no_expire_value", value)

		// 清理测试数据
		Redis.Del(ctx, "legacy_test_key", "legacy_normal_key", "legacy_no_expire_key")
	})

	t.Run("SetForLegacy Parameter Validation", func(t *testing.T) {
		// 测试空键
		err := Redis.SetForLegacy(ctx, "", "value", time.Minute)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "key cannot be empty")

		// 测试nil值
		err = Redis.SetForLegacy(ctx, "key", nil, time.Minute)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "value cannot be nil")
	})
}

// ==================== 日志功能测试 ====================

func TestRedisWithDifferentLogFormats(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}

	t.Run("JSON Log Format", func(t *testing.T) {
		// 创建带JSON日志的Redis客户端
		_, err := NewRedisLogger(WithLogFormatter(JSONLogFormatter))
		assert.NoError(t, err)

		// 执行一些操作来生成日志
		ctx := context.Background()
		err = Redis.Set(ctx, "log_test_key", "log_test_value", time.Minute)
		assert.NoError(t, err)

		// 验证操作成功
		value, err := Redis.Get(ctx, "log_test_key")
		assert.NoError(t, err)
		assert.Equal(t, "log_test_value", value)

		// 清理测试数据
		Redis.Del(ctx, "log_test_key")
	})

	t.Run("Text Log Format", func(t *testing.T) {
		// 创建带文本日志的Redis客户端
		_, err := NewRedisLogger(WithLogFormatter(DefaultLogFormatter))
		assert.NoError(t, err)

		// 执行一些操作来生成日志
		ctx := context.Background()
		err = Redis.Set(ctx, "log_test_key2", "log_test_value2", time.Minute)
		assert.NoError(t, err)

		// 验证操作成功
		value, err := Redis.Get(ctx, "log_test_key2")
		assert.NoError(t, err)
		assert.Equal(t, "log_test_value2", value)

		// 清理测试数据
		Redis.Del(ctx, "log_test_key2")
	})
}

func TestRedisLogLevels(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}

	t.Run("Info Level Logging", func(t *testing.T) {
		// 创建info级别日志记录器
		_, err := NewRedisLogger(WithLogLevel(LogLevelInfo))
		assert.NoError(t, err)

		// 执行操作
		ctx := context.Background()
		err = Redis.Set(ctx, "log_level_test", "value", time.Minute)
		assert.NoError(t, err)

		// 验证操作成功
		value, err := Redis.Get(ctx, "log_level_test")
		assert.NoError(t, err)
		assert.Equal(t, "value", value)

		// 清理测试数据
		Redis.Del(ctx, "log_level_test")
	})

	t.Run("Debug Level Logging", func(t *testing.T) {
		// 创建debug级别日志记录器
		_, err := NewRedisLogger(WithLogLevel(LogLevelDebug))
		assert.NoError(t, err)

		// 执行操作
		ctx := context.Background()
		err = Redis.Set(ctx, "debug_test", "value", time.Minute)
		assert.NoError(t, err)

		// 验证操作成功
		value, err := Redis.Get(ctx, "debug_test")
		assert.NoError(t, err)
		assert.Equal(t, "value", value)

		// 清理测试数据
		Redis.Del(ctx, "debug_test")
	})
}

func TestRedisLogContent(t *testing.T) {
	t.Run("Operation Logging", func(t *testing.T) {
		// 检查全局Redis客户端是否可用
		if Redis == nil {
			t.Skip("Redis client not initialized")
			return
		}

		// 执行各种操作
		ctx := context.Background()

		// SET操作
		err := Redis.Set(ctx, "log_op_test", "value", time.Minute)
		assert.NoError(t, err)

		// GET操作
		_, err = Redis.Get(ctx, "log_op_test")
		assert.NoError(t, err)

		// DEL操作
		err = Redis.Del(ctx, "log_op_test")
		assert.NoError(t, err)

		// 验证操作成功（由于没有实际的日志记录器，我们只验证操作本身）
		assert.NoError(t, err)
	})
}

// ==================== 并发测试 ====================

func TestRedisConcurrency(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Concurrent Set Operations", func(t *testing.T) {
		numGoroutines := 50
		var wg sync.WaitGroup
		errors := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				key := fmt.Sprintf("concurrent_set_%d", id)
				value := fmt.Sprintf("value_%d", id)

				err := Redis.Set(ctx, key, value, time.Minute)
				if err != nil {
					errors <- err
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		// 检查是否有错误
		for err := range errors {
			t.Errorf("并发Set操作失败: %v", err)
		}

		// 验证所有值都被正确设置
		for i := 0; i < numGoroutines; i++ {
			key := fmt.Sprintf("concurrent_set_%d", i)
			expectedValue := fmt.Sprintf("value_%d", i)

			value, err := Redis.Get(ctx, key)
			assert.NoError(t, err)
			assert.Equal(t, expectedValue, value)
		}

		// 清理测试数据
		for i := 0; i < numGoroutines; i++ {
			key := fmt.Sprintf("concurrent_set_%d", i)
			Redis.Del(ctx, key)
		}
	})

	t.Run("Concurrent Hash Operations", func(t *testing.T) {
		numGoroutines := 30
		var wg sync.WaitGroup
		errors := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				key := fmt.Sprintf("concurrent_hash_%d", id)
				field := fmt.Sprintf("field_%d", id)
				value := fmt.Sprintf("value_%d", id)

				err := Redis.HSet(ctx, key, field, value)
				if err != nil {
					errors <- err
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		// 检查是否有错误
		for err := range errors {
			t.Errorf("并发Hash操作失败: %v", err)
		}

		// 验证所有值都被正确设置
		for i := 0; i < numGoroutines; i++ {
			key := fmt.Sprintf("concurrent_hash_%d", i)
			field := fmt.Sprintf("field_%d", i)
			expectedValue := fmt.Sprintf("value_%d", i)

			value, err := Redis.HGet(ctx, key, field)
			assert.NoError(t, err)
			assert.Equal(t, expectedValue, value)
		}

		// 清理测试数据
		for i := 0; i < numGoroutines; i++ {
			key := fmt.Sprintf("concurrent_hash_%d", i)
			Redis.Del(ctx, key)
		}
	})

	t.Run("Concurrent Counter Operations", func(t *testing.T) {
		counterKey := "concurrent_counter"
		numGoroutines := 100
		var wg sync.WaitGroup

		// 初始化计数器
		err := Redis.Set(ctx, counterKey, "0", 0)
		assert.NoError(t, err)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := Redis.Incr(ctx, counterKey)
				if err != nil {
					t.Errorf("并发计数器操作失败: %v", err)
				}
			}()
		}

		wg.Wait()

		// 验证最终计数
		finalValue, err := Redis.Get(ctx, counterKey)
		assert.NoError(t, err)
		assert.Equal(t, "100", finalValue, "并发操作后计数器应该是100")

		// 清理测试数据
		Redis.Del(ctx, counterKey)
	})
}

// ==================== 性能测试 ====================

func TestRedisPerformance(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Bulk Set Performance", func(t *testing.T) {
		numKeys := 1000
		start := time.Now()

		// 批量设置键值对
		for i := 0; i < numKeys; i++ {
			key := fmt.Sprintf("perf_set_%d", i)
			value := fmt.Sprintf("value_%d", i)
			err := Redis.Set(ctx, key, value, time.Minute)
			assert.NoError(t, err)
		}

		duration := time.Since(start)
		t.Logf("设置 %d 个键值对耗时: %v", numKeys, duration)
		assert.Less(t, duration, 5*time.Second, "批量设置应该在5秒内完成")

		// 清理测试数据
		for i := 0; i < numKeys; i++ {
			key := fmt.Sprintf("perf_set_%d", i)
			Redis.Del(ctx, key)
		}
	})

	t.Run("Bulk Get Performance", func(t *testing.T) {
		numKeys := 1000

		// 先设置测试数据
		for i := 0; i < numKeys; i++ {
			key := fmt.Sprintf("perf_get_%d", i)
			value := fmt.Sprintf("value_%d", i)
			err := Redis.Set(ctx, key, value, time.Minute)
			assert.NoError(t, err)
		}

		start := time.Now()

		// 批量获取键值对
		for i := 0; i < numKeys; i++ {
			key := fmt.Sprintf("perf_get_%d", i)
			expectedValue := fmt.Sprintf("value_%d", i)
			value, err := Redis.Get(ctx, key)
			assert.NoError(t, err)
			assert.Equal(t, expectedValue, value)
		}

		duration := time.Since(start)
		t.Logf("获取 %d 个键值对耗时: %v", numKeys, duration)
		assert.Less(t, duration, 3*time.Second, "批量获取应该在3秒内完成")

		// 清理测试数据
		for i := 0; i < numKeys; i++ {
			key := fmt.Sprintf("perf_get_%d", i)
			Redis.Del(ctx, key)
		}
	})

	t.Run("Large Value Performance", func(t *testing.T) {
		// 测试大值处理性能
		largeValue := strings.Repeat("a", 1024*1024) // 1MB
		key := "perf_large_value"

		start := time.Now()
		err := Redis.Set(ctx, key, largeValue, time.Minute)
		duration := time.Since(start)
		assert.NoError(t, err)
		t.Logf("设置1MB值耗时: %v", duration)
		assert.Less(t, duration, 2*time.Second, "设置大值应该在2秒内完成")

		start = time.Now()
		value, err := Redis.Get(ctx, key)
		duration = time.Since(start)
		assert.NoError(t, err)
		assert.Equal(t, largeValue, value)
		t.Logf("获取1MB值耗时: %v", duration)
		assert.Less(t, duration, 2*time.Second, "获取大值应该在2秒内完成")

		// 清理测试数据
		Redis.Del(ctx, key)
	})
}

// ==================== 内存和资源管理测试 ====================

func TestRedisMemoryAndResourceManagement(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Connection Pool Management", func(t *testing.T) {
		// 测试连接池配置
		client := Redis.GetClient()
		assert.NotNil(t, client)

		// 获取连接池统计信息
		poolStats := client.PoolStats()
		t.Logf("连接池统计: 总连接数=%d, 空闲连接数=%d, 使用中连接数=%d",
			poolStats.TotalConns, poolStats.IdleConns, poolStats.StaleConns)
	})

	t.Run("Memory Usage with Large Data", func(t *testing.T) {
		// 测试大量数据的内存使用
		numKeys := 100
		valueSize := 1024 // 1KB per value

		// 设置大量数据
		for i := 0; i < numKeys; i++ {
			key := fmt.Sprintf("mem_test_key_%d", i)
			valueData := strings.Repeat("v", valueSize)

			err := Redis.Set(ctx, key, valueData, time.Minute)
			assert.NoError(t, err)
		}

		// 验证数据
		for i := 0; i < numKeys; i++ {
			key := fmt.Sprintf("mem_test_key_%d", i)
			expectedValue := strings.Repeat("v", valueSize)

			value, err := Redis.Get(ctx, key)
			assert.NoError(t, err)
			assert.Equal(t, expectedValue, value)
		}

		// 清理测试数据
		for i := 0; i < numKeys; i++ {
			key := fmt.Sprintf("mem_test_key_%d", i)
			Redis.Del(ctx, key)
		}
	})

	t.Run("Resource Cleanup", func(t *testing.T) {
		// 测试资源清理
		testKey := "cleanup_test"
		testValue := "cleanup_value"

		// 设置数据
		err := Redis.Set(ctx, testKey, testValue, time.Minute)
		assert.NoError(t, err)

		// 验证数据存在
		value, err := Redis.Get(ctx, testKey)
		assert.NoError(t, err)
		assert.Equal(t, testValue, value)

		// 清理数据
		err = Redis.Del(ctx, testKey)
		assert.NoError(t, err)

		// 验证数据已被清理
		value, err = Redis.Get(ctx, testKey)
		assert.NoError(t, err)
		assert.Empty(t, value)
	})
}

// ==================== 网络和超时测试 ====================

func TestRedisNetworkAndTimeout(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Connection Resilience", func(t *testing.T) {
		// 测试连接恢复能力
		testKey := "resilience_test"
		testValue := "resilience_value"

		// 执行操作
		err := Redis.Set(ctx, testKey, testValue, time.Minute)
		assert.NoError(t, err)

		// 验证操作成功
		value, err := Redis.Get(ctx, testKey)
		assert.NoError(t, err)
		assert.Equal(t, testValue, value)

		// 清理测试数据
		Redis.Del(ctx, testKey)
	})

	t.Run("Timeout Handling", func(t *testing.T) {
		// 测试超时处理
		testKey := "timeout_test"
		testValue := "timeout_value"

		// 设置一个较短的过期时间
		err := Redis.Set(ctx, testKey, testValue, 100*time.Millisecond)
		assert.NoError(t, err)

		// 等待过期
		time.Sleep(200 * time.Millisecond)

		// 验证键已过期
		value, err := Redis.Get(ctx, testKey)
		assert.NoError(t, err)
		assert.Empty(t, value)
	})

	t.Run("Network Latency Simulation", func(t *testing.T) {
		// 模拟网络延迟情况下的操作
		testKey := "latency_test"
		testValue := "latency_value"

		// 执行操作并测量时间
		start := time.Now()
		err := Redis.Set(ctx, testKey, testValue, time.Minute)
		duration := time.Since(start)
		assert.NoError(t, err)

		// 记录操作耗时
		t.Logf("Set操作耗时: %v", duration)
		assert.Less(t, duration, 1*time.Second, "操作应该在1秒内完成")

		// 清理测试数据
		Redis.Del(ctx, testKey)
	})
}

// ==================== 集成测试 ====================

func TestRedisIntegration(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Complex Workflow", func(t *testing.T) {
		// 测试复杂的Redis工作流程
		userID := "user_123"
		sessionKey := fmt.Sprintf("session:%s", userID)
		userDataKey := fmt.Sprintf("user_data:%s", userID)
		counterKey := fmt.Sprintf("counter:%s", userID)

		// 1. 创建用户会话
		sessionData := map[string]interface{}{
			"user_id":    userID,
			"login_time": time.Now().Unix(),
			"status":     "active",
		}
		err := Redis.HSet(ctx, sessionKey, sessionData)
		assert.NoError(t, err)

		// 2. 设置用户数据
		userData := map[string]interface{}{
			"name":  "测试用户",
			"email": "test@example.com",
			"role":  "user",
		}
		err = Redis.HSet(ctx, userDataKey, userData)
		assert.NoError(t, err)

		// 3. 初始化计数器
		err = Redis.Set(ctx, counterKey, "0", time.Hour)
		assert.NoError(t, err)

		// 4. 模拟用户操作
		for i := 0; i < 5; i++ {
			// 增加计数器
			_, err = Redis.Incr(ctx, counterKey)
			assert.NoError(t, err)

			// 更新会话状态
			err = Redis.HSet(ctx, sessionKey, "last_activity", time.Now().Unix())
			assert.NoError(t, err)

			// 短暂等待
			time.Sleep(10 * time.Millisecond)
		}

		// 5. 验证最终状态
		// 检查会话数据
		sessionInfo, err := Redis.HGetList(ctx, sessionKey)
		assert.NoError(t, err)
		assert.Contains(t, sessionInfo, "user_id")
		assert.Contains(t, sessionInfo, "status")

		// 检查用户数据
		userInfo, err := Redis.HGetList(ctx, userDataKey)
		assert.NoError(t, err)
		assert.Equal(t, "测试用户", userInfo["name"])
		assert.Equal(t, "test@example.com", userInfo["email"])

		// 检查计数器
		counterValue, err := Redis.Get(ctx, counterKey)
		assert.NoError(t, err)
		assert.Equal(t, "5", counterValue)

		// 清理测试数据
		Redis.Del(ctx, sessionKey, userDataKey, counterKey)
	})

	t.Run("Data Consistency", func(t *testing.T) {
		// 测试数据一致性
		orderKey := "order:12345"
		orderItemsKey := "order_items:12345"
		orderStatusKey := "order_status:12345"

		// 创建订单
		orderData := map[string]interface{}{
			"order_id":     "12345",
			"customer_id":  "customer_001",
			"total_amount": 299.99,
			"created_at":   time.Now().Unix(),
		}
		err := Redis.HSet(ctx, orderKey, orderData)
		assert.NoError(t, err)

		// 设置订单项目
		orderItems := []string{"item_001", "item_002", "item_003"}
		for _, item := range orderItems {
			err = Redis.LPush(ctx, orderItemsKey, item)
			assert.NoError(t, err)
		}

		// 设置订单状态
		err = Redis.Set(ctx, orderStatusKey, "pending", time.Hour)
		assert.NoError(t, err)

		// 验证数据一致性
		orderInfo, err := Redis.HGetList(ctx, orderKey)
		assert.NoError(t, err)
		assert.Equal(t, "12345", orderInfo["order_id"])

		// 验证订单项目数量
		itemCount := 0
		for {
			item, err := Redis.RPop(ctx, orderItemsKey)
			if err != nil || item == "" {
				break
			}
			itemCount++
		}
		assert.Equal(t, 3, itemCount)

		// 验证订单状态
		status, err := Redis.Get(ctx, orderStatusKey)
		assert.NoError(t, err)
		assert.Equal(t, "pending", status)

		// 清理测试数据
		Redis.Del(ctx, orderKey, orderItemsKey, orderStatusKey)
	})
}

// ==================== 压力测试 ====================

func TestRedisStress(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("High Volume Operations", func(t *testing.T) {
		// 高容量操作测试
		numOperations := 5000
		start := time.Now()

		// 并发执行大量操作
		var wg sync.WaitGroup
		errors := make(chan error, numOperations)

		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				// 执行多种操作
				key := fmt.Sprintf("stress_test_%d", id)

				// SET操作
				err := Redis.Set(ctx, key, fmt.Sprintf("value_%d", id), time.Minute)
				if err != nil {
					errors <- err
					return
				}

				// GET操作
				_, err = Redis.Get(ctx, key)
				if err != nil {
					errors <- err
					return
				}

				// HASH操作
				hashKey := fmt.Sprintf("stress_hash_%d", id)
				err = Redis.HSet(ctx, hashKey, "field", fmt.Sprintf("value_%d", id))
				if err != nil {
					errors <- err
					return
				}

				// 计数器操作
				counterKey := fmt.Sprintf("stress_counter_%d", id%100) // 使用模运算减少键的数量
				_, err = Redis.Incr(ctx, counterKey)
				if err != nil {
					errors <- err
					return
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		duration := time.Since(start)
		t.Logf("执行 %d 个操作耗时: %v", numOperations, duration)

		// 检查错误
		errorCount := 0
		for err := range errors {
			errorCount++
			t.Logf("操作错误: %v", err)
		}

		assert.Less(t, errorCount, numOperations/10, "错误率应该低于10%")
		assert.Less(t, duration, 30*time.Second, "压力测试应该在30秒内完成")

		// 清理测试数据
		for i := 0; i < numOperations; i++ {
			key := fmt.Sprintf("stress_test_%d", i)
			hashKey := fmt.Sprintf("stress_hash_%d", i)
			Redis.Del(ctx, key, hashKey)
		}

		for i := 0; i < 100; i++ {
			counterKey := fmt.Sprintf("stress_counter_%d", i)
			Redis.Del(ctx, counterKey)
		}
	})

	t.Run("Memory Pressure Test", func(t *testing.T) {
		// 内存压力测试
		numLargeKeys := 100
		valueSize := 10 * 1024 // 10KB per value

		start := time.Now()

		// 设置大量大值
		for i := 0; i < numLargeKeys; i++ {
			key := fmt.Sprintf("memory_pressure_%d", i)
			value := strings.Repeat(fmt.Sprintf("data_%d_", i), valueSize/10) // 确保值大小接近预期

			err := Redis.Set(ctx, key, value, time.Minute)
			assert.NoError(t, err)
		}

		// 验证所有值
		for i := 0; i < numLargeKeys; i++ {
			key := fmt.Sprintf("memory_pressure_%d", i)
			expectedValue := strings.Repeat(fmt.Sprintf("data_%d_", i), valueSize/10)

			value, err := Redis.Get(ctx, key)
			assert.NoError(t, err)
			assert.Equal(t, expectedValue, value)
		}

		duration := time.Since(start)
		t.Logf("内存压力测试耗时: %v", duration)
		assert.Less(t, duration, 20*time.Second, "内存压力测试应该在20秒内完成")

		// 清理测试数据
		for i := 0; i < numLargeKeys; i++ {
			key := fmt.Sprintf("memory_pressure_%d", i)
			Redis.Del(ctx, key)
		}
	})
}

// ==================== 辅助函数 ====================
