# 生产可靠性修复：Pi 独立验收记录

## 候选与状态

- 基线：`33292a82db8211f79b190dc9ad2bb8915d1f9933` 加当前未提交工作区。
- 第一轮编码及窄修由 Claude Code 完成，实际模型 `claude-sonnet-5-5`；编码会话 `95b083f1-b1c8-4430-bc2f-97de785e617a`。
- 用户授权编码和隔离本机验收；未授权 commit、push、生产访问、迁移或部署。
- 本文仅记录本机验证范围，不是生产上线批准。
- 窄修后的独立新会话复审完成：session f2f2a7ac-e673-4152-9ad0-937c363e44c5，实际模型claude-sonnet-5-5。正常状态未发现跳过未处理消息或孤儿；指出整数边界校验疑点。
- Pi已真实Redis8.10.2核实HINCRBY拒绝007、超长数字和int64最大值加1。原session已补规范字符串和精确上限校验，Pi已读取代码与边界测试并重跑下面所有验收。
- 最后整数专项独立复审完成：session cbd6575a-df83-4cc7-b7e4-c12b311efac1，实际模型claude-sonnet-5-5，未发现阻断。结论：限定本机Redis8.10.2环境的候选验收通过，不是全面生产放行。

## 已由 Pi 独立运行

1. `git diff --check`：通过。
2. `go build ./...`、`go vet ./...`：通过。
3. `go test -count=1 ./...`、`go test -race -count=1 ./...`：通过。未配置Redis地址的普通测试会跳过Redis场景，不以此替代集成验收。
4. `scripts/reliability-integration.sh`：通过；含真实Redis/race、进程SIGKILL恢复、双实例、Manager长任务自动续租、合作与非合作Stop、损坏记录检查、失败留存重放、响应丢失以及AOF重启。脚本门禁未发现意外SKIP。
5. 将当前工作区候选打成Go module zip，经本机file module proxy装入干净调用模块，无本地replace指向源码；通过公开Manager API完成成功、panic重试、重试后成功、失败查询与显式重放。
6. 通过同一打包候选的独立调用进程入队并领取任务，强制kill该进程，再启动新进程通过公开Manager API恢复；恢复成功且Redis pending为0。
7. Pi独立 `govulncheck@v1.1.4 -show verbose`：0代码可达漏洞、0导入包漏洞。1个模块级GO-2026-5932涉及未导入的openpgp，不等于依赖模块完全无漏洞。窄修未改变go.mod/go.sum。

候选包版本仅用于本机验收，非发布：`v0.2.1-0.20261103020000-33292a82db82`。
候选zip SHA256：`e15786eb89022af11249f41878f81014d72951ad94d6501683d6bec0c0f433c4`。
整数窄修后已重新执行全部构建、vet、测试、race、真实Redis集成（含四路径整数边界）和打包黑盒/SIGKILL恢复，均通过。
打包之后仅修改Pi文档时不影响上述运行代码；任何运行代码、测试或依赖变化后必须重验。

## 环境及清理

- macOS arm64，Go 1.27.3，Docker隔离Redis 8.10.2。
- 固定镜像：`redis@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0`。
- Redis仅监听127.0.0.1独立端口，集成用独立卷并开启AOF/appendfsync always。
- 本轮测试容器和卷已清理；redisx/testdata无测试生成差异。
- 收口时清理本次临时证据、外部调用模块、合成Go模块缓存；持久验收摘要保留在本文和HANDOVER.md。

## 未放行范围与剩余限制

- 生产Redis版本/拓扑、Sentinel故障切换、Cluster、其它Redis版本未验收；Cluster可靠路径明确拒绝。
- 未做目标负载/QPS/积压容量验收，不给吞吐或延迟承诺。
- 旧数据迁移工具与状态保留入口缺失，迁移受阻；不得按新任务丢弃旧重试/延迟/失败语义后宣称完成迁移。
- 导出Reader/Worker/工具函数删除，属于破坏性升级，接入代码需要检查。版本策略与发布另行确认。
- Processor挂死但进程存活时会持续续租，必须由调用方提供超时、幂等和监控；组件无法强制杀死goroutine。
- 损坏任务记录会显式报错并保留现场，可能阻塞同队列领取，需人工修复，不是自动隔离恢复系统。
- ListFailed无分页，单次1–1000条；默认失败永久保留，需要规模监控。
- 普通异步Redis复制不提供故障切换零丢失；自动重试或响应丢失可能造成结果不确定，业务副作用须幂等。
- DB测试仅SQLite，不能替代MySQL/Postgres/ClickHouse真实驱动验收。
- 最终复审低风险剩余项（静态结论，未动态复现）：n达到2^53以上时，HINCRBY结果经过Lua浮点回复，Delivery.Attempt可能失真；Redis中计数字符串仍准确。不影响领取Token或任务状态保证，但不得依赖极端Attempt返回值作精确判断。
- 边界测试仍有断言覆盖不足：非增量r的字段保持检查被条件跳过，Replay会重置r；相关用例证明操作不报错，但没有完整证明所有非增量字段保持原值。后续小修可补，不作为本轮阻断。
