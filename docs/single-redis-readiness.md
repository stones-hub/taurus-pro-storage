# 单机 Redis 有限上线验收（候选报告）

> 状态：**有限负载与故障证据报告，不是严格上线验收通过。** Pi已独立复跑矩阵/积压/SIGKILL/OOM；独立方法复审指出采集程序缺少严格失败门禁及部分账本，不能把退出0当成通过。下列执行器原始数值曾由临时JSON/CSV/日志核对，收口摘要保留在本文。
> 验收范围仅限本机隔离的 Redis 8.10.2 单机容器，不代表生产环境、主机故障或长期稳定性。

## 1. 候选身份与环境

- 基线 HEAD `33292a82db8211f79b190dc9ad2bb8915d1f9933` + 当前未提交改动。运行源码清单：`evidence/candidate-source-sha256.txt`（34 个 .go/go.mod/go.sum 文件，清单自身 SHA256 `2a49a972…8a3e`）；验收结束后重新计算与清单完全一致，`git status` 与开始时一致。
- 打包：排除 `.pi/ docs/ scripts/ HANDOVER.md` 后做成 module zip，经 file module proxy 以合成版本 `v0.0.0-readiness.20261103121429` 装入独立调用模块（无 `replace`）。zip SHA256 `e5886b4e…ef0ae`。
- Redis：`redis@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0`（8.10.2），仅绑定 127.0.0.1 随机端口，容器 1 CPU / 384MiB，`maxmemory 128mb`、`noeviction`、`appendonly yes`、`appendfsync always`，独立卷。**为便于隔离我设置了 `--save ""`（关闭自动 RDB）**，生产默认配置可能不同。
- 调用方：`GOMAXPROCS=2`，生产并发 8，Worker 8，连接池 32，其余 Config 为默认值（租约 30s，PollInterval 200ms，MaxRetries 3，DedupWindow 24h）。
- **生产者与消费者在同一进程**，不是独立生产部署；吞吐/延迟只反映该拓扑。
- 总负载时间约 70s（矩阵约 8s + 积压约 2s + 持续 60s），低于 3 分钟上限；资源核对异常：Pi前后快照显示他用taurus-pro-milvus-soak-wxsh4f容器及同名卷消失，不能声称其他资源完全未变。Docker事件证实其收到signal9后被删除，但不记录调用者，当前无法归因；本轮未进行恢复或删除他用资源。

## 2. 有限矩阵（每场景 3000 条上限 / 15s）

原始：`matrix-<name>.json`、`lat-<name>.csv`、`samples-<name>.csv`。生产者 8 并发无限速，所以**消费速度远低于入队速度，延迟主要是积压排队时间**，不是单条处理延迟。

| 场景 | 接受成功 | 业务调用 | 唯一任务 | 重复 | 入队失败 | 入队耗时 s | 入队→最后处理 s | 处理吞吐/s | 入队前载荷创建→处理开始 P50 / P95 / P99 ms |
|---|---|---|---|---|---|---|---|---|---|
| 256B, 无延迟 | 3000 | 3000 | 3000 | 0 | 0 | 0.322 | 0.685 | 4378 | 251.8 / 353.6 / 360.5 |
| 256B, 5ms | 3000 | 3000 | 3000 | 0 | 0 | 0.260 | 3.091 | 971 | 1504.0 / 2702.0 / 2797.7 |
| 4KiB, 无延迟 | 3000 | 3000 | 3000 | 0 | 0 | 0.408 | 0.807 | 3720 | 344.6 / 395.0 / 399.6 |
| 4KiB, 5ms | 3000 | 3000 | 3000 | 0 | 0 | 0.332 | 3.031 | 990 | 1437.7 / 2563.1 / 2667.3 |

Stop 之后 Redis 侧核对（四个场景相同）：stream 长度 0、pending 0、delayed 0、failed 0、leases 0、任务记录 `m:*` 0；墓碑 `d:*` 3000（DedupWindow=24h 内保留，属预期，意味着稳定ID任务每条在 Redis 多占一个键约 24h）。峰值 used_memory：3.6MB / 4.5MB / 12.0MB / 17.4MB（采样 500ms）。

