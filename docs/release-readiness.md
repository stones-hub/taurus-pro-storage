# 发布候选就绪报告（release readiness）

> 状态：**发布候选的本机有界验收结果，不是生产放行，也不是已发布。** 没有 commit / push / tag / 发布，
> 没有访问任何生产资源。结论只覆盖下文“冻结身份”所列的源码与验收工具，以及本机隔离的 Redis 8.10.2 容器。
> 本文在最终验收结束后编写，编写本文**不改变**冻结的候选源码与验收工具身份（本文自身不在冻结清单内）。

## 1. 执行身份

- 执行器：Claude Code，进程命令行 `claude --effort high -p … --model claude-opus-5-5`（由 wrapper
  `/tmp/taurus-pro-storage-release/claude-high` 以 `exec claude --effort high "$@"` 启动，`--model claude-opus-5-5` 由调度脚本传入），
  本机进程表中可见（pid 27114）。会话 ID 以调度器证据 `/tmp/taurus-pro-storage-release/claude-code.json` 为准；本文不自报。
- 基线 `33292a82db8211f79b190dc9ad2bb8915d1f9933`（= 标签 `v0.2.0`）+ 未提交改动。

## 2. 本轮补齐的内容（只改验收工具与文档，未改 `pkg/`、`go.mod`、`go.sum`、模块路径）

| 文件 | 变化 |
|---|---|
| `scripts/readiness/cmd/gate/contract.go`（新） | 默认门禁新增场景 `contract`：经打包安装的公开 API 契约（覆盖表见 §4） |
| `scripts/readiness/cmd/gate/script.go`（新） | 脚本化 Processor：按 (ID, 第几次调用) 返回成功/可重试错误/普通错误/panic/合作式或非合作式阻塞，逐次写账本 |
| `scripts/readiness/cmd/gate/soak.go`、`soak_test.go`（新） | 显式可选的 30 分钟有界长稳态 `soak`、连续采样、安全阈值、墓碑三态核对、趋势；纯函数单测 |
| `scripts/readiness/cmd/gate/main.go` | 接入 `contract`（默认集合 9→10 个结果）、`--soak SECONDS`（仅 1800 记为 `soak`，其它为 `soak-smoke`/`soak-limit-test` 且强制 PARTIAL）、整轮截止随 soak 延长、新账本变异 `drop_last_call` |
| `scripts/readiness/ledger/validate.go`、`validate_test.go` | `Expect.ExpectCalls`：脚本化ID调用次数必须恰好相等（`call_count_mismatch`），期望表中未提交的ID报 `expect_calls_unknown_id`；`ScriptedRepeats` 统计 |
| `scripts/strict-readiness.sh` | `--soak` / `--soak-smoke`（互斥）、soak 阈值真实负例、soak 独立核对（Python 读结果与采样）、`drop_last_call` 变异负例、新单测名纳入门禁 |
| `scripts/test-strict-readiness-args.sh` | 新增 6 个非法参数负例（共 20 个，均须退出 2） |
| `CHANGELOG.md`（新）、`README.md`、`docs/queue-reliability.md`、本文 | 发布说明、升级提示、验收命令、版本号建议 |

默认门禁含义的变化：**FULL 现在要求 10 个默认结果（多了 `contract`）**；`--sustained` 含义不变（仍是约 30 秒、不是长稳态）；
`--soak` 不加就不会运行。

## 3. 冻结身份（最终验收所用）

冻结清单存于 `/tmp/taurus-pro-storage-release/freeze/`（供 Pi 核验后清理）：

