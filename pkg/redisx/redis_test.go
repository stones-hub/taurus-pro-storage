package redisx

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	// 初始化 Redis 客户端
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
		log.Printf("跳过测试：Redis连接失败: %v", err)
		os.Exit(0)
	}

	// 运行测试
	code := m.Run()

	// 清理资源
	if Redis != nil {
		Redis.Close()
	}

	os.Exit(code)
}

func TestRedisBasicOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}

	ctx := context.Background()

	t.Run("Set and Get", func(t *testing.T) {
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
		Redis.client.Del(ctx, "test_key")
	})

	t.Run("Incr and Decr", func(t *testing.T) {
		// 测试递增
		val, err := Redis.Incr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)

		val, err = Redis.Incr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(2), val)

		// 测试递减
		val, err = Redis.Decr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)

		// 清理测试数据
		Redis.client.Del(ctx, "counter")
	})
}

func TestRedisHashOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("HSet and HGet with Field-Value Pairs", func(t *testing.T) {
		err := Redis.HSet(ctx, "user:1", "name", "张三", "age", "25")
		assert.NoError(t, err)

		value, err := Redis.HGet(ctx, "user:1", "name")
		assert.NoError(t, err)
		assert.Equal(t, "张三", value)

		value, err = Redis.HGet(ctx, "user:1", "age")
		assert.NoError(t, err)
		assert.Equal(t, "25", value)

		// 清理测试数据
		Redis.client.Del(ctx, "user:1")
	})

	t.Run("HSet and HGet with Map", func(t *testing.T) {
		userData := map[string]interface{}{
			"name": "李四",
			"age":  30,
		}
		err := Redis.HSet(ctx, "user:2", userData)
		assert.NoError(t, err)

		value, err := Redis.HGet(ctx, "user:2", "name")
		assert.NoError(t, err)
		assert.Equal(t, "李四", value)

		// 清理测试数据
		Redis.client.Del(ctx, "user:2")
	})

	t.Run("HGetAll", func(t *testing.T) {
		// 先设置测试数据
		err := Redis.HSet(ctx, "user:3", "name", "王五", "age", "35")
		assert.NoError(t, err)

		values, err := Redis.HGetList(ctx, "user:3")
		assert.NoError(t, err)
		assert.Equal(t, "王五", values["name"])
		assert.Equal(t, "35", values["age"])

		// 测试不存在的键
		values, err = Redis.HGetList(ctx, "non_existent_hash")
		assert.NoError(t, err)
		assert.Empty(t, values)

		// 清理测试数据
		Redis.client.Del(ctx, "user:3")
	})
}

func TestRedisListOperations(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("LPush and RPop", func(t *testing.T) {
		// 测试推入元素
		err := Redis.LPush(ctx, "list", "first", "second", "third")
		assert.NoError(t, err)

		// 测试弹出元素
		value, err := Redis.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "first", value)

		value, err = Redis.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "second", value)

		value, err = Redis.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "third", value)

		// 测试空列表
		value, err = Redis.RPop(ctx, "list")
		assert.Error(t, err)
		assert.Empty(t, value)

		// 清理测试数据
		Redis.client.Del(ctx, "list")
	})
}

func TestRedisLock(t *testing.T) {
	if Redis == nil {
		t.Skip("Redis client not initialized")
		return
	}
	ctx := context.Background()

	t.Run("Lock and Unlock", func(t *testing.T) {
		// 测试获取锁
		locked, err := Redis.Lock(ctx, "test_lock", "process1", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 测试重复获取锁
		locked, err = Redis.Lock(ctx, "test_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.False(t, locked)

		// 测试解锁
		err = Redis.Unlock(ctx, "test_lock", "process1")
		assert.NoError(t, err)

		// 测试锁已释放
		locked, err = Redis.Lock(ctx, "test_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 清理测试数据
		Redis.client.Del(ctx, "test_lock")
	})

	t.Run("Lock Expiration", func(t *testing.T) {
		// 测试锁过期
		locked, err := Redis.Lock(ctx, "expire_lock", "process1", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 等待锁过期
		time.Sleep(2 * time.Second)

		// 测试可以重新获取锁
		locked, err = Redis.Lock(ctx, "expire_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 清理测试数据
		Redis.client.Del(ctx, "expire_lock")
	})
}
