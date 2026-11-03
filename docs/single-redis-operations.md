# 单机 Redis 运维清单与监控建议（候选）

> 状态：**清单与建议，不是已部署配置。** 没有监控平台接入授权：本文的告警阈值未接入任何平台，没有告警已上线；也没有开发页面或扩展 API。
> 阈值均为**初始建议值，非实测 SLA**；上线前必须用目标机器、目标负载、目标 Processor 重新标定。
> 适用范围：本仓库队列的可靠路径（`engine.REDIS`，Redis Stream 引擎），单机 Redis，本机仅在 Redis 8.10.2 上验收。不适用 Cluster / Sentinel 切换 / 主从故障转移。

## 1. 上线前配置清单（逐项确认，勾选后才算就绪）

| 项 | 要求 | 说明 / 依据 |
|---|---|---|
| 版本 | 固定镜像摘要 `redis@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0`（Redis 8.10.2） | 其它版本未验；引擎只拒绝低于 6.2 的版本，并不代表低版本已验证 |
| 内存上限 | 显式 `maxmemory`（留出宿主余量），**`maxmemory-policy noeviction`** | 淘汰策略会静默删除队列键，不可使用；`noeviction` 下写满会拒写（见 §4） |
| 持久化 | `appendonly yes` + `appendfsync always`（本验收配置） | `always` 的写延迟取决于目标磁盘，本机 Docker VM 数值不可外推；若改 `everysec` 需单独评估丢失窗口，本验收未覆盖 |
| 持久卷 | 数据目录在独立持久卷/磁盘上，不放容器可写层或 tmpfs | AOF 写满磁盘会让 `always` 模式下的 Redis 直接退出（见 §6） |
| 磁盘余量 | 数据盘空闲 ≥ 数据集 + AOF 大小的 2 倍（AOF 重写、BGSAVE 的临时占用） | 初始建议 |
| 认证 | `requirepass` 或 ACL 用户（最小权限），不使用默认空密码 | 调用方用 `redisx.WithPassword` |
| 网络限制 | `bind` 到内网接口/回环，防火墙只放行业务主机；不暴露公网 | 本仓库 `redisx` 目前**没有 TLS 选项**；跨不可信网络需另行方案 |
| 命令面 | 业务账号不需要 `FLUSHDB/FLUSHALL/CONFIG/DEBUG`，用 ACL 禁用 | 防误操作清空队列 |
| 备份 | 定期 `BGSAVE` + `redis-check-rdb` + 异机/异盘复制 + **定期恢复演练**（§7） | RPO 以快照时刻为边界 |
| 进程守护 | systemd/容器 `restart` 策略，启动失败告警 | 进程崩溃后同卷恢复实测通过（§8），但需有人/系统去拉起 |
| 命名 | 每个队列 `Config.Source` 唯一且规范；同名队列共享数据 | 键空间 `taurus:q2:{<Source>}:`，与旧版 List 键隔离 |
| Processor | **幂等**，并尊重 `ctx` 取消，自带业务超时 | 至少一次投递；确认失败/崩溃/租约接管都会重投 |
| 容量规划 | 完成去重墓碑、失败留存的增长预算（§5） | 墓碑默认 24h，失败默认永久 |

## 2. 用 Redis 原生命令观察（无需新增 API）

> 键名布局 `taurus:q2:{NAME}:{ready,delayed,leases,failed,m:<id>,d:<id>}` 是实现内部约定，**可能随版本变化**；严格门禁里有“布局探针”会在布局变化时失败，而不是读出 0。把下面命令固化进监控前，请对照当前候选的 `pkg/queue/engine/redis_stream.go` 文件头注释。

```sh
NAME=orders                                   # Config.Source
P="taurus:q2:{$NAME}:"
RC="redis-cli -a $REDIS_PASSWORD --no-auth-warning"
$RC XLEN     "${P}ready"                      # 就绪 + 在途（Ack 后条目被删除）
$RC XPENDING "${P}ready" workers              # 在途（已领取未确认）数量，第 1 行
$RC ZCARD    "${P}delayed"                    # 等待重试
$RC ZCARD    "${P}leases"                     # 持有租约的任务
$RC ZCARD    "${P}failed"                     # 失败留存条数
$RC XRANGE   "${P}ready" - + COUNT 1          # 最老未完成条目；ID 前半段是毫秒时间戳 -> 年龄
$RC ZRANGE   "${P}delayed" 0 0 WITHSCORES     # 最近到期的重试
# 租约已过期（疑似崩溃/卡死）：用 Redis 服务端时间，避免本机时钟偏差
NOW_MS=$($RC TIME | awk 'NR==1{s=$1} NR==2{printf "%d", s*1000+int($1/1000)}')
$RC ZCOUNT   "${P}leases" -inf "$NOW_MS"
# 任务记录与墓碑数量：用 --scan，不要用 KEYS；大库上 SCAN 也有成本，降低采样频率
$RC --scan --pattern "${P}m:*" | wc -l
$RC --scan --pattern "${P}d:*" | wc -l
```

