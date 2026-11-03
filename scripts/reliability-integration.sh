#!/usr/bin/env bash
# 可靠性集成验收：在独立的 Redis 容器（固定镜像摘要、127.0.0.1 随机端口、独立卷）中
# 运行队列/redisx 集成测试与进程级故障测试，结束时清理自建资源。
# 不会连接默认的 localhost:6379，也不会触碰其它容器。
#
# 用法: scripts/reliability-integration.sh [logdir]
set -euo pipefail

IMAGE="redis@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0"
SUFFIX="$(date +%s)-$$"
NAME="taurus-pro-storage-reliability-redis-${SUFFIX}"
VOLUME="taurus-pro-storage-reliability-data-${SUFFIX}"
LOGDIR="${1:-/tmp/taurus-pro-storage-reliability}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$LOGDIR"

cleanup() {
  set +e
  docker rm -f "$NAME" >/dev/null 2>&1
  docker volume rm "$VOLUME" >/dev/null 2>&1
  echo "cleanup: removed container $NAME and volume $VOLUME" | tee -a "$LOGDIR/cleanup.log"
}
trap cleanup EXIT

# 选一个空闲端口并固定（docker restart 后随机映射端口会变化，持久化阶段需要地址不变）
PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
docker volume create "$VOLUME" >/dev/null
docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:6379" -v "$VOLUME:/data" "$IMAGE" \
  redis-server --appendonly yes --appendfsync always >/dev/null
export TAURUS_TEST_REDIS_ADDR="127.0.0.1:${PORT}"
export TAURUS_TEST_REDIS_CONTAINER="$NAME"

for i in $(seq 1 50); do
  if docker exec "$NAME" redis-cli ping 2>/dev/null | grep -q PONG; then break; fi
  sleep 0.2
done
docker exec "$NAME" redis-cli INFO server | grep -E '^redis_version' | tee "$LOGDIR/redis-version.txt"
echo "redis addr: $TAURUS_TEST_REDIS_ADDR image: $IMAGE" | tee -a "$LOGDIR/redis-version.txt"

cd "$ROOT"

# 1) 常规集成测试（含 race）。持久化用例与辅助子进程用例由下面的阶段/父测试驱动，故在此显式排除；
#    其余用例出现 SKIP 即视为失败，避免“跳过”冒充通过
go test -race -count=1 -v -skip '^(TestPersistenceAcrossRestart|TestHelperClaimAndBlock)$' ./pkg/queue/... ./pkg/redisx/... ./pkg/db/... 2>&1 | tee "$LOGDIR/integration.log"
if grep -E '^\s*--- SKIP' "$LOGDIR/integration.log"; then
  echo "FAIL: 存在被跳过的测试" >&2; exit 1
fi

# 2) 持久化/重启：seed -> docker restart -> verify
TAURUS_PERSIST_PHASE=seed go test -count=1 -run '^TestPersistenceAcrossRestart$' -v ./pkg/queue/ 2>&1 | tee "$LOGDIR/persist-seed.log"
docker restart "$NAME" >/dev/null
for i in $(seq 1 50); do
  if docker exec "$NAME" redis-cli ping 2>/dev/null | grep -q PONG; then break; fi
  sleep 0.2
done
TAURUS_PERSIST_PHASE=verify go test -count=1 -run '^TestPersistenceAcrossRestart$' -v ./pkg/queue/ 2>&1 | tee "$LOGDIR/persist-verify.log"
grep -q -- '--- PASS: TestPersistenceAcrossRestart' "$LOGDIR/persist-verify.log"

echo "ALL INTEGRATION CHECKS PASSED (redis $(grep redis_version "$LOGDIR/redis-version.txt" | head -1))"
