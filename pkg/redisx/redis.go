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
// Date: 2025-06-13

package redisx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisClient Redis客户端结构
type RedisClient struct {
	client  redis.UniversalClient
	version float64 // Redis版本号
}

// Redis 全局Redis客户端
var Redis *RedisClient

// Options Redis配置选项
type Options struct {
	Addrs        []string      // Redis地址列表
	Password     string        // 密码
	DB           int           // 数据库编号
	PoolSize     int           // 连接池大小
	MinIdleConns int           // 最小空闲连接数
	DialTimeout  time.Duration // 连接超时时间
	ReadTimeout  time.Duration // 读取超时时间
	WriteTimeout time.Duration // 写入超时时间
	MaxRetries   int           // 最大重试次数
	EnableLog    bool          // 是否启用日志
	Logger       *RedisLogger  // 日志记录器
	Protocol     int           // Redis协议版本 (2=Redis 2.x, 3=Redis 6.0+)
}

// Option 定义配置选项函数类型
type Option func(*Options)

// WithAddrs 设置Redis地址列表
func WithAddrs(addrs ...string) Option {
	return func(o *Options) {
		o.Addrs = addrs
	}
}

// WithPassword 设置密码
func WithPassword(password string) Option {
	return func(o *Options) {
		o.Password = password
	}
}

// WithDB 设置数据库编号
func WithDB(db int) Option {
	return func(o *Options) {
		o.DB = db
	}
}

// WithPoolSize 设置连接池大小
func WithPoolSize(size int) Option {
	return func(o *Options) {
		o.PoolSize = size
	}
}

// WithMinIdleConns 设置最小空闲连接数
func WithMinIdleConns(n int) Option {
	return func(o *Options) {
		o.MinIdleConns = n
	}
}

// WithTimeout 设置超时时间
func WithTimeout(dial, read, write time.Duration) Option {
	return func(o *Options) {
		o.DialTimeout = dial
		o.ReadTimeout = read
		o.WriteTimeout = write
	}
}

// WithMaxRetries 设置最大重试次数
func WithMaxRetries(n int) Option {
	return func(o *Options) {
		o.MaxRetries = n
	}
}

// WithLogging 启用日志记录
func WithLogging(logger *RedisLogger) Option {
	return func(o *Options) {
		o.EnableLog = true
		o.Logger = logger
	}
}

// WithProtocol 设置Redis协议版本
func WithProtocol(protocol int) Option {
	return func(o *Options) {
		o.Protocol = protocol
	}
}

// DefaultOptions 返回默认配置
func DefaultOptions() *Options {
	return &Options{
		Addrs:        []string{"localhost:6379"},
		DB:           0,
		PoolSize:     10,
		MinIdleConns: 5,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		MaxRetries:   3,
		EnableLog:    false,
		Logger:       nil,
		Protocol:     2, // 默认使用Redis 2.x协议，兼容旧版本
	}
}

// InitRedis 初始化Redis客户端
func InitRedis(opts ...Option) error {
	// 使用默认配置
	options := DefaultOptions()

	// 应用自定义配置
	for _, opt := range opts {
		opt(options)
	}

	var client redis.UniversalClient

	// 根据地址数量决定使用的模式
	if len(options.Addrs) == 1 {
		// 单机模式
		client = redis.NewClient(&redis.Options{
			Addr:         options.Addrs[0],
			Password:     options.Password,
			DB:           options.DB,
			PoolSize:     options.PoolSize,
			MinIdleConns: options.MinIdleConns,
			DialTimeout:  options.DialTimeout,
			ReadTimeout:  options.ReadTimeout,
			WriteTimeout: options.WriteTimeout,
			MaxRetries:   options.MaxRetries,
			// 禁用自动协议协商，避免HELLO命令
			Protocol: options.Protocol,
			// 禁用客户端信息设置，避免CLIENT SETINFO命令
			DisableIndentity: true,
		})
	} else {
		// 集群模式
		client = redis.NewUniversalClient(&redis.UniversalOptions{
			Addrs:        options.Addrs,
			Password:     options.Password,
			DB:           options.DB,
			PoolSize:     options.PoolSize,
			MinIdleConns: options.MinIdleConns,
			DialTimeout:  options.DialTimeout,
			ReadTimeout:  options.ReadTimeout,
			WriteTimeout: options.WriteTimeout,
			MaxRetries:   options.MaxRetries,
			// 禁用自动协议协商，避免HELLO命令
			Protocol: options.Protocol,
			// 禁用客户端信息设置，避免CLIENT SETINFO命令
			DisableIndentity: true,
		})
	}

	// 如果启用了日志，添加日志Hook
	if options.EnableLog && options.Logger != nil {
		logHook := NewRedisLogHook(options.Logger)
		client.AddHook(logHook)
	}

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), options.DialTimeout)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis connect failed: %v", err)
	}

	// 检测Redis版本（总是获取，用于功能选择）
	var version float64
	info, err := client.Info(ctx, "server").Result()
	if err == nil {
		// 解析Redis版本
		version = parseRedisVersion(info)
	} else {
		// 如果无法获取版本信息，使用默认版本
		version = 4.0
	}

	Redis = &RedisClient{
		client:  client,
		version: version,
	}

	return nil
}

