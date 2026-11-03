package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stones-hub/taurus-pro-storage/pkg/queue/common"
	"github.com/stones-hub/taurus-pro-storage/pkg/redisx"
)

// Redis Stream 可靠引擎
//
// 数据模型（命名空间 taurus:q2:{<name>}:，与旧版 List 键 <name>/<name>_delayed 完全隔离）：
//
//	ready    Stream   就绪队列，条目只含任务ID；消费者组 workers 负责 pending 持久化
//	delayed  ZSet     延迟重试，member=任务ID，score=到期毫秒
//	leases   ZSet     已领取任务的租约，member=任务ID，score=租约到期毫秒
//	failed   ZSet     失败留存索引，member=任务ID，score=失败毫秒
//	m:<id>   Hash     任务记录（内容、状态、领取凭证、重试计数）；成功确认后删除
//	d:<id>   String   稳定ID任务完成后的去重墓碑（内容摘要），带 PX 过期
//
// 所有状态迁移都在单个 Lua 脚本里完成，且脚本先做只读校验（键类型、领取权、记录完整性：
// 状态/payload/n/r 合法）再写入，因为 Redis 脚本出错不会回滚已写入的命令。发现损坏记录时
// 返回 common.ErrCorruptRecord，保留入口、索引和 payload 原样，由人工修复后自然恢复。
// 保证范围：键级类型与上述字段级校验；对 Redis 数据被外部任意篡改不做更多承诺。
// 幂等的消费者组创建与“索引指向的记录已不存在”的清理（无 payload 可保护）除外。领取（XREADGROUP + 写凭证 + 写租约）在同一脚本内
// 完成，所以不存在“已读取但凭证未登记”时崩溃的窗口；租约到期的任务按 leases 的 score 顺序
// 被重新领取，不会反复扫描同一页。
//
// 仅支持单机/主从直连（单个逻辑 key 空间）。Redis Cluster 被明确拒绝。
// 实测范围仅限 Redis 8.10.2；6.2 只是准入检查（拒绝更低版本），未经测试，不代表已支持。
const (
	streamKeyPrefix = "taurus:q2:"
	streamGroup     = "workers"
	streamConsumer  = "c"
	promoteBatch    = 100
	minMajor        = 6
	minMinor        = 2
)

const luaPrelude = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local function ktype(k) return redis.call('TYPE', k)['ok'] end
local function bad(k, want) local t = ktype(k); return t ~= 'none' and t ~= want end
local function wrongtype() return redis.error_reply('WRONGTYPE unexpected key type in queue namespace') end
local function corrupt(msg) return redis.error_reply('CORRUPT ' .. msg) end
-- 与 Redis HINCRBY/string2ll 一致的“规范十进制 int64 字符串”：无符号、无前导零（除 "0" 本身）、
-- 不超过 int64 范围。按位数和字典序精确比较，不用 tonumber（double 会在 2^53 以上失真）。
local I64_MAX = '9223372036854775807'
local I64_MAX_MINUS_1 = '9223372036854775806'
local function isint(v, limit)
  if type(v) ~= 'string' or not string.match(v, '^[0-9]+$') then return false end
  if #v > 1 and string.sub(v, 1, 1) == '0' then return false end
  limit = limit or I64_MAX
  if #v > #limit then return false end
  if #v == #limit and v > limit then return false end
  return true
end
-- 将被 HINCRBY +1 的计数必须 <= int64 最大值 - 1，否则 HINCRBY 会在部分写入之后才报错
local function isinc(v) return isint(v, I64_MAX_MINUS_1) end
-- 记录完整性：状态必须是 want，payload 存在，n/r 是规范 int64；incN/incR 表示该路径会对其 +1。
-- 返回 nil 表示通过。必须在任何相关写入之前调用。
local function check_record(m, want, incN, incR)
  local f = redis.call('HMGET', m, 's', 'd', 'n', 'r')
  if f[1] ~= want then return 'state is ' .. tostring(f[1]) .. ', want ' .. want end
  if not f[2] or f[2] == '' then return 'payload missing' end
  if not (incN and isinc(f[3]) or (not incN) and isint(f[3])) then
    return 'field n is not a canonical int64' .. (incN and ' <= max-1' or '')
  end
  if not (incR and isinc(f[4]) or (not incR) and isint(f[4])) then
    return 'field r is not a canonical int64' .. (incR and ' <= max-1' or '')
  end
  return nil
