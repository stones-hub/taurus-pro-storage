# 队列可靠性：语义、运行要求与迁移

> 状态：已实现，并在本机隔离环境（仅 Redis 8.10.2）验收（见“验证范围”）。**未**在生产 Redis、Sentinel 切换、
> Redis Cluster、其它 Redis 版本上验证；**旧数据迁移当前不受支持（见“旧 List 数据迁移”）**，没有迁移工具，也不得据文档执行迁移。

## 投递语义

- **至少一次**。入队成功的任务在 Redis 数据未丢的前提下，不会因消费者进程崩溃而静默丢失；
  租约到期后由其它（或重启后的）实例重新领取，**允许重复投递**。Processor 必须幂等。
  组件不依赖业务表，不提供恰好一次。
- `AddData` / `AddDataWithID` 返回 nil **只表示 Redis 已接收**，不表示业务完成，也不表示 Redis 本身零丢失
  （取决于 AOF/RDB、复制与淘汰策略；单机或异步复制不能当作零丢失保证）。
- 网络超时/断连时入队结果**不确定**（命令可能已执行）。`AddData`（自动ID）重试会产生新任务；
  `AddDataWithID`（稳定ID）重试会被识别为重复。
- `channel` 引擎是易失的进程内队列：同样的 API 与重试/失败语义，但进程退出即丢，不提供崩溃恢复。

## Redis 要求

- 单机或主从直连（单个 key 空间）。**明确拒绝 Redis Cluster**（`*redis.ClusterClient` 或 `cluster_enabled:1`，
  返回 `common.ErrUnsupportedTopology`）。Sentinel 故障切换未验证，异步复制下切换可能丢失最近写入。
- 版本：`NewManager` 会拒绝低于 6.2 的 Redis，但 6.2 只是代码里的准入检查，**未经测试，不能称为已支持**。
  **实际验收范围仅限 Redis 8.10.2**（镜像 `redis@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0`）；
  6.2 – 7.x、其它 8.x 小版本未测。
- 建议开启 AOF（`appendonly yes`，按需 `appendfsync always/everysec`），并将 `maxmemory-policy` 设为 `noeviction`
  （队列数据被淘汰等同于丢消息）。

## 数据模型与键命名空间

键前缀 `taurus:q2:{<Source>}:`，与旧版 List 键（`<Source>`、`<Processing>`、`<Retry>_delayed`、`<Failed>`）完全隔离：

| 键 | 类型 | 内容 |
|----|------|------|
| `ready` | Stream（消费者组 `workers`） | 就绪队列，条目只含任务ID，pending 由 Redis 持久化 |
| `delayed` | ZSet | 延迟重试，score=到期毫秒 |
| `leases` | ZSet | 已领取任务的租约，score=到期毫秒 |
| `failed` | ZSet | 失败留存索引，score=失败毫秒 |
| `m:<id>` | Hash | 任务记录：内容、状态、领取凭证、重试计数；**确认成功后删除** |
| `d:<id>` | String（带 PX） | 稳定ID任务完成后的去重墓碑（内容摘要） |

状态迁移（领取、续租、确认、延迟重试、失败转移、重放、到期转回就绪）各自在**单个 Lua 脚本**内完成。
Redis 脚本出错不会回滚已执行的命令，所以脚本先只读校验、通过后才写入：键类型、领取权，以及即将转移/领取的
记录是否完整（状态合法、payload 存在，`n`/`r`/`rp` 是规范十进制 int64 字符串：无符号、无前导零、不超过 int64 范围，
按位数与字典序精确比较；会被 `HINCRBY` +1 的字段还须 ≤ 9223372036854775806，不加 1 的字段允许等于最大值）。

