package redisx

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
)

func setupTestRedis(t *testing.T) (*RedisClient, *miniredis.Miniredis, func()) {
	// 创建 miniredis 实例用于测试
	mr, err := miniredis.Run()
	assert.NoError(t, err)

	// 初始化 Redis 客户端
	config := RedisConfig{
		Addrs:        []string{mr.Addr()},
		Password:     "",
		DB:           0,
		PoolSize:     10,
		MinIdleConns: 5,
		DialTimeout:  1,
		ReadTimeout:  1,
		WriteTimeout: 1,
		MaxRetries:   3,
	}

	client := InitRedis(config)
	assert.NotNil(t, client)

	cleanup := func() {
		client.Close()
		mr.Close()
	}

	return client, mr, cleanup
}

func TestRedisBasicOperations(t *testing.T) {
	client, _, cleanup := setupTestRedis(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("Set and Get", func(t *testing.T) {
		err := client.Set(ctx, "test_key", "test_value", time.Minute)
		assert.NoError(t, err)

		value, err := client.Get(ctx, "test_key")
		assert.NoError(t, err)
		assert.Equal(t, "test_value", value)

		// 测试不存在的键
		value, err = client.Get(ctx, "non_existent_key")
		assert.NoError(t, err)
		assert.Empty(t, value)
	})

	t.Run("Incr and Decr", func(t *testing.T) {
		// 测试递增
		val, err := client.Incr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)

		val, err = client.Incr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(2), val)

		// 测试递减
		val, err = client.Decr(ctx, "counter")
		assert.NoError(t, err)
		assert.Equal(t, int64(1), val)
	})
}

func TestRedisHashOperations(t *testing.T) {
	client, _, cleanup := setupTestRedis(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("HSet and HGet with Field-Value Pairs", func(t *testing.T) {
		err := client.HSet(ctx, "user:1", "name", "张三", "age", "25")
		assert.NoError(t, err)

		value, err := client.HGet(ctx, "user:1", "name")
		assert.NoError(t, err)
		assert.Equal(t, "张三", value)

		value, err = client.HGet(ctx, "user:1", "age")
		assert.NoError(t, err)
		assert.Equal(t, "25", value)
	})

	t.Run("HSet and HGet with Map", func(t *testing.T) {
		userData := map[string]interface{}{
			"name": "李四",
			"age":  30,
		}
		err := client.HSet(ctx, "user:2", userData)
		assert.NoError(t, err)

		value, err := client.HGet(ctx, "user:2", "name")
		assert.NoError(t, err)
		assert.Equal(t, "李四", value)
	})

	t.Run("HGetList", func(t *testing.T) {
		values, err := client.HGetList(ctx, "user:1")
		assert.NoError(t, err)
		assert.Equal(t, "张三", values["name"])
		assert.Equal(t, "25", values["age"])

		// 测试不存在的键
		values, err = client.HGetList(ctx, "non_existent_hash")
		assert.NoError(t, err)
		assert.Empty(t, values)
	})
}

func TestRedisListOperations(t *testing.T) {
	client, _, cleanup := setupTestRedis(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("LPush and RPop", func(t *testing.T) {
		// 测试推入元素
		err := client.LPush(ctx, "list", "first", "second", "third")
		assert.NoError(t, err)

		// 测试弹出元素
		value, err := client.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "first", value)

		value, err = client.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "second", value)

		value, err = client.RPop(ctx, "list")
		assert.NoError(t, err)
		assert.Equal(t, "third", value)

		// 测试空列表
		value, err = client.RPop(ctx, "list")
		assert.Error(t, err)
		assert.Empty(t, value)
	})
}

func TestRedisLock(t *testing.T) {
	client, mr, cleanup := setupTestRedis(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("Lock and Unlock", func(t *testing.T) {
		// 测试获取锁
		locked, err := client.Lock(ctx, "test_lock", "process1", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 测试重复获取锁
		locked, err = client.Lock(ctx, "test_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.False(t, locked)

		// 测试解锁
		err = client.Unlock(ctx, "test_lock", "process1")
		assert.NoError(t, err)

		// 测试锁已释放
		locked, err = client.Lock(ctx, "test_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)
	})

	t.Run("Lock Expiration", func(t *testing.T) {
		// 测试锁过期
		locked, err := client.Lock(ctx, "expire_lock", "process1", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)

		// 手动快进时间
		mr.FastForward(2 * time.Second)

		// 测试可以重新获取锁
		locked, err = client.Lock(ctx, "expire_lock", "process2", time.Second)
		assert.NoError(t, err)
		assert.True(t, locked)
	})
}

func TestRedisClusterMode(t *testing.T) {
	// 创建多个 miniredis 实例模拟集群
	mr1, err := miniredis.Run()
	assert.NoError(t, err)
	defer mr1.Close()

	mr2, err := miniredis.Run()
	assert.NoError(t, err)
	defer mr2.Close()

	// 初始化集群模式的 Redis 客户端
	config := RedisConfig{
		Addrs:        []string{mr1.Addr(), mr2.Addr()},
		Password:     "",
		DB:           0,
		PoolSize:     10,
		MinIdleConns: 5,
		DialTimeout:  1,
		ReadTimeout:  1,
		WriteTimeout: 1,
		MaxRetries:   3,
	}

	client := InitRedis(config)
	defer client.Close()

	// 测试基本操作是否正常
	ctx := context.Background()
	err = client.Set(ctx, "cluster_test", "value", time.Minute)
	assert.NoError(t, err)

	value, err := client.Get(ctx, "cluster_test")
	assert.NoError(t, err)
	assert.Equal(t, "value", value)
}