5ms 场景处理吞吐约 970–990/s，低于 8 Worker 理论上限 1600/s（轮询间隔 200ms 与领取开销），不是 Redis 极限。

## 3. 1000 条积压

`backlog.json`：无消费者经公开 `ReliableEngine.Enqueue` 预置 1000 条（1KiB，耗时 0.381s），预置后 stream_len=1000、记录键 1000。启动 Manager（8 Worker，5ms 处理）后 0.951s 全部处理；调用 1000、唯一 1000、重复 0；Stop 无错误；最终 stream/pending/delayed/leases/failed/记录均为 0，墓碑 1000。

## 4. 60 秒有界持续负载

`sustained.json`、`samples-sustained.csv`（12 个 5s 采样）、`docker_stats_samples`。50 条/s 限速、总 3000 封顶、1KiB、5ms、8 Worker：接受 3000、唯一 3000、重复 0、入队失败 0；入队 60.0s，到最后处理 60.18s；P50/P95/P99/max = 94.0 / 185.6 / 195.7 / 210.0 ms（主要来自 200ms 轮询间隔）。采样期间 used_memory 2.33–2.87MB，最大 pending 3、leases 3、stream_len 10、expired_leases 0；结束时无遗留状态（仅墓碑 3000）。docker stats 10 次：Redis 容器 CPU 峰值 4.17%，内存约 28MiB/384MiB。docker stats 本身耗时 ~2–7s，所以采样间隔 >5s。
**这只是一次 60 秒短测，不是长稳压测**，不能外推内存/键增长趋势（墓碑 24h 内线性增长未观察）。

## 5. Redis SIGKILL 持久化恢复

`crash.json`、`crash-dump-before/after.json`。无消费者预置 ready 50 / delayed 10（延迟 40s）/ inflight 10（租约 20s）/ failed 5 / 完成去重墓碑 10。`docker kill --signal KILL`（容器退出码 137），同卷 `docker start`。

- 杀前与重启后的全量键转储（hash/zset/stream+消费者组+PEL）SHA256 完全相同（`digest_equal=true`）；墓碑 PTTL 前 86399930ms、后 86399260ms；Redis 日志显示从 AOF base+incr 加载。kill到恢复可PING总计约0.605s；0.315s仅为docker start命令完成后的waitPing时间。
- 恢复后消费（公开 ReliableEngine）：ready 50/50、delayed 10/10、inflight 10/10（全部 `Recovered=true`、`Attempt≥2`），无预期外投递；failed 5 条仍可 ListFailed，ReplayFailed 1 条后被消费；已完成任务用不同内容重复入队返回 `ErrIDConflict`（去重状态存活）；最终仅剩 4 条 failed。
- 范围：只证明**容器内 Redis 进程被杀**后同卷恢复；不证明主机掉电、磁盘损坏或 fsync 之外的介质故障。

## 6. noeviction 容量耗尽

`evict.json`、`evict2.json`、`evict.log`。独立实例 `maxmemory 16mb noeviction`，先入队 20 条，再写 125 个 100KiB 填充键至返回 `OOM command not allowed`。

- 满载后 100 次 AddData：成功 16、失败 84，**84 个错误全部带 OOM 文本**；成功的记录全部存在于 Redis（假成功 0），失败的均不存在（假失败 0）。只能证明填充SET首次OOM后又接受16条；缺少逐次内存采样，不能断言这些成功均发生在越过maxmemory之后。
- **关键发现**：OOM 期间 `Ack` 也被拒绝（日志 `ack task pre-000 failed: OOM command not allowed ... (task may be redelivered)`），`Receive` 也持续失败。打开 Processor 闸门 6s 后，唯一处理 3 个，Redis 中 pending=3、leases=3 未被确认。也就是说：**内存顶满后队列既无法入队也无法消费/确认，不能靠自身消化来释放空间**，必须外部删数据/加内存。
- 释放填充键后：50 条入队全部成功，累计接受 86、全部被处理、accepted_never_processed=0。但那 3 条在 OOM 期间已被 Processor 执行过、Ack 失败，租约到期后又被重新投递（`evict2.json`：3 条再次被处理）→ **至少一次的重复实际发生**，Processor 必须幂等。
- 未做宿主机 OOM 测试；容器 384MiB 内未触发 OOM kill。