> 上面的 `redis-cli` 片段**没有**逐条在 redis-cli 上执行验证；严格门禁用 go-redis 执行了等价查询（XLEN/XPENDING/ZCARD/ZCOUNT/SCAN/INFO persistence 的 `rdb_saves` 等）。落地前请在测试实例上逐条试跑，尤其是 awk 取 TIME 的写法和各 INFO 字段名。

实例级：

```sh
redis-cli INFO memory       # used_memory, maxmemory, mem_fragmentation_ratio
redis-cli INFO persistence  # aof_enabled, aof_last_write_status, aof_last_bgrewrite_status,
                            # aof_rewrite_in_progress, aof_delayed_fsync, aof_current_size,
                            # rdb_last_bgsave_status, rdb_saves
redis-cli LASTSAVE          # 上次 RDB 快照的时间（RPO 度量）
redis-cli INFO stats        # evicted_keys（应恒为 0）, rejected_connections, total_error_replies
redis-cli INFO errorstats   # errorstat_OOM / errorstat_MISCONF 计数增长
redis-cli INFO clients      # connected_clients, blocked_clients 对比 maxclients
redis-cli INFO commandstats # 长期趋势；SLOWLOG GET 看慢命令
redis-cli MEMORY USAGE "${P}d:<某个已完成ID>"   # 估算单个墓碑的内存成本（以你的ID长度实测）
df -h <数据卷挂载点>        # 磁盘（Redis 自身看不到宿主机磁盘余量）
```

## 3. 建议告警阈值（初始建议，**非实测 SLA**）

| 指标 | 警告 | 严重 | 说明 |
|---|---|---|---|
| `used_memory / maxmemory` | ≥ 70% | ≥ 85% | 到 100% 后队列无法入队**也可能无法领取/确认**（§4） |
| `aof_last_write_status` / `rdb_last_bgsave_status` / `aof_last_bgrewrite_status` | — | 不为 `ok` | 立即处理磁盘/权限 |
| 数据盘空闲 | < 20% | < 10% | `always` 下磁盘满会令 Redis 退出 |
| Redis 进程/端口不可达 | — | 立即 | 配合进程守护的重启次数告警 |
| `errorstat_OOM` 或 `errorstat_MISCONF` | 增长 | 持续增长 | 业务入队正在被拒 |
| `ZCOUNT leases -inf now`（已过期租约） | > 0 持续 > 2×`LeaseDuration` | 持续增长 | 有任务无人在续租：消费者崩溃或停机；若仍有消费者在线但不减少，查 Receive 是否被拒（OOM） |
| `XPENDING` 在途数 | 持续 > `WorkerCount`×实例数 | 持续且最老条目年龄增长 | 在途超过并发能力说明有残留租约/卡死 |
| 最老 ready 条目年龄 | > 5 倍预期排队时间 | > 15 分钟（按业务调整） | “无进展”的近似指标，**见 §5.3 缺口** |
| `ZCARD failed` | > 0 | > 100 或增速异常 | 失败默认永久留存，需人工处理 |
| 墓碑数 `d:*` / 估算内存 | 占预算 ≥ 50% | ≥ 80% | 稳定ID任务数 × `DedupWindow`（默认 24h） |
| 消费者端：Processor 平均/最大耗时、重复执行率 | 按业务 | — | 重复执行是正常现象，但突增表示确认失败或租约抖动 |
| `ListFailed` 返回 1000 条 | 告警 | — | 无分页，已触顶 |

采样周期建议 10–60 秒；SCAN 类采样 1–5 分钟。

## 4. 内存写满（OOM）处置

**实测现象（严格门禁 `oom` 场景，16MB `noeviction` 实例）**：写满后

- 入队被 `OOM command not allowed` 明确拒绝，已接收的任务都真实存在于 Redis，被拒的都不存在（未观察到假成功/假失败）；
- 领取（Receive）与确认（Ack）同样是写操作，也会被拒：已执行的任务**确认失败**，租约到期后释放内存再被**重新投递**（该场景累计出现重复执行）；释放空间后，所有已接收任务最终全部执行并确认。
- 因此：写满期间队列**不能靠自身消化来释放空间**。

**处置顺序**：

1. **先停止接收**（暂停上游生产/摘流量），避免继续被拒与重试风暴；
2. 释放**非队列**的占用（临时填充、缓存、其它业务键）或**扩容**（`CONFIG SET maxmemory` 提高上限、迁移到更大机器）；
3. 等待消费恢复：观察已过期租约归零、`XPENDING` 回落、最老条目年龄回落；
4. 恢复接收。重投的任务由业务幂等保证无副作用。