| 清单 | 清单文件 SHA256 | 范围 |
|---|---|---|
| 候选源码 `candidate-source-sha256.txt` | `1d9b3271e72ac1e86a1cad4b6b0570a9505d76b4791bc6609a8d8923dbcbf6a3` | 28 个文件：全部 `*.go`（含未跟踪源码与测试，排除根下 scripts/docs/examples/.pi）+ 根 `go.mod`/`go.sum`，与打包器同一规则 |
| 验收工具 `acceptance-tools-sha256.txt` | `7b021352a16c62e0ba357ed8aac8dff612549d49f7a9d7ca19e8a162f04c1aad` | `scripts/` 下全部 26 个文件 |
| 发布文档/示例 `release-docs-examples-sha256.txt` | `160e68f064782c8b9d5617c3c03806768595b1318e0fcde5dea0c8c96fb5bb26` | README、CHANGELOG、docs 4 篇、示例 4 个 |

- 候选源码清单哈希与上一轮严格门禁（docs/single-redis-readiness.md §12）记录的 `1d9b3271…` 完全一致：本轮**没有改动候选源码**。
- 最终门禁打包的合成模块：`v0.0.0-strict.s1793694753-d47bab`，zip SHA256 `bc0d66ad510431c679d915a02f9d9e12ba9712586d7479012933db61a3d3562d`
  （含合成版本号，不等于正式发布包身份）。
- **重新冻结记录**：第一次冻结（v1，工具清单 `51e543de…`）的最终门禁中 `contract` 因**验收工具自身的竞态**失败——布局探针打在
  `c-ok-00000` 上，而该任务可能在探针前已被确认、记录删除（自测时恰好通过）。这不是候选缺陷。已中止该轮（exit 130，资源由清单精确清理，
  证据 `final/aborted-run1-contract-probe-race/`），把探针改到确定仍在执行的 held 任务上，用开发构建连跑 3 次 `contract` 全部通过后重新冻结为 v2。
  v1→v2 只有 `scripts/readiness/cmd/gate/contract.go` 一处变化；候选源码不变。最终结论只来自 v2 的完整运行。

## 4. 公开 API 契约覆盖表

列含义：**EXT** = 外部安装门禁（打包候选经 file module proxy 装入独立模块、只调用公开 API，`scripts/strict-readiness.sh`；
场景名见括号）；**PROBE** = 门禁里直接读 Redis 键的内部状态探针（只用于确认终态/未写入，不是 API 契约，检查名带 `_probe` 或为逐ID对账）；
**UNIT** = 既有源码单测/集成测试（`go test`，Redis 部分由 `scripts/reliability-integration.sh` 在隔离 Redis 中运行）。