## 7. AOF 写失败注入

`aoffail-always.json`、`aoffail-everysec.json`。方法：独立容器 `--tmpfs /data:size=4m`（不触碰宿主机磁盘），`docker exec -u 0` 用 dd 写满，`df` 显示 100%、dd 报 `No space left on device`，Redis 日志出现 `Short write while writing to the AOF file`——真实 ENOSPC 注入已被证实（`injection_proven_enospc=true`）。

- `appendfsync always`：注入后第一次写使 **Redis 进程自行退出**（日志 `Can't recover from AOF write error when the AOF fsync policy is 'always'. Exiting...`，容器 exit=1）。随后 20 次 AddData 全部失败（connection refused），0 假成功。tmpfs 随容器停止清空，所以**写满后重启恢复这一步未测**（标未验证）。含义：磁盘写满会直接导致 Redis 宕机，需要进程守护 + 磁盘告警。
- `appendfsync everysec`（对照，非任务指定配置）：注入后首次 AddData 仍返回成功（post-000），`aof_last_write_status` 变为 err，其后 19 次返回 `MISCONF Errors writing to the AOF file: No space left on device`；释放空间后 2s，10 次入队全部成功、`aof_last_write_status:ok`。当次AOF写入未完整成功；未逐ID核对后续补写或重启恢复，不能断言这条任务最终丢失。
- 未测：真实宿主磁盘/卷故障、慢盘、部分写后的 AOF 修复（redis-check-aof）。

## 8. 备份与恢复

`backup.json`、`backup-dump-*.json`、`backup-files/dump.rdb`。第一次尝试因我的脚本 `docker cp` 占位符 bug 恢复为空（已移至 `evidence/invalid-attempt1-backup-cp-bug/`，不计入结论），修复后重跑。

- 种子同 §5；`BGSAVE` 0.104s，`redis-check-rdb`：`Checksum OK`、89 keys。`docker cp` 导出到 tmp；备份后再写入 20 条。
- 恢复到新容器 + 新卷（先 `appendonly no` 载入 RDB，再 `CONFIG SET appendonly yes` 重写 AOF）：创建流程开始到载入完成约0.302s；start命令完成后waitPing约0.105s，含启用 AOF 全程0.443s（本数据量）。恢复库转储 SHA256 与备份时刻转储完全一致；备份后写入的 20 条在恢复库中为 0。恢复后 ready 50/50、delayed 10/10、inflight 10/10 被消费，failed 5 条保留，已完成墓碑去重有效。
- 人工步骤：BGSAVE→核对→复制 RDB→新卷/容器→拷入→启动→（AOF 实例须先关 AOF 载入 RDB 再开启，且启动参数也要改为 appendonly yes）。**未实测**“appendonly yes + 空目录 + dump.rdb”的坑，仅凭官方行为推断，需在流程中避免。
- 数据边界为RDB快照时刻，不是文件复制完成时刻；快照之后的写入不在该备份内。RPO取决于距快照时刻的时间间隔。RTO 取决于数据量，本次仅 ~89 个键。

## 9. 监控现状与建议（`monitor.json`）

可用 Redis 原生命令采集（本报告调用方的 `queueState` 已实现，键前缀 `taurus:q2:{<name>}:`）：`XLEN ready`（含在途）、`XPENDING ready workers`（在途）、`ZCARD delayed/leases/failed`、`ZCOUNT leases -inf <now>`（租约已过期=疑似卡死/崩溃）、`SCAN m:*`/`d:*` 计数、`XRANGE ready - + COUNT 1` 的 ID 时间戳（最老未完成条目年龄）、`ZRANGE delayed 0 0 WITHSCORES`、`INFO memory/persistence`。