**严禁**：`FLUSHDB` / `FLUSHALL`、删除 `taurus:q2:{...}:` 下的任务记录/索引/流来“恢复”——这会丢失未完成任务、破坏去重与账目；也不要手工 `XACK/XDEL`。需要清理失败任务时用 `ReplayFailed` 或在业务确认后按受控流程处理。

## 5. 已知缺口与容量规划

### 5.1 确认失败与重投
`AddData*` 返回 nil 只表示 Redis 已接收；Ack 失败（OOM、网络、租约被接管）或进程崩溃会导致重投。**业务必须幂等**（用任务ID去重业务副作用）。

### 5.2 失败与墓碑增长
- 失败任务默认永久留存（`FailedRetention=0`），没有自动清理；`ListFailed` 最多 1000 条、无分页。需要：失败数告警、处理责任人、定期重放或确认后的受控清理流程；也可设置 `FailedRetention`（到期自动清理，意味着数据被丢弃，需业务同意）。
- 稳定ID任务确认后留下墓碑 `d:<id>`（默认 24h，`DedupWindow`）。稳态墓碑数 ≈ 任务速率 × 24h，需计入内存预算；用 `MEMORY USAGE` 在目标ID长度下实测单键成本。缩短 `DedupWindow` 会缩小重复提交的保护窗口。

### 5.3 挂死 Processor 无进展缺口
挂死但存活的 Processor 会被自动**持续续租**，租约不会过期，`expired_leases` 为 0，队列看起来“有人在处理”。本候选**没有最长执行时间配置，也没有“无进展”专用指标**。缓解：Processor 内部自带业务超时并尊重 `ctx`；监控最老条目年龄、在途数长期不降；业务侧埋点（每次处理开始/结束、耗时直方图）。`Stop` 在超时后会取消 ctx，但不响应 ctx 的 Processor 仍会继续运行，此时 `Stop` 返回错误。

### 5.4 其它
- 单个损坏记录会使队列报错并保留现场，可能阻塞；需人工修复。
- DB（SQLite 以外）、Sentinel/Cluster、独立生产者进程、网络延迟、多实例争抢、长时间稳态均未验。

## 6. AOF 写失败（磁盘满）
上一轮有限验收（`docs/single-redis-readiness.md` §7）观察到：`appendfsync always` 下磁盘满会使 Redis **自行退出**；`everysec` 下首次写入仍可能返回成功，随后报 `MISCONF`。严格门禁**没有实现** ENOSPC 破坏性模式（默认不跑）。结论：必须有磁盘告警 + 进程守护，且任何 `everysec` 写入在未重启逐ID验证前不能宣称最终丢失或最终保留。

## 7. 备份与恢复（严格门禁已走通的流程）

1. `BGSAVE`，等待 `INFO persistence` 里 `rdb_saves` 增加、`rdb_bgsave_in_progress:0`、`rdb_last_bgsave_status:ok`（超时视为失败，不要忽略）；
2. 对 `dump.rdb` 运行 `redis-check-rdb`，并记录文件 SHA256；复制到异机/异盘；
3. 恢复：新卷 + 新实例，拷入 `dump.rdb`，先以 **`appendonly no`** 启动让其载入 RDB，确认 PING 与数据后 `CONFIG SET appendonly yes` 并等待 `aof_rewrite_in_progress:0` 且 `aof_last_bgrewrite_status:ok`；之后**务必把启动配置也改回 `appendonly yes`**，否则下次重启会丢失 AOF 语义；
4. 恢复后：快照时刻的 ready/delayed/inflight/failed/去重状态全部还原，快照之后接收的任务**不在备份内**（RPO = 距快照时刻的时间）。
5. 恢复时间从创建容器前开始计，另记录 `start` 命令完成后到 PING 成功的时间；本机小数据集（约 90 个键）只有量级参考，RTO 随数据量变化。
6. “`appendonly yes` + 空 AOF 目录 + `dump.rdb`” 的组合未实测，流程里按上面 `appendonly no` 先载入的方式避免。

## 8. 进程崩溃恢复
Redis 进程被 `SIGKILL` 后同卷重启：选定状态（任务记录、墓碑、延迟/租约/失败集合、Stream 条目与 PEL）一致，随后每个任务恰好执行一次（本机场景）。**仅证明进程崩溃**，不证明主机掉电、磁盘损坏或 fsync 之外的介质故障。不要据此宣称单机主机故障零丢失。

## 9. 严格验收门禁如何运行

