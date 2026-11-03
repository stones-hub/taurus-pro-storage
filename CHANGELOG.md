# 变更日志

本文件记录对使用方可见的变更。版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)；
当前模块路径 `github.com/stones-hub/taurus-pro-storage` 没有 `/vN` 后缀，仍处于 v0（1.0 之前）。

## [v0.3.0] —— 破坏性升级

> **升级前请阅读下列破坏性变更。** 保留 module 路径 `github.com/stones-hub/taurus-pro-storage`，不需要增加 `/v2`。
> v0 阶段不承诺 API 向后兼容；`go get -u` 可能升级到本版本，旧调用代码可能无法编译，旧 Redis List 数据不会自动迁移。
> 本版本已完成独立安装、故障恢复与30分钟有界稳定性验收，不代表任意生产环境已获验证。
> 验收结果与限制见 [docs/release-readiness.md](docs/release-readiness.md)。

### 破坏性变更：队列（`pkg/queue`）

- **Redis 引擎改为 Redis Stream 可靠消费（至少一次）**，键命名空间改为 `taurus:q2:{<Source>}:*`，
  **不再读写旧版 List 键**（`<Source>`、`<Processing>`、`<Retry>_delayed`、`<Failed>`）。
  新旧版本不能同时消费同一业务队列；**旧数据不会被迁移**，当前没有迁移工具（见 docs/queue-reliability.md）。
- 删除导出符号（依赖它们的代码将无法编译）：
  `queue.Reader`、`queue.NewReader`、`queue.Worker`、`queue.NewWorker`、`HandleRetry`、
  `HandleFailedProcessorBatch`、`HandleRetryProcessorBatch`、`PushToFailedQueueNonBlocking`、
  `PushToProcessingQueueNonBlocking`、`SaveFailedDataToFile`。
- `Manager` 为**单次生命周期**：`Stop` 之后 `Start` 返回 `common.ErrManagerClosed`；重复 `Start` 返回
  `ErrManagerAlreadyRunning`；未运行时 `Stop`/`AddData` 返回 `ErrManagerNotRunning`。
- `Stop(ctx)` 在 `min(ctx, Config.Timeout)` 内排空；超时返回 `common.ErrStopTimeout`（不响应取消的 Processor
  会被如实报告为仍在运行），未确认任务保留租约、到期后可被恢复。`Stop` 不关闭共享 Redis 客户端。
- **失败任务默认永久留存，不再自动重新调用 Processor**，也不再写 `/tmp/failed_data.log`；
  通过新增的 `ListFailed`/`ReplayFailed` 处理。`FailedRetention > 0` 才会自动清理。
- `Config.Source` 必填并校验（`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`）；`MaxRetries` 的 0 表示“不重试”，
  不再被改写为 3（`DefaultConfig()` 仍为 3）；bool 字段不再被默认值覆盖；`NewManager` 不修改调用方传入的 `Config`。
- 以下字段已废弃且**不生效**（`NewManager` 打印警告）：`ReaderCount`、`Processing`、`Retry`、`Failed`、
  `EnableFailedQueue`、`EnableRetryQueue`、`FailedInterval`、`FailedBatch`、`RetryInterval`、`RetryBatch`、
  `EnableFailedDataToFile`、`FailedDataFilePath`。
- `GetStats()` 不再包含 `readers`；`GenerateID()` 由 8 字节改为 16 字节随机数（32 位十六进制）。
- `common.IsRetryableError`/`IsErrorableError` 改用 `errors.As`，可识别被包装的错误。
- Redis 要求：**拒绝 Redis Cluster**（`ErrUnsupportedTopology`），低于 6.2 拒绝；实际只在 Redis 8.10.2 上验证。
- `engine.Engine`、`ChannelEngine`、`RedisEngine` 仍导出但已不被 `Manager` 使用；`RedisEngine` 仍是破坏性 Pop，
  不要用于可靠消费。

### 新增：队列

- `Manager.AddDataWithID`（稳定ID，可安全重试；同ID同内容视为重复返回 nil，不同内容返回 `common.ErrIDConflict`）、
  `Manager.ListFailed`（最早失败的至多 1–1000 条，无翻页）、`Manager.ReplayFailed`（同ID转回就绪，重试预算重置）。
