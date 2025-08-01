package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// RedisEngine Redis队列引擎实现
type RedisEngine struct {
	client *redisx.RedisClient
}

// NewRedisEngine 创建Redis队列引擎
func NewRedisEngine(client *redisx.RedisClient) Engine {
	return &RedisEngine{
		client: client,
	}
}

// Push 将数据推入队列(source 和 failed)
func (e *RedisEngine) Push(ctx context.Context, queue string, data []byte) error {
	return e.client.GetClient().LPush(ctx, queue, data).Err()
}

// Pop 从队列中弹出数据(source 和 failed)
func (e *RedisEngine) Pop(ctx context.Context, queue string, timeout time.Duration) ([]byte, error) {
	// 使用BRPOP命令从队列中弹出数据, 如果队列为空，则阻塞等待timeout时间
	result, err := e.client.GetClient().BRPop(ctx, timeout, queue).Result()
	if err != nil {
		return nil, err
	}
	// BRPOP 返回 [key, value]，我们需要第二个元素
	return []byte(result[1]), nil
}

// BatchPop 批量从队列中弹出数据(source 和 failed)
func (e *RedisEngine) BatchPop(ctx context.Context, queue string, count int, timeout time.Duration) ([][]byte, error) {
	var result [][]byte
	for i := 0; i < count; i++ {
		data, err := e.Pop(ctx, queue, timeout)
		if err != nil {
			if err == redis.Nil {
				break
			}
			return result, err
		}
		result = append(result, data)
	}
	return result, nil
}

// PushDelayed 将数据推入延迟队列
// 使用Redis有序集合(Sorted Set)实现延迟队列
// 参数说明:
//   - ctx: 上下文，用于控制超时和取消
//   - queue: 队列名称，会自动添加"_delayed"后缀
//   - data: 要存储的消息数据
//   - delay: 延迟时间，消息将在当前时间+delay后到期
func (e *RedisEngine) PushDelayed(ctx context.Context, queue string, data []byte, delay time.Duration) error {
	// 计算到期时间戳作为分数(score)
	// score = 当前时间 + 延迟时间，用于排序
	score := float64(time.Now().Add(delay).Unix())

	// ZADD命令：向有序集合添加成员
	// 参数说明:
	//   - ctx: 上下文
	//   - queue+"_delayed": 有序集合的键名，例如"test_retry_delayed"
	//   - redis.Z{Score: score, Member: data}: 有序集合成员
	//     - Score: 分数(时间戳)，用于排序和到期判断
	//     - Member: 成员(消息数据)，实际存储的内容
	return e.client.GetClient().ZAdd(ctx, queue+"_delayed", redis.Z{
		Score:  score,
		Member: data,
	}).Err()
}

// PopDelayed 从延迟队列中弹出到期的数据
// 使用Redis有序集合的ZRANGEBYSCORE和ZREM命令实现
// 参数说明:
//   - ctx: 上下文，用于控制超时和取消
//   - queue: 队列名称，会自动添加"_delayed"后缀
//   - count: 最多返回的消息数量，用于分页控制
func (e *RedisEngine) PopDelayed(ctx context.Context, queue string, count int) ([][]byte, error) {
	// 获取当前时间戳，用于判断哪些消息已到期
	now := float64(time.Now().Unix())

	// ZRANGEBYSCORE命令：按分数范围查询有序集合成员
	// 参数说明:
	//   - ctx: 上下文
	//   - queue+"_delayed": 有序集合的键名
	//   - &redis.ZRangeBy: 查询条件
	//     - Min: "0" - 最小分数，从0开始查询
	//     - Max: fmt.Sprintf("%.0f", now) - 最大分数，当前时间戳
	//     - Offset: 0 - 偏移量，从第0个开始
	//     - Count: int64(count) - 最多返回count条记录
	// 查询逻辑：返回所有分数 <= 当前时间的消息（即已到期的消息）
	results, err := e.client.GetClient().ZRangeByScore(ctx, queue+"_delayed", &redis.ZRangeBy{
		Min:    "0",
		Max:    fmt.Sprintf("%.0f", now),
		Offset: 0,
		Count:  int64(count),
	}).Result()

	if err != nil {
		return nil, err
	}

	// 如果没有到期的消息，返回redis.Nil
	if len(results) == 0 {
		return nil, redis.Nil
	}

	// ZREM命令：从有序集合中删除指定的成员
	// 参数说明:
	//   - ctx: 上下文
	//   - queue+"_delayed": 有序集合的键名
	//   - members...: 要删除的成员列表
	// 作用：删除已获取的消息，避免重复处理
	var members []interface{}
	for _, result := range results {
		members = append(members, result)
	}

	// 删除已获取的消息
	err = e.client.GetClient().ZRem(ctx, queue+"_delayed", members...).Err()
	if err != nil {
		return nil, err
	}

	// 将字符串结果转换为字节数组
	var data [][]byte
	for _, result := range results {
		data = append(data, []byte(result))
	}

	return data, nil
}

func (e *RedisEngine) Close() error {
	return nil
}