发现损坏记录时返回 `common.ErrCorruptRecord`（键类型错误返回 WRONGTYPE），**保留 stream 入口、租约/延迟索引和 payload
原样**，不 `XACK/XDEL/ZREM`，也不会留下“只在 PEL、没有租约”的孤儿：领取新任务前先用 `XINFO GROUPS` + `XRANGE`
预检队首条目，通过后才 `XREADGROUP`。代价是一条损坏记录会使 `Receive` 持续报错（队列停在该条目上），
需要人工修复该记录后自然恢复。保证范围仅限上述键级与字段级校验（覆盖新领取、租约恢复、Retry、Replay 四条会 `HINCRBY` 的路径，在它们的任何相关写入之前预检）；
不对 Redis 数据被任意篡改做更多承诺，也不声称其它字段被篡改时的行为。
仅有两类清理不报错：幂等的消费者组创建，以及“索引/未投递入口指向的记录已不存在”（没有 payload 可保护，会写入 **Redis 服务端日志**（Lua `redis.log`，不是仓库里的日志文件））。
时间取自 Redis `TIME`，不受实例间时钟差影响。

## 领取、租约与恢复

- 每次领取生成独立 Token。`Ack/Renew/Retry/Fail` 必须出示当前 Token，租约到期被接管后旧 Token 的任何操作
  返回 `ErrLeaseLost`，不会改变新领取（`Ack` 对已不存在的任务幂等返回 nil）。
- XREADGROUP、写入 Token、写入租约在同一脚本内完成，不存在“已读取但凭证未登记”时崩溃的窗口。
  领取响应丢失（客户端没收到）的任务同样在租约到期后恢复（有代理注入测试）。
- 恢复按租约到期时间（ZSet 有序）逐个领取，每次 Receive 先转回到期的延迟重试、再恢复到期租约、最后读新任务；
  不会反复只扫描首页。
- Manager 对在途任务每 `LeaseDuration/3` 自动续租；续租返回凭证失效，或持续无法续租到本地认为租约过期时，
  取消 Processor 的 `ctx`，并且无论 Processor 随后返回 nil、错误还是 panic，都**不再**对该任务做确认/重试/失败转移
  （本地推定过期但服务端凭证其实仍有效时，会多一次重复投递，属于至少一次语义允许的范围）。
  本地租约起点取“发出领取/续租请求之前”的时刻，保守于服务端的到期时刻。
- 无法强制终止不响应取消的 Processor：它可能与接管者**重叠执行**；其迟到的结果因 Token 失效而被忽略。
- **存活但挂死的 Processor 会被持续续租**：只要进程活着、续租成功，该任务和对应 worker 就一直被占用，不会被其它实例接管；
  当前没有“最长处理时间”配置（未新增）。这是残余风险：请让 Processor 自带超时并响应 `ctx`，并监控 `GetStats()["inflight"]`。

## 重试、失败与重放

- `common.RetryableError`（可被 `errors.As` 识别）与 Processor panic 在 `MaxRetries` 内按退避延迟重试，
  转入延迟集合并确认原消息是同一脚本；耗尽后转入失败留存。其它错误直接失败留存。`MaxRetries=0` 表示不重试。
- 失败留存默认永久保留，**不会自动再次调用 Processor**。`Manager.ListFailed(ctx, limit)` 返回最早失败的至多
  `limit` 条（1–1000，无翻页；处理/重放后再取下一批）。`Manager.ReplayFailed(ctx, id)` 以同一ID转回就绪队列，
  重试预算重置；不处于失败状态返回 `ErrFailedNotFound`，重放失败时原记录保持不变（脚本先校验后写入）。
- `ListFailed` 无翻页：只返回最早失败的 `limit` 条，这是当前明确的限制，没有新增接口。
- `FailedRetention > 0` 时，每次失败转移顺带清理过期的失败记录（每次至多 100 条）。默认 0，
  请自行监控失败留存的规模。

## 去重与清理（有界策略）

- `AddDataWithID`：同一ID且内容相同视为重复返回 nil；内容不同返回 `ErrIDConflict`，**不覆盖**已有任务。
  任务**未完成**（待处理/执行中/待重试）或处于**失败留存**时一直参与去重，不受窗口限制。
  成功完成后留下带 `DedupWindow`（默认 24h）过期的墓碑，窗口过后同一ID视为新任务。
  `AddData`（自动ID）不产生墓碑。