// parseRedisVersion 解析Redis版本信息
func parseRedisVersion(info string) float64 {
	// 简单的版本解析，提取主版本号
	// 例如: "redis_version:5.0.7" -> 5.0
	lines := strings.Split(info, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "redis_version:") {
			parts := strings.Split(strings.TrimPrefix(line, "redis_version:"), ".")
			if len(parts) >= 2 {
				if major, err := strconv.Atoi(parts[0]); err == nil {
					if minor, err := strconv.Atoi(parts[1]); err == nil {
						return float64(major) + float64(minor)/10.0
					}
				}
			}
			break
		}
	}
	return 0.0
}

// Set 设置键值对（Redis 6.0+ 原生支持 KEEPTTL）
func (r *RedisClient) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	// 参数验证
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if value == nil {
		return fmt.Errorf("value cannot be nil")
	}

	return r.client.Set(ctx, key, value, expiration).Err()
}

// SetForLegacy Redis 4.0/5.0 兼容的 Set 函数（手动实现 KEEPTTL 功能）
func (r *RedisClient) SetForLegacy(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	// 参数验证
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if value == nil {
		return fmt.Errorf("value cannot be nil")
	}

	if expiration == redis.KeepTTL {
		// 手动实现 KEEPTTL 功能
		return r.setWithKeepTTL(ctx, key, value)
	}
	return r.client.Set(ctx, key, value, expiration).Err()
}

// SetNX 只在键不存在时设置
func (r *RedisClient) SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) (bool, error) {
	// 参数验证
	if key == "" {
		return false, fmt.Errorf("key cannot be empty")
	}
	if value == nil {
		return false, fmt.Errorf("value cannot be nil")
	}

	return r.client.SetNX(ctx, key, value, expiration).Result()
}

// setWithKeepTTL 手动实现 KEEPTTL 功能（用于低版本Redis）
func (r *RedisClient) setWithKeepTTL(ctx context.Context, key string, value interface{}) error {
	// 先获取当前TTL，然后设置新值
	ttl, err := r.client.TTL(ctx, key).Result()
	if err != nil {
		// 如果获取TTL失败，直接设置值（无过期时间）
		return r.client.Set(ctx, key, value, 0).Err()
	}
	if ttl > 0 {
		// 保持原有TTL
		return r.client.Set(ctx, key, value, ttl).Err()
	}
	// 如果原key没有TTL，直接设置值
	return r.client.Set(ctx, key, value, 0).Err()
}

// Get 获取键的值
func (r *RedisClient) Get(ctx context.Context, key string) (string, error) {
	result, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return result, err
}

// Del 删除键
func (r *RedisClient) Del(ctx context.Context, keys ...string) error {
	return r.client.Del(ctx, keys...).Err()
}

// Incr 原子递增
func (r *RedisClient) Incr(ctx context.Context, key string) (int64, error) {
	return r.client.Incr(ctx, key).Result()
}

// Decr 原子递减
func (r *RedisClient) Decr(ctx context.Context, key string) (int64, error) {
	return r.client.Decr(ctx, key).Result()
}

// HSet 设置哈希字段，支持两种方式：
// 1. 传入字段值对：HSet(ctx, "user:1001", "name", "test", "age", "20")
// 2. 传入map：HSet(ctx, "user:1001", map[string]interface{}{"name": "test", "age": 20})
func (r *RedisClient) HSet(ctx context.Context, key string, values ...interface{}) error {
	// 参数验证
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if len(values) == 0 {
		return nil
	}

	// 如果第一个参数是map，则转换为字段值对
	if len(values) == 1 {
		if m, ok := values[0].(map[string]interface{}); ok {
			var pairs []interface{}
			for k, v := range m {
				pairs = append(pairs, k, v)
			}
			return r.client.HSet(ctx, key, pairs...).Err()
		}
	}

	return r.client.HSet(ctx, key, values...).Err()
}

// HSetNX 只在字段不存在时设置
func (r *RedisClient) HSetNX(ctx context.Context, key, field string, value interface{}) (bool, error) {
	// 参数验证
	if key == "" {
		return false, fmt.Errorf("key cannot be empty")
	}
	if field == "" {
		return false, fmt.Errorf("field cannot be empty")
	}
	return r.client.HSetNX(ctx, key, field, value).Result()
}