缺口与风险（如实记录，未改 API）：
- **没有可靠的“无进展”或“最老待处理任务”指标**：成功处理不留痕，只能用最老 stream 条目年龄近似（含在途与重试）。
- **挂死但存活的 Processor 会无限续租**：实测 8s 内 `expired_leases` 始终 0、租约分值每秒前移、记录年龄线性增长；此时只能靠“最老条目年龄/记录年龄”告警。Stop 在 3s ctx 下返回超时错误（耗时 5.0s），Processor goroutine 仍在运行，Stop 后租约到期（expired_leases=1）才可被他人恢复。
- **ListFailed 无分页**：1100 条失败时 `ListFailed(1000)` 仅返回 1000 条，1001 或 0 返回错误；失败记录 TTL 为 -1（`FailedRetention=0` 时永久留存），需用 `ZCARD failed` 告警并设置保留策略。
- 墓碑键与稳定ID 任务数同量级、24h 保留，需把它计入内存规划。

## 10. 未测 / 未验证清单

1. 独立生产者进程、网络延迟、多实例消费者并发争抢。
2. 长时间（>60s）稳态、内存/键增长趋势、墓碑过期后的行为。
3. 主机掉电 / 真实磁盘或卷故障 / fsync 之外的介质损坏；写满后重启恢复（tmpfs 无法验证）。
4. appendfsync always 在目标硬盘上的真实写延迟（本机 Docker VM 数值不可外推）。
5. Redis 其他版本、Sentinel/主从切换、Cluster。
6. 业务 Processor 真实耗时、payload 大于 4KiB、超过 8 Worker 的情形。
7. “appendonly yes + 空 AOF + dump.rdb” 恢复陷阱（仅文字提示）。
8. Pi已独立复跑矩阵、积压、SIGKILL恢复及OOM/释放后重投；只读方法复审已完成，限制见下节。备份/ENOSPC/60秒短稳态本轮仅核对已有代码与原始记录，未由Pi重跑。

## 11. Pi独立复跑与方法复审

- Pi独立使用同一打包候选在新隔离实例复跑：四组各3000条，接受/唯一处理均3000、重复0、缺失0；Stop后pending/leases/delayed/failed/任务记录为0。吞吐参考4296.5、965.4、3893.0、989.6条/s，P99到处理开始分别372.328、2834.043、395.678、2676.468ms。不是容量上限或完整确认延迟。
- Pi复跑1000条积压约0.957s消化，Stop后状态核对通过。SIGKILL恢复ready50/delayed10/inflight10均收到，选定状态转储相等；kill到ready约0.526s。
- Pi复跑OOM：100次接收16成功/84报OOM，接收假成功/假失败未观察到；OOM期间2条已执行未确认，释放后86个唯一任务全部执行，但仍有2条pending，后续进程重新投递这2条才清空。至少一次重投确实发生，Stop成功不等于所有任务已确认。
- 方法复审指出：采集程序多数结果只写JSON，不自动失败退出；Redis状态查询错误被忽略可能表现成0；恢复按ID的map会覆盖重复，并在达到唯一ID数后停止；Ack前已记入接收结果。不能证明恢复零重复、完整Redis内部状态一致。
- 吞吐计时存在潜在截止点偏差：Stop前记录结束时间、Stop后取最终计数；多Worker的last.Store也不是严格最大值。本次数字内部一致，但不作为精确容量承诺。
- dump比对仅覆盖选择的hash/zset/stream/组/PEL字段，不包含完整TTL和全部内部元数据。备份/ENOSPC没有Pi重跑；everysec首次成功后的最终持久化未核对。
- 未保存可完整重建的逐ID接受/处理账本；bad_payload只校验分隔格式，不等于完整载荷校验。
- 资源边界：caller当前调用点未发现广域删除，但dkAny仅检查首级子命令、没有实现注释所称的名称前缀校验。不能排除外层清理，也不能归因他用Milvus资源删除。Docker事件不含调用方身份。

## 12. 严格门禁最终收口