- 就绪 Stream **不做 XTRIM**：只对已确认/已转移的条目 `XACK` + `XDEL`，未确认消息不会被裁剪；
  成功任务的记录整体删除。存储规模 ≈ 未完成任务 + 失败留存 + 去重墓碑（TTL 有界）。
- 内存引擎以 `QueueSize` 限制未清理任务数。

## 生命周期

- `Start` 只能成功一次；`Stop` 之后再 `Start` 返回 `ErrManagerClosed`，并发启停不会 panic。
- `Stop(ctx)`：立即停止领取；在 `min(ctx, Config.Timeout)` 内等待在途任务完成；确认使用独立的 5 秒限时 `ctx`。
  超时则取消 Processor 的 `ctx` 并再等 2 秒，仍返回 `ErrStopTimeout`（含仍在运行的任务数）；
  未确认的任务保留租约，到期后由其它实例恢复。不响应取消的 Processor 的 goroutine 会继续运行，
  Stop 不假装其已退出。`Stop` 不关闭共享的 Redis 客户端。

## 兼容性变化（破坏性发布，不是源码兼容升级）

这是一次**破坏性发布**：删除了导出的符号，依赖它们的代码无法编译；`go.mod` 的模块路径没有 `/vN` 后缀，
版本号策略（升主版本或按现有标签策略发布并在发布说明中明确）需要发布者决定。不得宣称源码兼容。

- Redis 引擎改用 Stream；不再读写旧 List 键。新旧版本**不能**并发消费同一业务队列。
- `Source` 必填且校验名称；`MaxRetries` 的 0 不再被改写为 3（`DefaultConfig()` 仍为 3）；bool 字段不再被默认值覆盖。
- `ReaderCount/Processing/Retry/Failed/EnableFailedQueue/EnableRetryQueue/FailedInterval/FailedBatch/RetryInterval/
  RetryBatch/EnableFailedDataToFile/FailedDataFilePath` 已废弃且不生效（`NewManager` 日志提示）；`DefaultConfig()`
  不再填充它们。失败数据不再自动重新处理，不再写 `/tmp/failed_data.log`。
- 删除了依赖破坏性 Pop 的旧实现：`queue.Reader`/`NewReader`/`Worker`/`NewWorker`、`PushToFailedQueueNonBlocking`、
  `PushToProcessingQueueNonBlocking`、`HandleRetry`、`HandleFailedProcessorBatch`、`HandleRetryProcessorBatch`、
  `SaveFailedDataToFile`。`engine.Engine` 接口、`ChannelEngine`、`RedisEngine` 保留（已不被 Manager 使用，
  Redis 版本仍是破坏性 Pop，不要用于可靠消费）。新增 `engine.ReliableEngine`、`engine.Delivery`、
  `engine.RedisStreamEngine`、`engine.MemoryEngine`。
- 新增：`Manager.AddDataWithID/ListFailed/ReplayFailed`，`Config.RedisClient/LeaseDuration/DedupWindow/FailedRetention/PollInterval`。
- `GetStats()` 不再返回 `readers`。`common.IsRetryableError/IsErrorableError` 改用 `errors.As`（可识别被包装的错误）。
- `GenerateID` 由 8 字节改为 16 字节随机数。

## 旧 List 数据迁移与回滚（当前不受支持；未验收，不允许执行）

**结论：以现有公共入口，旧数据迁移被阻断。** 没有实现迁移工具，也没有新增迁移 API。下面只是设计约束，
不是可执行步骤，**未经演练和验收之前不得在任何有真实数据的环境照做**。

现有入口无法表达旧状态：`Enqueue` 固定把任务写成“就绪、重试计数 0”，没有导入重试计数、延迟到期时间、
失败状态的入口。因此：旧的延迟重试项会丢失剩余等待和重试预算，旧的失败项无法进入新的失败留存
（`ListFailed/ReplayFailed` 看不到）。**“丢弃旧状态、把任务当新任务入队”不等于迁移完成**，
也不是被接受的降级方案；要保留语义需要先新增导入能力并经用户确认。