end
local function release_entry(ready, group, entry)
  if entry and entry ~= '' then
    redis.call('XACK', ready, group, entry)
    redis.call('XDEL', ready, entry)
  end
end
`

// KEYS: ready, delayed, leases   ARGV: group, token, leaseMs, mp, batch, consumer
//
// 三步（转回到期重试 → 恢复到期租约 → 领取新任务）都遵循“先校验、后写入”：
//   - 即将转移/领取的记录必须完整合法（状态、payload、n、r），否则返回 CORRUPT/WRONGTYPE 错误，
//     不修改该记录、不 XACK/XDEL、不 ZREM，现场原样保留；
//   - 领取新任务时先用 XINFO GROUPS + XRANGE 预检队首条目，通过后才 XREADGROUP，
//     所以不会出现“已进入 PEL 但没有租约”的孤儿；
//   - 唯一的静默清理是“索引指向的记录已不存在”（没有 payload 可保护），会写入 Redis 服务端日志（redis.log）。
var receiveScript = redis.NewScript(luaPrelude + `
local ready, delayed, leases = KEYS[1], KEYS[2], KEYS[3]
local group, token, lease, mp, batch, consumer = ARGV[1], ARGV[2], tonumber(ARGV[3]), ARGV[4], tonumber(ARGV[5]), ARGV[6]
if bad(ready, 'stream') or bad(delayed, 'zset') or bad(leases, 'zset') then return wrongtype() end
local now = now_ms()

-- 1) 到期的延迟重试转回 ready：先校验全部，再写入
local due = redis.call('ZRANGEBYSCORE', delayed, '-inf', now, 'LIMIT', 0, batch)
local promote, stale = {}, {}
for _, id in ipairs(due) do
  local m = mp .. id
  local ty = ktype(m)
  if ty == 'none' then
    stale[#stale + 1] = id
  elseif ty ~= 'hash' then
    return wrongtype()
  else
    local e = check_record(m, 'delayed', true, false)
    if e then return corrupt(m .. ': ' .. e) end
    promote[#promote + 1] = id
  end
end
local g = redis.pcall('XGROUP', 'CREATE', ready, group, '0', 'MKSTREAM')
if type(g) == 'table' and g.err and not string.find(g.err, 'BUSYGROUP', 1, true) then return redis.error_reply(g.err) end
for _, id in ipairs(promote) do
  redis.call('XADD', ready, '*', 'id', id)
  redis.call('HSET', mp .. id, 's', 'ready')
  redis.call('ZREM', delayed, id)
end
for _, id in ipairs(stale) do
  redis.log(redis.LOG_WARNING, 'taurus-queue: delayed index without record, removing: ' .. id)
  redis.call('ZREM', delayed, id)
end

-- 2) 恢复租约到期的任务（按到期时间顺序，每次一个）
for i = 1, 20 do
  local cand = redis.call('ZRANGEBYSCORE', leases, '-inf', now, 'LIMIT', 0, 1)
  if #cand == 0 then break end
  local id = cand[1]
  local m = mp .. id
  local ty = ktype(m)
  if ty == 'none' then
    redis.log(redis.LOG_WARNING, 'taurus-queue: lease index without record, removing: ' .. id)
    redis.call('ZREM', leases, id)
  elseif ty ~= 'hash' then
    return wrongtype()
  else
    local e = check_record(m, 'inflight', true, false)
    if e then return corrupt(m .. ': ' .. e) end
    local entry = redis.call('HGET', m, 'e')
    redis.call('HSET', m, 'tok', token)
    local n = redis.call('HINCRBY', m, 'n', 1)
    redis.call('ZADD', leases, now + lease, id)
    if entry then redis.pcall('XCLAIM', ready, group, consumer, 0, entry) end
    return {id, redis.call('HGET', m, 'd'), n, redis.call('HGET', m, 'r'), '1'}
  end
end

-- 3) 领取新任务：先预检队首（尚未投递给该组的第一条），通过后才 XREADGROUP
for i = 1, 20 do
  local last = nil
  for _, gi in ipairs(redis.call('XINFO', 'GROUPS', ready)) do
    local name, ld
    for j = 1, #gi, 2 do
      if gi[j] == 'name' then name = gi[j + 1] elseif gi[j] == 'last-delivered-id' then ld = gi[j + 1] end
    end
    if name == group then last = ld end
  end
  if not last then return corrupt('consumer group ' .. group .. ' missing on ' .. ready) end
  local head = redis.call('XRANGE', ready, '(' .. last, '+', 'COUNT', 1)
  if #head == 0 then return false end
  local entry, fields, id = head[1][1], head[1][2], nil
  for j = 1, #fields, 2 do if fields[j] == 'id' then id = fields[j + 1] end end
  if not id then return corrupt('stream entry ' .. entry .. ' has no id field') end
  local m = mp .. id
  local ty = ktype(m)
  if ty == 'none' then
    redis.log(redis.LOG_WARNING, 'taurus-queue: undelivered stream entry without record, removing: ' .. entry .. ' ' .. id)
    redis.call('XDEL', ready, entry)
  elseif ty ~= 'hash' then
    return wrongtype()
  else
    local e = check_record(m, 'ready', true, false)
    if e then return corrupt(m .. ': ' .. e) end
    local res = redis.call('XREADGROUP', 'GROUP', group, consumer, 'COUNT', 1, 'STREAMS', ready, '>')
    if not res or res[1][2][1][1] ~= entry then return corrupt('stream head changed during claim: ' .. entry) end
    redis.call('HSET', m, 's', 'inflight', 'tok', token, 'e', entry)
    local n = redis.call('HINCRBY', m, 'n', 1)
    redis.call('ZADD', leases, now + lease, id)
    return {id, redis.call('HGET', m, 'd'), n, redis.call('HGET', m, 'r'), '0'}
  end