// HIncrBy 哈希字段递增
func (r *RedisClient) HIncrBy(ctx context.Context, key, field string, incr int64) (int64, error) {
	// 参数验证
	if key == "" {
		return 0, fmt.Errorf("key cannot be empty")
	}
	if field == "" {
		return 0, fmt.Errorf("field cannot be empty")
	}
	return r.client.HIncrBy(ctx, key, field, incr).Result()
}

// HIncrByFloat 哈希字段浮点递增
func (r *RedisClient) HIncrByFloat(ctx context.Context, key, field string, incr float64) (float64, error) {
	// 参数验证
	if key == "" {
		return 0, fmt.Errorf("key cannot be empty")
	}
	if field == "" {
		return 0, fmt.Errorf("field cannot be empty")
	}
	return r.client.HIncrByFloat(ctx, key, field, incr).Result()
}

// HGet 获取哈希字段的值
// 示例：value, err := HGet(ctx, "user:1001", "name")
func (r *RedisClient) HGet(ctx context.Context, key string, field string) (string, error) {
	// 参数验证
	if key == "" {
		return "", fmt.Errorf("key cannot be empty")
	}
	if field == "" {
		return "", fmt.Errorf("field cannot be empty")
	}

	result, err := r.client.HGet(ctx, key, field).Result()
	if err == redis.Nil {
		return "", nil
	}
	return result, err
}

// HGetList 获取哈希表的所有字段和值
// 示例：values, err := HGetList(ctx, "user:1001")
func (r *RedisClient) HGetList(ctx context.Context, key string) (map[string]string, error) {
	result, err := r.client.HGetAll(ctx, key).Result()
	if err == redis.Nil {
		return make(map[string]string), nil
	}
	return result, err
}

// LPush 向列表左侧推入元素
func (r *RedisClient) LPush(ctx context.Context, key string, values ...interface{}) error {
	return r.client.LPush(ctx, key, values...).Err()
}

// RPop 从列表右侧弹出元素
func (r *RedisClient) RPop(ctx context.Context, key string) (string, error) {
	return r.client.RPop(ctx, key).Result()
}

// Close 关闭Redis连接
func (r *RedisClient) Close() error {
	return r.client.Close()
}

// Lock 尝试获取一个分布式锁, 如果是同一个进程获取同一个锁，会自动续期
func (r *RedisClient) Lock(ctx context.Context, lockKey string, lockValue string, lockExpireTime time.Duration) (bool, error) {
	// 参数验证
	if lockKey == "" {
		return false, fmt.Errorf("lock key cannot be empty")
	}
	if lockValue == "" {
		return false, fmt.Errorf("lock value cannot be empty")
	}

	// lockExpireTime 锁的过期时间, 必须设置，防止死锁
	if lockExpireTime <= 0 {
		lockExpireTime = 10 * time.Second
	}

	// 使用更安全的 SET NX EX 实现，避免竞态条件
	script := `
		local key = KEYS[1]
		local value = ARGV[1]
		local expire_time = ARGV[2]
		
		-- 使用 SET NX EX 原子性设置锁
		local result = redis.call("SET", key, value, "NX", "PX", expire_time)
		if result then
			return 1
		end
		
		-- 如果设置失败，检查是否是同一个进程的锁
		local current_value = redis.call("GET", key)
		if current_value == value then
			-- 同一个进程，续期锁
			redis.call("PEXPIRE", key, expire_time)
			return 1
		end
		
		return 0
	`

	result, err := r.client.Eval(ctx, script, []string{lockKey}, lockValue, lockExpireTime.Milliseconds()).Result()
	if err != nil {
		return false, fmt.Errorf("lock failed: %v", err)
	}

	return result == int64(1), nil
}

// Unlock 释放分布式锁
func (r *RedisClient) Unlock(ctx context.Context, lockKey string, currentProcessLockValue string) error {
	// 使用Lua脚本保证原子性，并检查锁是否过期
	script := `
		local key = KEYS[1]
		local value = ARGV[1]
		
		-- 检查锁是否存在且值匹配
		local current_value = redis.call("GET", key)
		if current_value == value then
			-- 检查锁是否过期
			local ttl = redis.call("PTTL", key)
			if ttl > 0 then
				-- 锁有效，删除它
				return redis.call("DEL", key)
			else
				-- 锁已过期，返回错误
				return -1
			end
		else
			-- 值不匹配或锁不存在
			return 0
		end
	`

	result, err := r.client.Eval(ctx, script, []string{lockKey}, currentProcessLockValue).Result()
	if err != nil {
		return fmt.Errorf("unlock failed: %v", err)
	}

	switch result {
	case int64(1):
		return nil // 成功删除锁
	case int64(0):
		return fmt.Errorf("lock not owned by current process or already released")
	case int64(-1):
		return fmt.Errorf("lock has expired")
	default:
		return fmt.Errorf("unexpected unlock result: %v", result)
	}
}