旧数据格式：`<Source>`/`<Processing>` 为 List，`<Retry>_delayed` 为 ZSet（member 为 JSON `DataItem`），
`<Failed>` 为 List。旧版本已弹出且未留存的数据无法恢复。

设计约束（供后续实现与评审，不可直接执行）：

1. 必须先在隔离环境演练并确认旧数据格式，演练通过前不得在生产执行。
2. 必须停机：暂停生产者与全部旧消费者；对 Redis 做 RDB 备份，记录 `LLEN/ZCARD`。
3. 导入不能使用 `Manager.Start/AddData`：`Manager` 一启动就会开始消费并确认，占位 Processor 返回 nil 会直接销毁迁移数据，
   真实 Processor 会在对账前就消费。导入程序应只使用不带消费者的 `engine.NewRedisStreamEngine(...).Enqueue(ctx, id, data, true)`，
   **且前提是已具备能保留延迟/重试计数/失败状态的导入入口——目前没有**。
4. 重跑幂等只在 `DedupWindow`（默认 24h）内成立：已完成任务的去重墓碑过期后，同一ID重跑会再次入队，
   所以导入必须在窗口内完成，窗口过后不得重跑。
5. 对旧ID：必须满足 `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`，不满足的需要人工处理。
6. 旧失败键 `<Failed>` 不得清理，在新版能导入失败状态之前它们是唯一的留存副本；对账通过后也只清理已确认迁移的键。
7. 回滚：新版本运行后产生的任务在新命名空间中，旧版本看不到。回滚需先停机，导出新命名空间中未完成/失败的任务
   并反向写回旧 List，再启动旧版本；已被新版本处理的迁移任务会被旧版本再次处理（重复）。只回退二进制不是有效回滚。

## 验证范围

> 发布候选的独立安装验收（公开 API 契约覆盖表、30 分钟有界长稳态、冻结身份与结果）见
> [release-readiness.md](release-readiness.md)；破坏性变更与版本号建议见仓库根目录 `CHANGELOG.md`。

- 本机 Docker，固定镜像摘要，Redis 8.10.2，127.0.0.1 空闲端口，独立卷，`scripts/reliability-integration.sh` 可重复执行并清理。
- 覆盖：接收/确认/续期/接管旧凭证、panic 后继续处理、重试耗尽留存、显式重放、双实例并发领取、Stop 超时与二次启停、
  Manager 在真实 Redis 上对长任务自动续租（第二实例不提前接管）、合作式 Processor 在 Stop 超时被取消且任务保留并被恢复、
  响应丢失下的入队/领取、pending 清理、稳定ID去重与过期、损坏记录（坏类型、非法/缺失状态、畸形 n/r/rp（前导零、超长、超出 int64、int64 最大值与临界可增值）、缺失 payload、
  Retry/Fail/Replay 前置校验）显式报错并保留现场、`kill -9` 辅助进程后由另一进程恢复、Redis 容器重启（AOF）后的状态保留。
- go-redis 自动重试：`redisx.InitRedis` 的 `MaxRetries` 为 0 时 go-redis 使用默认 3 次重试。读超时/断连后命令可能已在服务端执行，
  客户端重试得到的是第二次结果。因此 `Retry`/`Fail`/`ReplayFailed` 返回错误（如 `ErrLeaseLost`/`ErrFailedNotFound`）
  **不保证服务端没有执行**，调用方和日志不能把错误当作“一定无效果”；状态本身始终由 Token/状态检查保护，不会损坏。
  需要确定性时用 `redisx.WithMaxRetries(-1)` 关闭自动重试（测试里的丢响应用例即如此）；`AddData`（自动ID）在自动重试下也可能重复。
- DB 封装语义用 SQLite 验证，不代表 MySQL/Postgres/ClickHouse。
- **未验证**：生产拓扑/版本、Sentinel 切换、Cluster（仅测试了按客户端类型拒绝）、除 8.10.2 外的任何 Redis 版本（含 6.2–7.x）、旧数据迁移、目标 QPS/容量/积压规模、Redis 网络分区的完整故障矩阵。