```sh
scripts/strict-readiness.sh                 # 默认场景，约 1 分钟；证据写入 /tmp/taurus-pro-storage-strict/<run-id>/
scripts/strict-readiness.sh --sustained     # 额外短稳态：2400 条 @80/s（约 30s 生产），整场景总硬截止 60 秒
scripts/strict-readiness.sh --scenarios normal,oom --n 500 /some/evidence/dir   # 部分运行，见下
```

**完整 vs 部分（PARTIAL）**：只有“默认 6 类场景（共 9 个结果：normal×4、backlog、consumer-kill、redis-sigkill、oom、backup-restore）一个不少且 N=1000”才可能输出 `STRICT GATE: PASS`（退出码 0），此时脚本还会对 `summary.json` 逐项对账这 9 个结果必须存在且为 true（缺一即失败）。使用 `--scenarios` 子集或 `--n` 调低条数时，即使全部通过也只输出 `STRICT GATE: PARTIAL`（退出码 3），summary 里 `mode=PARTIAL` 并列出原因，注入账本负例若缺基线场景则记 SKIP；PARTIAL 不能当作完整门禁结论。退出码：0 完整通过 / 1 失败 / 2 环境不满足 / 3 仅部分通过。`--sustained` 是附加项，不改变 FULL/PARTIAL 判定。

**短稳态（`--sustained`）**：2400 条（≤3000）按 80 条/秒提交，生产约 30 秒，剩余预算留给排空；**整个场景**（提交 + 确认 + Stop + 对账）有 60 秒硬截止，超时本身是一条失败检查（`scenario_total_deadline`），不是“60 秒纯生产窗口”。

它做什么、不做什么：

- 打包候选（含未跟踪源码，合成版本，局部 `GOMODCACHE`，不用 `replace`），在独立调用模块里只经公开 API 使用；不改 `pkg`、根 `go.mod/go.sum`。
- 逐ID append-only JSONL 账本：提交（每次尝试一行，含载荷 SHA256/长度/结果/时刻）、每次 Processor 调用起止（重复追加，区分唯一ID与总执行次数）、完成观察、检查结果。校验器从磁盘重新读取账本，检查未知ID、载荷不符、漏执行、漏确认、未预期重复、查询失败（不当 0）。
- “Ack 后对账”：Manager 正常 `Stop` 之后（静止边界）逐ID核对任务记录消失、墓碑存在且摘要等于提交时载荷 SHA256、PTTL 合理，以及 Stream/PEL/租约/延迟/失败/记录/墓碑数。运行中的采样是非原子的，不用于严格判定。
- 故障场景：独立消费者子进程领取后 SIGKILL 并由新进程恢复（场景返回、panic、INT/TERM 路径都会对本轮启动的子进程按精确 PID 做 TERM→KILL 并 Wait；子进程发现父进程消失也会自退；脚本收到 INT/TERM 时只对当前步骤的精确 PID 发信号，不使用 pkill/killall）；Redis 进程 SIGKILL 同卷恢复；OOM；RDB 备份/异容器恢复。恢复后额外观察至少一个租期再停，记录重复。
- 门禁自测：校验器负例单元测试；真实命令负例（坏 Redis 地址、归属标签不符的诱饵容器被拒绝且未被触碰；串用 manifest、篡改 owner/run、用本轮 owner 或错误 run 清理诱饵都被拒绝且不执行任何变更，诱饵只能用独立显式的 decoy owner + 同一 run 清理）；对真实场景账本注入未知ID/错误载荷/漏完成/查询失败/重复/清空账本/删除必需检查后，`verify` 必须以**预期的违规码**失败（退出码 1；账本不可读为 4，违规但码不符为 5，都不算负例通过）。空账本（无记录或无提交）本身即违规；场景把关键对账检查登记为“必需检查”，缺失同样是违规，不会假通过。
- 资源边界：只操作本轮 run-id + owner 标签的容器与卷，保存完整容器ID，每次 kill/start/exec/cp/rm 前先 inspect 核验（登记的期望 owner/run 必须等于当前上下文，且实际标签与之相等）；创建流程为 create → 登记完整ID → start，start 失败也保留清单供精确清理，登记失败则对刚创建的资源按完整ID/卷名精确补救；清理按显式清单，不 prune、不按前缀枚举；不使用默认 Redis。

**延迟/吞吐口径**：`submit_to_process_start/end` = 提交调用前 → Processor 开始/返回（不含 Ack）；`submit_to_final_observed` = 提交调用前 → 轮询观察到完成（含 50ms 轮询间隔与“提交结束后才开始轮询”，是 Ack 时刻的上界，不是 Ack 时刻）；吞吐 = 观察到完成的唯一ID数 ÷（最早提交前 → 最后一次完成观察）。
**通过的含义**：本机、固定镜像、有限负载下的有界验收通过；**不等于生产放行**。