- `Config.RedisClient`、`LeaseDuration`（默认 30s，≥500ms，自动每 1/3 租期续租）、`DedupWindow`（默认 24h）、
  `FailedRetention`、`PollInterval`。
- `engine.ReliableEngine`、`engine.Delivery`、`engine.FailedMessage`、`engine.ReliableOptions`、
  `engine.RedisStreamEngine`、`engine.MemoryEngine`、`engine.ValidateID`、`engine.MaxListFailed`。
- 错误：`ErrManagerClosed`、`ErrStopTimeout`、`ErrLeaseLost`、`ErrIDConflict`、`ErrInvalidID`、`ErrFailedNotFound`、
  `ErrNotSupported`、`ErrUnsupportedTopology`、`ErrCorruptRecord`、`ErrEngineClosed`；`RetryableError`/`ErrorableError` 增加 `Unwrap`。

### 行为变化：数据库（`pkg/db`、`pkg/db/dao`）

- `InitDB`：`MaxRetries <= 0`、`RetryDelay < 0`、未知 `DBType` 直接返回错误；最后一次连接失败后不再等待；
  并发初始化同名连接只有一个成功；连接注册表加锁。
- `GetDB` 未找到时错误包装新增的 `db.ErrConnectionNotFound`（可 `errors.Is`）；`DbList()` 返回快照副本。
- 原生分页/表名辅助函数：表名必须是标识符（可带一层 schema 前缀），`page`/`pageSize` 必须 ≥ 1。
- `dao.BaseRepository` 分页：`page`/`pageSize` 必须 ≥ 1；`orderBy` **只接受实体模型中的字段名或列名**
  （以前直接拼进 SQL；传 `"created_at desc"` 之类的表达式现在会返回错误，请改用字段名 + `desc` 参数）。
  `FindByID`/`FindOne` 的“未找到”错误包装原始错误。`Update`/`Save` 的零值语义写入了注释（行为未改）。
- 上述 DB 语义只用 SQLite 验证过，MySQL/Postgres/ClickHouse 未验证。

### 其它

- `redisx.Lock` 补充注释：它是单实例租约锁，不是 fencing 锁（行为未改）。
- 示例 `examples/queue_example/*` 去掉已废弃字段；新增 `examples/reliable_queue_example`。

### 语义与边界（使用方必须知道）

- **至少一次**：`AddData`/`AddDataWithID` 返回 nil 只表示 Redis 已接收，不表示业务完成；崩溃恢复、租约接管、
  Ack 失败（例如 Redis 内存顶满）都会导致**重复执行**，Processor 必须幂等。不提供恰好一次。
- 网络超时/断连时入队结果不确定；只有 `AddDataWithID` 可以安全重试。
- **单机 Redis 数据故障边界**：持久性取决于 AOF/RDB 配置；验证仅覆盖 Redis 进程被 SIGKILL 后同卷恢复（AOF always）
  与 RDB 快照恢复（RPO = 快照时刻）。主机掉电、磁盘损坏、异步复制切换都可能丢失已确认接收的任务，不能宣称零丢失。
- `maxmemory` 顶满（noeviction）时入队、领取与确认都会失败，队列不能自愈；已执行未确认的任务会在恢复后重投。
- 存活但挂死的 Processor 会被持续续租，没有最长执行时间；不响应 ctx 的 Processor 可能与接管者重叠执行。
- 损坏记录显式报错并保留现场，可能阻塞队列直到人工修复。
- 内存（channel）引擎是进程内易失队列，进程退出即丢。

### 验收与未验证

- 一条命令的独立安装验收（打包候选 → 本地 file module proxy → 独立调用模块只用公开 API；私有 Redis 8.10.2 容器）：
  `bash scripts/strict-readiness.sh --sustained`；附加 30 分钟有界长稳态：`--soak`。
  源码级：`go build ./... && go vet ./... && go test ./...`，Redis 集成：`bash scripts/reliability-integration.sh`。
- 结果、阈值、覆盖表与**未验证清单**（生产拓扑/版本、Sentinel、Cluster、其它 Redis 版本、目标负载与容量、
  >30 分钟运行、主机/磁盘故障、MySQL/Postgres/ClickHouse）见 [docs/release-readiness.md](docs/release-readiness.md)。