end
return false
`)

// KEYS: ready   ARGV: mp, dp, id, data, hash, dedup, windowMs
// 返回 'ok' | 'dup' | 'conflict'
var enqueueScript = redis.NewScript(luaPrelude + `
local ready = KEYS[1]
local mp, dp, id, data, hash, dedup = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6]
local m, d = mp .. id, dp .. id
if bad(ready, 'stream') then return wrongtype() end
local ty = ktype(m)
if ty == 'hash' then
  if redis.call('HGET', m, 'h') == hash then return 'dup' end
  return 'conflict'
elseif ty ~= 'none' then return wrongtype() end
if dedup == '1' then
  local dty = ktype(d)
  if dty == 'string' then
    if redis.call('GET', d) == hash then return 'dup' end
    return 'conflict'
  elseif dty ~= 'none' then return wrongtype() end
end
redis.call('XADD', ready, '*', 'id', id)
redis.call('HSET', m, 's', 'ready', 'd', data, 'h', hash, 'dd', dedup, 'n', 0, 'r', 0, 'rp', 0, 'c', now_ms())
return 'ok'
`)

// KEYS: ready, leases   ARGV: group, mp, dp, id, token, windowMs
// 返回 1 成功 | 0 凭证失效 | 2 任务已不存在
var ackScript = redis.NewScript(luaPrelude + `
local ready, leases = KEYS[1], KEYS[2]
local group, mp, dp, id, token, window = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], tonumber(ARGV[6])
local m = mp .. id
local ty = ktype(m)
if ty == 'none' then return 2 end
if ty ~= 'hash' or bad(ready, 'stream') or bad(leases, 'zset') then return wrongtype() end
if redis.call('HGET', m, 's') ~= 'inflight' or redis.call('HGET', m, 'tok') ~= token then return 0 end
if redis.call('HGET', m, 'dd') == '1' and window > 0 then
  if bad(dp .. id, 'string') then return wrongtype() end
  redis.call('SET', dp .. id, redis.call('HGET', m, 'h'), 'PX', window)