Pi独立执行 `bash scripts/strict-readiness.sh --sustained`，run `s1793692124-ccec9a`，退出0。默认9个子场景加短稳态全部通过；14个非法参数负例均退出2。单元/race、打包测试、根go.sum信任基准、候选解析身份、依赖verify、7类账本变异及不可读账本门禁通过。

| 场景 | 接受唯一ID | Processor调用 | 重复调用 | 最终done / failed |
|---|---:|---:|---:|---:|
| 四组正常矩阵（每组） | 1000 | 1000 | 0 | 1000 / 0 |
| 积压 | 1000 | 1000 | 0 | 1000 / 0 |
| 消费者强杀 | 20 | 24 | 4 | 20 / 0 |
| OOM及释放后恢复 | 120 | 126 | 6 | 120 / 0 |
| Redis强杀状态恢复 | 85 | 71 | 0 | 81 / 4 |
| RDB恢复 | 85 | 50 | 0 | 60 / 5 |
| 短稳态 | 2400 | 2400 | 0 | 2400 / 0 |

故障恢复种子包含已完成/失败任务，因此done可包含没有调用Processor的既有墓碑；备份场景另有20条快照后任务按预期不恢复，不能用85减50断言丢失。短稳态约30.284秒，预设80条/秒，不是60秒或长稳态测量。最终本轮资源/进程检查未发现残留，候选源码与根依赖在运行前后不变。

候选源码清单SHA256 `1d9b3271e72ac1e86a1cad4b6b0570a9505d76b4791bc6609a8d8923dbcbf6a3`（28文件）；此次合成模块zip SHA256 `ce7dbcf5e85d49fa795dfad3395ce7afc60bd9ccac9fca29a4683dad73c3d9f3`。zip身份含合成版本，不等于正式发布包身份。

新会话只读复审session `a4ac6747-5774-4d92-a4d1-d5a5524e141b` 未发现坏结果误PASS路径；其提出的信号/依赖/参数/清理错误码/取消ctx登记等收尾项经Claude session `73d88dde-6359-488f-b8f2-83251a18980b`修正，由Pi代码核对及上述完整重跑验证。不是宣称修后又做了一轮Claude复审。

残余边界：清理中第二次信号、gate自身SIGKILL、panic注入及构建TERM传递未实测；创建到登记前的不可中断窗口无法完全消除，脚本和gate同时SIGKILL需要人工精确回收。离线账本不是抗篡改审计系统；required集合由场景声明，完整生命周期被一致删除不保证检出。短稳态只有时限上界，没有最短时长/速率门禁。当前门禁不编译DB包（DB此前另轮build/test通过），不替代全库测试。原始临时证据核验后清理，本文保存结果摘要；可运行持久脚本重新生成。

## 13. 结论与建议（未全面放行）

> 后续：严格门禁入口 `scripts/strict-readiness.sh`（逐ID账本、Ack 后对账、负例验证、归属标签资源管理）与运维清单 `docs/single-redis-operations.md` 已补充；本报告 §11 指出的采集方法缺口由该门禁取代，但本节结论仍不是生产放行。


- 原始证据显示常规矩阵/积压/短稳态计数一致；SIGKILL及RDB转储匹配。OOM测试未观察到接收假成功；AOF always故障后请求失败。everysec对照出现首次返回成功但未持久化，不能笼统表述为磁盘写满没有假成功或已保证落盘。
- 延迟采集点是载荷创建时刻到Processor开始，不含模拟处理耗时和Ack完成，不能称为完整端到端确认延迟。
- 但存在需要决策的行为：(a) **maxmemory 顶满后无法 Ack/Receive，队列自身无法自愈，且产生重复投递**；(b) always 模式磁盘满会让 Redis 直接退出；(c) 挂死 Processor 续租不可见。若要在代码层处理（例如 Ack 路径在 OOM 下可执行、最长执行时间、分页 ListFailed），属于正式代码修改，本次未做，需另行授权。
- 因此**不能据此直接宣布生产可上线**；至少需配置内存/磁盘/failed 数量/最老条目年龄告警、进程守护、定期 RDB 备份与演练，并要求 Processor 幂等。