| 契约 | EXT | PROBE | UNIT（既有） |
|---|---|---|---|
| 正常成功、逐ID确认 | normal×4、backlog、sustained、contract(`c-ok`)、soak | 每个 done ID：记录删除 + 墓碑摘要一致 + PTTL≤窗口 | TestSuccessFlow、TestRedisManagerEndToEnd |
| 配置拒绝（空/非法 Source、租约<500ms、负重试、负窗口、未知引擎、nil Processor/Config）；不改调用方 Config、`MaxRetries=0` 保留 | contract | — | TestConfigValidation、TestConfigDefaultsAndExplicitZero、TestNewManagerDoesNotMutateCallerConfig |
| 输入错误：非法ID（5 种）→`ErrInvalidID`；空数据→`ErrItemEmpty` | contract | — | TestStreamEnqueueValidation |
| panic：重试后成功；耗尽→失败留存 `retries exhausted: panic:`；`MaxRetries=0` 时直接留存 | contract、soak(panic1 类 358 次) | — | TestPanicIsIsolatedRetriedThenRetained |
| `RetryableError` 重试后成功；耗尽留存（Attempts=MaxRetries+1）；普通错误直接留存（Attempts=1，不重试）；`MaxRetries=0` | contract、soak(retry1 895 次、fatal) | — | TestRetryPolicies、TestMaxRetriesZeroMeansNoRetry、TestRetryDelayBackoff、TestStreamRetryFailReplay |
| `ListFailed`：集合精确、payload/原因/次数、按失败时间升序、limit=2 返回最早两条、limit 0/-1/1001 报错、取消 ctx 报错 | contract、redis-sigkill（重启后）、soak（15s 巡检 + 收尾精确集合 179 条） | — | TestReplayFailed、TestStreamRetryFailReplay |
| `ReplayFailed`：成功后再次执行并完成；已完成/未知/重放后再重放→`ErrFailedNotFound` | contract、redis-sigkill、soak（179 条重放全部完成） | — | TestReplayFailed、TestStreamReplayFailureKeepsOriginal |
| 稳定ID去重/冲突：执行中、失败留存中、完成后窗口内（同内容 nil 且不执行；不同内容 `ErrIDConflict`）；窗口到期后视为新任务 | contract（四种状态全覆盖）、redis-sigkill、oom（同ID重投）、soak（36 组冲突/重复探针、33 次到期后重提交） | 到期探针：墓碑与记录均不存在后才重提交 | TestAddDataWithIDDedupAndConflict、TestStreamDedupWindowAndUnfinished、TestRedisUncertainEnqueueResultWithStableID |
| Stop 成功（空闲/有已完成任务）、重复 Stop → `ErrManagerNotRunning` | contract、全部场景收尾 | Stop 后静止对账 | TestStopDrainsInflightAndStopsClaiming |
| Stop 合作式超时：`ErrStopTimeout`、耗时有界（实测 1.00s，Timeout 1s）、任务保留租约不入失败留存、另一 Manager 租约到期后恢复并完成 | contract(`c-stopcoop`) | 停止后 pending/leases/records=1、failed=0 | TestStopTimeoutCancelsCooperativeProcessorAndKeepsTask、TestRedisCooperativeStopCancelsAndKeepsTask、TestRedisStopTimeoutThenRecoveredByAnotherInstance |
| Stop 非合作式超时：错误含“still running”，状态 Stopped 且不能再 Start；迟到的成功结果在租约有效时仍被确认 | contract(`c-stophard`) | — | TestStopTimeoutIsReportedHonestly |
| 单次生命周期：Start 前 Add/Stop → NotRunning；二次 Start → AlreadyRunning；Stop 后 Start → `ErrManagerClosed`、Add → NotRunning；状态值 | contract | — | TestLifecycleSingleUse、TestConcurrentStartStopAddDoesNotPanic（并发只在单测） |
| ctx 边界：已取消 → `ErrContextCanceled`（稳定ID与自动ID）；已过期 → `ErrContextTimeout` | contract | 被拒提交未写入记录（任何意外写入还会被账本当作未知ID执行而发现） | — |
| 内存（channel）引擎：QueueSize 满 → `ErrQueueFull`、失败留存/列出/重放、Stop | contract | — | TestMemoryEngineLifecycle、TestMemoryEngineCapacityAndClose |
| 消费者进程崩溃后跨进程恢复 | consumer-kill | PEL/墓碑 | TestRedisCrashedConsumerProcessIsRecovered |
| Redis 进程 SIGKILL 同卷恢复、RDB 备份恢复、OOM 拒绝与释放后重投 | redis-sigkill、backup-restore、oom | 选定字段快照比对 | TestPersistenceAcrossRestart |

**只在 UNIT 覆盖、外部门禁未覆盖**（需要注入内部状态或代理，不适合只用公开 API）：长任务自动续租（TestLongTaskIsLeaseRenewed、
TestRedisManagerAutoRenewPreventsEarlyTakeover）、租约丢失时取消 Processor 且不转移（TestLeaseLostCancelsProcessorAndSkipsTransfer、
TestRenewFailureCancelsProcessorAfterConservativeLease）、损坏记录保留现场（TestStreamReceiveCorrupt*、TestStreamCounter*）、
领取响应丢失（TestRedisReceiveResponseLostIsRecoveredAfterLease）、Cluster 客户端拒绝（TestStreamRejectsClusterClient，仅按客户端类型）、
双实例并发领取（TestRedisTwoInstancesShareOneQueue、TestStreamConcurrentConsumersExactlyOnceClaim）。
**任何层面都未覆盖**：ListFailed 超过 1000 条的翻页（没有该 API；上一轮实测 1100 条时只返回 1000）、`FailedRetention>0` 的外部行为
（仅 TestStreamFailedRetentionPrunes）、Sentinel/Cluster/其它 Redis 版本。