end
release_entry(ready, group, redis.call('HGET', m, 'e'))
redis.call('ZREM', leases, id)
redis.call('DEL', m)
return 1
`)

// KEYS: leases   ARGV: mp, id, token, leaseMs
var renewScript = redis.NewScript(luaPrelude + `
local leases = KEYS[1]
local mp, id, token, lease = ARGV[1], ARGV[2], ARGV[3], tonumber(ARGV[4])
local m = mp .. id
local ty = ktype(m)
if ty == 'none' then return 0 end
if ty ~= 'hash' or bad(leases, 'zset') then return wrongtype() end
if redis.call('HGET', m, 's') ~= 'inflight' or redis.call('HGET', m, 'tok') ~= token then return 0 end
redis.call('ZADD', leases, now_ms() + lease, id)
return 1
`)

// KEYS: ready, delayed, leases   ARGV: group, mp, id, token, delayMs, reason
var retryScript = redis.NewScript(luaPrelude + `
local ready, delayed, leases = KEYS[1], KEYS[2], KEYS[3]
local group, mp, id, token, delay, reason = ARGV[1], ARGV[2], ARGV[3], ARGV[4], tonumber(ARGV[5]), ARGV[6]
local m = mp .. id
local ty = ktype(m)
if ty == 'none' then return 0 end
if ty ~= 'hash' or bad(ready, 'stream') or bad(delayed, 'zset') or bad(leases, 'zset') then return wrongtype() end
if redis.call('HGET', m, 's') ~= 'inflight' or redis.call('HGET', m, 'tok') ~= token then return 0 end
local ce = check_record(m, 'inflight', false, true)
if ce then return corrupt(m .. ': ' .. ce) end
release_entry(ready, group, redis.call('HGET', m, 'e'))
redis.call('HSET', m, 's', 'delayed', 'err', reason)
redis.call('HDEL', m, 'tok', 'e')
redis.call('HINCRBY', m, 'r', 1)
redis.call('ZADD', delayed, now_ms() + delay, id)
redis.call('ZREM', leases, id)
return 1
`)

// KEYS: ready, leases, failed   ARGV: group, mp, id, token, reason, retentionMs
var failScript = redis.NewScript(luaPrelude + `
local ready, leases, failed = KEYS[1], KEYS[2], KEYS[3]
local group, mp, id, token, reason, retention = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], tonumber(ARGV[6])
local m = mp .. id
local ty = ktype(m)
if ty == 'none' then return 0 end
if ty ~= 'hash' or bad(ready, 'stream') or bad(leases, 'zset') or bad(failed, 'zset') then return wrongtype() end
if redis.call('HGET', m, 's') ~= 'inflight' or redis.call('HGET', m, 'tok') ~= token then return 0 end
local now = now_ms()
local old = {}
if retention > 0 then
  old = redis.call('ZRANGEBYSCORE', failed, '-inf', now - retention, 'LIMIT', 0, 100)
  for _, oid in ipairs(old) do if bad(mp .. oid, 'hash') then return wrongtype() end end
end
release_entry(ready, group, redis.call('HGET', m, 'e'))
redis.call('HSET', m, 's', 'failed', 'err', reason, 'fa', now)
redis.call('HDEL', m, 'tok', 'e')
redis.call('ZADD', failed, now, id)
redis.call('ZREM', leases, id)
for _, oid in ipairs(old) do
  if redis.call('HGET', mp .. oid, 's') == 'failed' then redis.call('DEL', mp .. oid) end
  redis.call('ZREM', failed, oid)
end
return 1
`)

// KEYS: failed   ARGV: mp, limit
var listFailedScript = redis.NewScript(luaPrelude + `
if bad(KEYS[1], 'zset') then return wrongtype() end
local ids = redis.call('ZRANGE', KEYS[1], 0, tonumber(ARGV[2]) - 1)
local out = {}
for _, id in ipairs(ids) do
  local m = ARGV[1] .. id
  if ktype(m) == 'hash' and redis.call('HGET', m, 's') == 'failed' then
    out[#out + 1] = {id, redis.call('HGET', m, 'd'), redis.call('HGET', m, 'n') or '0',
      redis.call('HGET', m, 'err') or '', redis.call('HGET', m, 'fa') or '0'}
  end
