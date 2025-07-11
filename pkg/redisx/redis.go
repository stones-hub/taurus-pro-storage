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
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisClient Redis客户端结构
type RedisClient struct {
	client redis.UniversalClient
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
}

// Option 定义配置选项函数类型
type Option func(*Options)

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
	}
}

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
		})
	}

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), options.DialTimeout)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis connect failed: %v", err)
	}

	Redis = &RedisClient{client: client}

	return nil
}

// Set 设置键值对
func (r *RedisClient) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return r.client.Set(ctx, key, value, expiration).Err()
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

// HGet 获取哈希字段的值
// 示例：value, err := HGet(ctx, "user:1001", "name")
func (r *RedisClient) HGet(ctx context.Context, key string, field string) (string, error) {
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

// Client 获取原始的Redis客户端
func (r *RedisClient) Client() redis.UniversalClient {
	return r.client
}

// Lock 尝试获取一个分布式锁
func (r *RedisClient) Lock(ctx context.Context, lockKey string, lockValue string, lockExpireTime time.Duration) (bool, error) {
	// lockExpireTime 锁的过期时间, 必须设置，防止死锁
	if lockExpireTime <= 0 {
		lockExpireTime = 10 * time.Second
	}

	// SetNX 设置一个键值对，如果键不存在，则设置键值对，并返回true，否则返回false
	if flag, err := r.client.SetNX(ctx, lockKey, lockValue, lockExpireTime).Result(); err != nil {
		return false, err
	} else {
		return flag, nil
	}
}

// Unlock 释放分布式锁
func (r *RedisClient) Unlock(ctx context.Context, lockKey string, currentProcessLockValue string) error {
	// 获取锁的值, 检查是否是当前线程持有的锁
	val, err := r.client.Get(ctx, lockKey).Result()
	if err != nil {
		return err
	}

	if val == currentProcessLockValue {
		// 删除锁
		_, err = r.client.Del(ctx, lockKey).Result()
	}

	return err
}

func (r *RedisClient) AddHook(hook redis.Hook) {
	r.client.AddHook(hook)
}

func (r *RedisClient) GetClient() redis.UniversalClient {
	return r.client
}