## 5. 最终验收结果（冻结 v2）

所有命令在 `/Users/yelei/data/code/repos/taurus/taurus-pro-storage` 执行；原始日志曾保存在 `/tmp/taurus-pro-storage-release/final/`
（`root-checks.tsv`汇总退出码与耗时）；govulncheck日志另在`govuln/govulncheck.txt`。临时证据经Pi核验后清理，本文保留摘要，持久门禁可重新生成证据。

| 检查 | 退出码 | 耗时 | 说明 |
|---|---:|---:|---|
| `go build ./...` | 0 | 3s | |
| `go vet ./...` | 0 | <1s | |
| `go test -count=1 ./...` | 0 | 10s | 未设置隔离 Redis 地址时 143 个 Redis 相关(子)测试 SKIP（设计如此，不连 localhost:6379），由下一行覆盖 |
| `go test -race -count=1 ./...` | 0 | 12s | 同上 |
| `bash scripts/reliability-integration.sh` | 0 | 30s | 隔离 Redis 8.10.2（race）：281 个 PASS、0 SKIP、0 FAIL；AOF 重启 seed/verify 通过 |
| `go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...`（私有 GOMODCACHE/GOCACHE/GOPATH，未全局安装） | 0 | 43s | 代码可达 0、导入包 0；模块级 1 条 GO-2026-5932（`x/crypto/openpgp`，未调用），与上一轮一致 |
| `bash scripts/test-strict-readiness-args.sh` | 0 | <5s | 20 个非法参数负例均退出 2 且未启动门禁 |
| `bash scripts/strict-readiness.sh --sustained --soak` | **0** | 2057s | run `s1793694753-d47bab`，`STRICT GATE: PASS` + `SOAK (30-minute bounded): PASS`；74 个步骤全部 PASS |

root 检查（build/vet/test/race/集成/govulncheck）在 v1 冻结后执行；v1→v2 只改验收工具 `contract.go`，候选源码清单相同，因此这些结果同样适用于 v2。

### 5.1 严格门禁各场景（逐ID账本 + 磁盘账本二次独立校验 `gate verify` 均为 0 违规）

| 场景 | 接受唯一ID | Processor 调用 | 重复调用（其中脚本化） | done / failed |
|---|---:|---:|---:|---:|
| normal 256B/4KiB × 0/5ms（每组） | 1000 | 1000 | 0 | 1000 / 0 |
| backlog | 1000 | 1000 | 0 | 1000 / 0 |
| consumer-kill（SIGKILL 消费者进程） | 20 | 24 | 4（被杀进程持有的 4 条重投） | 20 / 0 |
| sustained（2400 @80/s，约 30s，不是长稳态） | 2400 | 2400 | 0 | 2400 / 0 |
| **contract**（91 个必需检查全部通过） | 24 | 40 | 16（16） | 17 / 7 |
| redis-sigkill | 85 | 71 | 0 | 81 / 4 |
| oom | 120 | 127 | 7（OOM 下 Ack 失败后的重投，故障场景允许） | 120 / 0 |
| backup-restore | 85 | 50 | 0 | 60 / 5 |
| **soak**（30 分钟） | 17894 | 19359 | 1465（1465） | 17715 / 179 |

故障/种子场景的 done 含种子阶段已完成的墓碑任务，备份场景另有 20 条快照后任务按预期不恢复，不能用“接受数减调用数”推断丢失。