end
return out
`)

// KEYS: ready, failed   ARGV: mp, id
// 返回 1 成功 | 0 不处于失败状态
var replayScript = redis.NewScript(luaPrelude + `
local ready, failed = KEYS[1], KEYS[2]
local m = ARGV[1] .. ARGV[2]
local ty = ktype(m)
if ty == 'none' then return 0 end
if ty ~= 'hash' or bad(ready, 'stream') or bad(failed, 'zset') then return wrongtype() end
if redis.call('HGET', m, 's') ~= 'failed' then return 0 end
local ce = check_record(m, 'failed', false, false)
if ce then return corrupt(m .. ': ' .. ce) end
local rp = redis.call('HGET', m, 'rp')
if rp and not isinc(rp) then return corrupt(m .. ': field rp is not a canonical int64 <= max-1') end
redis.call('XADD', ready, '*', 'id', ARGV[2])
redis.call('HSET', m, 's', 'ready', 'r', 0)
redis.call('HINCRBY', m, 'rp', 1)
redis.call('ZREM', failed, ARGV[2])
return 1
`)

// RedisStreamEngine 基于 Redis Stream 消费者组的至少一次投递引擎
type RedisStreamEngine struct {
	client *redisx.RedisClient
	opts   ReliableOptions

	ready, delayed, leases, failed string
	msgPrefix, donePrefix          string
}

// NewRedisStreamEngine 创建 Redis Stream 引擎，校验队列名称、参数、拓扑与版本。
// 不会关闭传入的 client。
func NewRedisStreamEngine(ctx context.Context, client *redisx.RedisClient, opts ReliableOptions) (*RedisStreamEngine, error) {
	if client == nil {
		return nil, errors.New("redis client is nil: call redisx.InitRedis or set Config.RedisClient")
	}
	if err := ValidateID(opts.Name); err != nil {
		return nil, fmt.Errorf("queue name %q: %w", opts.Name, err)
	}
	if opts.LeaseDuration < 500*time.Millisecond {
		return nil, fmt.Errorf("lease duration must be >= 500ms, got %v", opts.LeaseDuration)
	}
	if opts.DedupWindow < 0 || opts.FailedRetention < 0 {
		return nil, errors.New("dedup window and failed retention cannot be negative")
	}
	if err := checkTopology(ctx, client.GetClient()); err != nil {
		return nil, err
	}
	p := streamKeyPrefix + "{" + opts.Name + "}:"
	return &RedisStreamEngine{
		client: client, opts: opts,
		ready: p + "ready", delayed: p + "delayed", leases: p + "leases", failed: p + "failed",
		msgPrefix: p + "m:", donePrefix: p + "d:",
	}, nil
}

// checkTopology 拒绝 Cluster 与低于 6.2 的版本
func checkTopology(ctx context.Context, c redis.UniversalClient) error {
	if _, ok := c.(*redis.ClusterClient); ok {
		return fmt.Errorf("%w: redis cluster client", common.ErrUnsupportedTopology)
	}
	info, err := c.Info(ctx, "server", "cluster").Result()
	if err != nil {
		return fmt.Errorf("query redis info: %w", err)
	}
	if strings.Contains(info, "cluster_enabled:1") {
		return fmt.Errorf("%w: cluster_enabled=1", common.ErrUnsupportedTopology)
	}
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "redis_version:"); ok {
			parts := strings.Split(v, ".")
			if len(parts) < 2 {
				break
			}
			major, e1 := strconv.Atoi(parts[0])
			minor, e2 := strconv.Atoi(parts[1])
			if e1 != nil || e2 != nil {
				break
			}
			if major < minMajor || (major == minMajor && minor < minMinor) {
				return fmt.Errorf("%w: redis %s < %d.%d", common.ErrUnsupportedTopology, v, minMajor, minMinor)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: cannot determine redis version", common.ErrUnsupportedTopology)
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func (e *RedisStreamEngine) rdb() redis.UniversalClient { return e.client.GetClient() }

// scriptErr 将脚本返回的 CORRUPT 错误映射为 common.ErrCorruptRecord（保留原始信息）
func scriptErr(err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "CORRUPT ") {
		return fmt.Errorf("%w: %s", common.ErrCorruptRecord, strings.TrimPrefix(err.Error(), "CORRUPT "))
	}
	return err
}

// Enqueue 实现 ReliableEngine
func (e *RedisStreamEngine) Enqueue(ctx context.Context, id string, data []byte, dedup bool) (bool, error) {
	if err := ValidateID(id); err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, common.ErrItemEmpty
	}
	flag := "0"
	if dedup {
		flag = "1"
	}
	res, err := enqueueScript.Run(ctx, e.rdb(), []string{e.ready},
		e.msgPrefix, e.donePrefix, id, data, contentHash(data), flag).Result()
	if err != nil {
		return false, err
	}
	switch res {
	case "ok":
		return false, nil
	case "dup":
		return true, nil
	case "conflict":
		return false, fmt.Errorf("%w: id=%s", common.ErrIDConflict, id)
	}
	return false, fmt.Errorf("unexpected enqueue result: %v", res)
}

// Receive 实现 ReliableEngine
func (e *RedisStreamEngine) Receive(ctx context.Context) (*Delivery, error) {
	token := newToken()
	res, err := receiveScript.Run(ctx, e.rdb(), []string{e.ready, e.delayed, e.leases},
		streamGroup, token, e.opts.LeaseDuration.Milliseconds(), e.msgPrefix, promoteBatch, streamConsumer).Result()
	if errors.Is(err, redis.Nil) {
		return nil, common.ErrQueueEmpty
	}
	if err != nil {
		return nil, scriptErr(err)
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) != 5 {
		return nil, fmt.Errorf("unexpected receive result: %v", res)
	}
	id, _ := arr[0].(string)
	data, _ := arr[1].(string)
	n, _ := arr[2].(int64)
	retriesStr, _ := arr[3].(string)
	retries, _ := strconv.Atoi(retriesStr)
	recovered, _ := arr[4].(string)
	return &Delivery{ID: id, Data: []byte(data), Token: token, Attempt: int(n), Retries: retries, Recovered: recovered == "1"}, nil
}

// Ack 实现 ReliableEngine
func (e *RedisStreamEngine) Ack(ctx context.Context, d *Delivery) error {
	res, err := ackScript.Run(ctx, e.rdb(), []string{e.ready, e.leases},
		streamGroup, e.msgPrefix, e.donePrefix, d.ID, d.Token, e.opts.DedupWindow.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if res == 0 {
		return common.ErrLeaseLost
	}
	return nil // 1 成功；2 任务已不存在（已被确认），幂等
}

// Renew 实现 ReliableEngine
func (e *RedisStreamEngine) Renew(ctx context.Context, d *Delivery) error {
	res, err := renewScript.Run(ctx, e.rdb(), []string{e.leases},
		e.msgPrefix, d.ID, d.Token, e.opts.LeaseDuration.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if res != 1 {
		return common.ErrLeaseLost
	}
	return nil
}

// Retry 实现 ReliableEngine
func (e *RedisStreamEngine) Retry(ctx context.Context, d *Delivery, delay time.Duration, reason string) error {
	if delay < 0 {
		delay = 0
	}
	res, err := retryScript.Run(ctx, e.rdb(), []string{e.ready, e.delayed, e.leases},
		streamGroup, e.msgPrefix, d.ID, d.Token, delay.Milliseconds(), reason).Int64()
	if err != nil {
		return scriptErr(err)
	}
	if res != 1 {
		return common.ErrLeaseLost
	}
	return nil
}

// Fail 实现 ReliableEngine
func (e *RedisStreamEngine) Fail(ctx context.Context, d *Delivery, reason string) error {
	res, err := failScript.Run(ctx, e.rdb(), []string{e.ready, e.leases, e.failed},
		streamGroup, e.msgPrefix, d.ID, d.Token, reason, e.opts.FailedRetention.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if res != 1 {
		return common.ErrLeaseLost
	}
	return nil
}

// ListFailed 实现 ReliableEngine
func (e *RedisStreamEngine) ListFailed(ctx context.Context, limit int) ([]FailedMessage, error) {
	if limit < 1 || limit > MaxListFailed {
		return nil, fmt.Errorf("limit must be in [1, %d], got %d", MaxListFailed, limit)
	}
	res, err := listFailedScript.Run(ctx, e.rdb(), []string{e.failed}, e.msgPrefix, limit).Result()
	if err != nil {
		return nil, err
	}
	rows, _ := res.([]interface{})
	out := make([]FailedMessage, 0, len(rows))
	for _, row := range rows {
		f, ok := row.([]interface{})
		if !ok || len(f) != 5 {
			return nil, fmt.Errorf("unexpected failed row: %v", row)
		}
		id, _ := f[0].(string)
		data, _ := f[1].(string)
		ns, _ := f[2].(string)
		reason, _ := f[3].(string)
		fas, _ := f[4].(string)
		n, _ := strconv.Atoi(ns)
		fa, _ := strconv.ParseInt(fas, 10, 64)
		out = append(out, FailedMessage{ID: id, Data: []byte(data), Attempts: n, Reason: reason, FailedAt: time.UnixMilli(fa)})
	}
	return out, nil
}

// ReplayFailed 实现 ReliableEngine
func (e *RedisStreamEngine) ReplayFailed(ctx context.Context, id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	res, err := replayScript.Run(ctx, e.rdb(), []string{e.ready, e.failed}, e.msgPrefix, id).Int64()
	if err != nil {
		return scriptErr(err)
	}
	if res != 1 {
		return common.ErrFailedNotFound
	}
	return nil
}

// Close 实现 ReliableEngine；共享的 Redis 客户端由调用方关闭
func (e *RedisStreamEngine) Close() error { return nil }
