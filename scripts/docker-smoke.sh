#!/usr/bin/env bash
# 候选容器实启门禁：非 root、版本标签正确、/api/health 200；失败时保留日志并非零退出。
set -euo pipefail

IMAGE="${1:?image required}"
PLATFORM="${2:?platform required}"
EXPECTED_VERSION="${3:?version required}"
NAME="sn-candidate-${PLATFORM//\//-}-$$"
PORT="${SMOKE_PORT:-13389}"

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

actual_version="$(docker inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$IMAGE")"
[[ "$actual_version" == "$EXPECTED_VERSION" ]] || {
  echo "镜像版本标签不符: $actual_version != $EXPECTED_VERSION" >&2
  exit 1
}
[[ "$(docker inspect --format '{{ .Config.User }}' "$IMAGE")" == "sticky-note" ]] || {
  echo "镜像未以 sticky-note 非 root 用户运行" >&2
  exit 1
}

docker run -d --name "$NAME" --platform "$PLATFORM" -p "127.0.0.1:${PORT}:13389" "$IMAGE" >/dev/null
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${PORT}/api/health" >/dev/null; then
    echo "候选容器冒烟通过: $IMAGE ($PLATFORM)"
    exit 0
  fi
  sleep 2
done

docker logs "$NAME" >&2 || true
echo "候选容器健康检查失败: $IMAGE ($PLATFORM)" >&2
exit 1