负例（全部按预期失败）：坏 Redis 地址；归属标签不符的诱饵容器在 5 种越权上下文 × kill/exec/cp/rm 下全部被拒且仍在运行；串用/改 run/非法 owner 清理被拒；
**soak 阈值负例**（`--soak-limit-redis-bytes 1`）：首个采样即越界，尚未提交任务时生产在0.1s内停止，场景失败且资源被精确清理；仅证明首次采样越限能阻止启动，未证明已有负载时中途越限停止；
账本变异 8 类：unknown_id、payload_mismatch、accepted_not_confirmed、query_error、unexpected_duplicate、empty_ledger、missing_required_check，
以及本轮新增的 `drop_last_call`（删掉 `c-exhaust-00000` 第 3 次调用 → `call_count_mismatch`）；不可读账本退出 4。

### 5.2 30 分钟有界 soak（结果名 `soak`）

- 证据：`final/strict/s1793694753-d47bab/main/soak.result.json`、`soak.ledger.jsonl`（74,626 行，10.9MB）、
  `soak.samples.jsonl` / `soak.samples.csv`（195 个连续采样）、`soak.expect.json`。采样起止 2026-11-03T16:34:30+08:00 → 17:06:47+08:00。
- 配置（运行时核验，不只信参数）：Redis 8.10.2 固定摘要，`maxmemory=134217728`、`noeviction`、`appendonly yes`、`appendfsync always`，
  容器 `memory.max=402653184`（384MiB）已读回核验；1 CPU由启动参数设置、未读回确认，127.0.0.1 随机端口；GOMAXPROCS=2；8 Worker；租约 30s；MaxRetries 2；DedupWindow 2 分钟；
  10 条/秒单一限速器；载荷 256B–4096B。
- 时长：生产窗口 **1800.0s**（计划 1800s）；排空 1.5s；墓碑到期等待 135s；场景总计 1937.4s（硬上限 2340s）。整轮门禁 2057s。
- 负载：提交调用 17,999 次（上限 18,000，含 36 次冲突探针、36 次窗口内重复探针、33 次到期后重提交），新任务 17,894 个，
  全部被接受（拒绝/不确定 0）；Processor 调用 19,359 次 = 17,894 + 1,465 次脚本化重复（retry1 895、panic1 358、重放 179、到期重提交 33）。
- 逐ID对账：17,894 个接受ID全部有终态观察（17,715 done，179 fatal 类按脚本失败留存）；**0 个非脚本化重复**；Go校验器通过；随后Pi另用Python核对接受/执行/终态的唯一ID集合、载荷SHA256/长度、调用配对及精确调用次数，全部一致。此为Pi交叉核验，未固化为持久独立重算工具，不声称冻结脚本已有逐ID Python重算门禁。
- 墓碑到期（不把到期当丢失）：排空时刻逐ID三态核对 17,715 个 done ID——1,169 个仍在窗口内且摘要一致、PTTL≤2m；16,546 个已过“最早可能到期时刻”
  按预期不存在；违规 0。等待一个窗口后静止状态：stream/pending/delayed/leases/墓碑均为 0，仅剩 179 条失败留存记录，`ListFailed` 恰好返回这 179 条且 payload 一致。
- 失败留存巡检：每 15s 一次 `ListFailed(1000)`，只重放 replay 类（179 条全部重放并完成），未出现任何未预期的失败任务。
- 延迟（提交前→Processor 开始）：P50 91.7ms / P95 184.3ms / P99 194.0ms / max 202.0ms（主要是 200ms 轮询间隔）；完成观察上界 ≤1.03s；
  观察吞吐 9.84/s（受 10/s 限速）。不是容量测试。
- 资源与安全阈值（全部低于资源上限，任一越过即失败并停止；本次 0 次越过，AOF 写状态始终 ok）：