// TryLock 尝试获取锁，带超时时间
func (r *RedisClient) TryLock(ctx context.Context, lockKey string, lockValue string, lockExpireTime time.Duration, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		acquired, err := r.Lock(ctx, lockKey, lockValue, lockExpireTime)
		if err != nil {
			return false, err
		}
		if acquired {
			return true, nil
		}

		// 短暂等待后重试
		time.Sleep(10 * time.Millisecond)
	}

	return false, fmt.Errorf("lock acquisition timeout after %v", timeout)
}

// RenewLock 续期分布式锁
func (r *RedisClient) RenewLock(ctx context.Context, lockKey string, lockValue string, lockExpireTime time.Duration) (bool, error) {
	script := `
		local key = KEYS[1]
		local value = ARGV[1]
		local expire_time = ARGV[2]
		
		-- 检查锁是否存在且值匹配
		local current_value = redis.call("GET", key)
		if current_value == value then
			-- 检查锁是否过期
			local ttl = redis.call("PTTL", key)
			if ttl > 0 then
				-- 锁有效，续期
				redis.call("PEXPIRE", key, expire_time)
				return 1
			else
				-- 锁已过期
				return -1
			end
		else
			-- 值不匹配或锁不存在
			return 0
		end
	`

	result, err := r.client.Eval(ctx, script, []string{lockKey}, lockValue, lockExpireTime.Milliseconds()).Result()
	if err != nil {
		return false, fmt.Errorf("renew lock failed: %v", err)
	}

	switch result {
	case int64(1):
		return true, nil // 成功续期
	case int64(0):
		return false, fmt.Errorf("lock not owned by current process")
	case int64(-1):
		return false, fmt.Errorf("lock has expired")
	default:
		return false, fmt.Errorf("unexpected renew result: %v", result)
	}
}

func (r *RedisClient) AddHook(hook redis.Hook) {
	r.client.AddHook(hook)
}

// GetClient 获取原始Redis客户端
func (r *RedisClient) GetClient() redis.UniversalClient {
	return r.client
}

// IsVersionSupported 检查是否支持指定版本
func (r *RedisClient) IsVersionSupported(minVersion float64) bool {
	return r.version >= minVersion
}

// GetVersion 获取Redis版本号
func (r *RedisClient) GetVersion() float64 {
	return r.version
}

// GetVersionString 获取Redis版本字符串
func (r *RedisClient) GetVersionString() string {
	return fmt.Sprintf("%.1f", r.version)
}

// GetVersionInfo 获取Redis版本详细信息
func (r *RedisClient) GetVersionInfo() map[string]interface{} {
	return map[string]interface{}{
		"version":           r.version,
		"version_str":       r.GetVersionString(),
		"is_legacy":         r.version < 6.0,
		"supports_keep_ttl": r.version >= 6.0,
	}
}

/*
分布式锁使用示例：

// 1. 基本使用
lockKey := "order:123:lock"
lockValue := uuid.New().String()
lockExpireTime := 30 * time.Second

// 获取锁
acquired, err := Redis.Lock(ctx, lockKey, lockValue, lockExpireTime)
if err != nil {
    log.Printf("获取锁失败: %v", err)
    return
}
if !acquired {
    log.Printf("锁已被占用")
    return
}

// 确保释放锁
defer func() {
    if err := Redis.Unlock(ctx, lockKey, lockValue); err != nil {
        log.Printf("释放锁失败: %v", err)
    }
}()

// 执行业务逻辑
// ... 处理订单 ...

// 2. 带超时的锁获取
acquired, err := Redis.TryLock(ctx, lockKey, lockValue, lockExpireTime, 5*time.Second)
if err != nil {
    log.Printf("获取锁失败: %v", err)
    return
}
if !acquired {
    log.Printf("获取锁超时")
    return
}

// 3. 锁续期（用于长时间任务）
go func() {
    ticker := time.NewTicker(lockExpireTime / 3) // 每1/3过期时间续期一次
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            renewed, err := Redis.RenewLock(ctx, lockKey, lockValue, lockExpireTime)
            if err != nil || !renewed {
                log.Printf("锁续期失败，停止续期: %v", err)
                return
            }
        case <-ctx.Done():
            return
        }
    }
}()

注意事项：
1. lockValue 应该是唯一的，建议使用UUID
2. lockExpireTime 应该大于业务执行时间
3. 使用 defer 确保锁被释放
4. 对于长时间任务，考虑使用锁续期机制
5. 在生产环境中，建议使用Redis集群或Sentinel提高可用性
*/