| 指标 | 阈值 | 实测最大 | 首/末采样 |
|---|---:|---:|---|
| Redis used_memory | 96MiB（maxmemory 128mb） | 3.47MiB | 1.51MiB / 3.25MiB |
| Redis used_memory_rss | （未设阈值，记录） | 28.9MiB | 26.4MiB / 28.9MiB |
| 容器 memory.current | 320MiB（容器 384MiB） | 81.0MiB | 13.6MiB / 21.3MiB |
| 门禁进程 Go Sys | 768MiB | 50.4MiB | 33.2MiB / 50.4MiB |
| 积压（已接受未终态） | 2000 | 5 | 0 / 0 |
| 失败留存 | 900（ListFailed 上限 1000） | 179 | — |
| 采样间隔 | 30s | 10.08s | — |
| 证据盘可用空间 | ≥1GiB | — | 满足 |

- 趋势（生产阶段 5 分钟后 150 个点的最小二乘斜率，**只是 30 分钟内的观察，不是“永不泄漏”的证明**）：
  Redis used_memory +21KB/分钟（2.70→3.04MiB）；队列键（记录+墓碑）+5.9/分钟，与失败留存的预期增长（179 条/30 分钟≈6/分钟）一致，墓碑在约 1,190 处持平；
  容器memory.current从首采样约14.2MB升到1670s约84.9MB，1680s骤降至约16.9MB后再次上升；趋势窗口斜率约+1.31MB/分钟，最高81MiB，趋势窗口末值20.5MiB。含页缓存，形态可能与AOF重写有关，**未验证原因，不能据此宣布无内存增长风险**；
  门禁进程 heap 3.7→21MiB，主要是验收工具自己保存的逐ID状态表（随任务数线性），不属于候选库；goroutine 13→13。
- 范围：生产者与消费者同进程；只有一个 Manager 实例；速率/载荷/Worker 都是任务给定的有界值；不代表目标负载、容量或更长时间运行。

### 5.3 资源与进程

- 所有容器/卷带 `taurus.strict.owner=taurus-pro-storage-strict`（诱饵为 `-decoy`）与 `taurus.strict.run=<run>` 标签，按完整 64 位容器ID/卷名登记、
  每次 kill/start/exec/cp/rm 前核验；最终按清单精确删除并逐个 inspect 确认不存在。本轮最终运行创建 15 个资源（见 `created-resources.txt`）。
- 本机 `docker ps -a` 与 `docker volume ls` 在自测、v1 中止运行、开发复跑与最终运行前后完全一致（13 个他用容器、408 个卷未变）。
  这只说明本轮观察窗口内没有变化，不能证明全机其它资源在本轮之外未变。
- `scripts/reliability-integration.sh`（既有脚本，本轮未改）按唯一名称 `rm -f`/`volume rm` 清理自建资源，不使用 owner 标签核验。
- 无残留门禁/消费者进程。临时证据保留在 `/tmp/taurus-pro-storage-release/` 供 Pi 核验后清理；开发编译副本 `devcompile/`（含本地 replace，仅用于开发期编译）
  与 `devcheck/` 不是验收证据。

## 6. 发布内容清单（未 git add / commit）

应交付：
- 候选源码：`pkg/**`（修改、新增与删除的 `reader.go`/`utils.go`/`worker.go`，含新增测试），根 `go.mod`/`go.sum` 与基线相同（未改）。
- 示例：`examples/queue_example/{basic,advanced,config}/main.go`（修改）、`examples/reliable_queue_example/main.go`（新增）。
- 验收工具：`scripts/strict-readiness.sh`、`scripts/test-strict-readiness-args.sh`、`scripts/reliability-integration.sh`、
  `scripts/readiness/**`（独立模块，有自己的 `go.mod`；不进入候选模块 zip，不影响根模块依赖）。
- 文档：`README.md`、`CHANGELOG.md`、`docs/queue-reliability.md`、`docs/single-redis-operations.md`、`docs/single-redis-readiness.md`、
  `docs/production-reliability-plan.md`、`docs/production-reliability-verification.md`、`docs/release-readiness.md`。

建议排除：`.pi/`（编排任务书）、`HANDOVER.md`（内部交接记录，含会话ID）——是否入库由维护者决定；`/tmp` 下所有证据、合成模块 zip、
本地模块缓存。仓库内没有被忽略的构建产物（`git status --ignored` 为空）。

## 7. 版本号与发布

- 依据实际标签：本地与 origin 最新 `v0.2.0` → 基线 `33292a8`。模块路径无 `/vN`。
- 建议见 `CHANGELOG.md`：兼容当前模块路径的 v0 破坏性 minor（如 `v0.3.0`），或经确认后改模块路径发布 `v2.0.0`。**未自行选择版本、未打标签。**

## 8. 未测 / 限制

1. 生产 Redis 拓扑与版本、Sentinel 切换、Cluster、Redis 8.10.2 以外任何版本（含 6.2–7.x）。
2. 目标负载、容量、积压规模；>30 分钟运行；多实例消费者长期并发；独立生产者进程与网络延迟。
3. 主机掉电、磁盘/卷故障、写满后重启恢复；appendfsync always 在目标磁盘上的延迟。
4. 旧 List 数据迁移（无工具，不支持）。
5. DB 语义仅 SQLite；MySQL/Postgres/ClickHouse 未验证。
6. 挂死但存活的 Processor 无最长执行时间；maxmemory 顶满时队列不能自愈且会重复投递；ListFailed 无翻页——属于需另行授权的实现层决策，本轮未改。
7. 离线账本不是抗篡改审计；门禁与 shell 同时被 SIGKILL 时需按 manifest 人工精确回收（见 strict-readiness.sh 头注释）。
8. 验收工具尚有非阻断强度缺口：soak被拒/不确定提交未直接作为失败门禁（本次均0）；部分required检查由场景自行声明；离线账本不抗一致性篡改。只承诺本次实际验证结果，不保证未来任意参数都同等覆盖。

## 8.1 Pi独立核验与最终复审

- Pi两次重新计算冻结清单中的每个文件SHA256，候选源码28文件、工具26文件、发布文档/示例10文件均与清单一致（工具清单哈希`7b021352…`）。验证当前工具身份与冻结身份一致；原运行未内置工具复制件哈希绑定，不能补称已具备该机制。
- Pi核对soak原始账本：17894个接受唯一ID与执行/终态集合相同；SHA256/长度一致；19359次start/end配对完整、脚本期望调用次数及默认1次规则一致；1465次重复均可解释。195采样包含最后seq=-1的final哨兵，前194个连续，AOF状态均ok，间隔及安全阈值核对通过。
- Pi独立执行20个参数负例及`bash scripts/strict-readiness.sh --sustained`，run `s1793697073-469e34`，退出0。默认10个结果、contract、短稳态、变异门禁和资源清理通过；未重复30分钟soak。
- 新会话只读复审：Claude Code `claude-opus-5-5`，启动脚本`--effort high`，session `fca419b8-5f16-452f-8a22-3dc8a77eee3c`，wrapper success。未发现真实发布阻断项；报告措辞已修正，非阻断工具强度缺口保留。
- Pi用`git rev-parse v0.2.0^{commit}`核实基线为`33292a82db8211f79b190dc9ad2bb8915d1f9933`。
- 最终标签资源查询及本轮gate进程检查未发现残留。原始证据核验后定向清理，未提交、推送、打tag或发布。

## 9. 结论

在冻结 v2 的候选源码与验收工具上，限定本机隔离 Redis 8.10.2，所有默认门禁、公开 API 契约、负例与 30 分钟有界 soak 均按实际输出通过。
结论：**公共组件候选在明确支持范围内已达到可进入版本发布流程的标准，当前无已知阻断项。** 这是有界独立验收，不依赖真实业务，也不是对任意生产环境的全面保证。没有发布；版本号、提交、推送和tag发布仍由维护者分别授权。生产使用方负责按运维清单落地环境与业务幂等。
