#!/bin/bash
# Docker 镜像构建脚本（Sticky note / notepad-src）
#
# 权威构建入口：在具备 docker 的 Linux 主机（测试虚拟机 192.0.2.10）上运行。
# 前置：先 ./scripts/build-all.sh 生成 release/v<VER>/sticky-note-<VER>-linux-amd64/{sticky-note,www}
#
# 两种构建模式（端口 13389 / 数据卷 / uid 均对齐项目根 Dockerfile）：
#   在线（默认）：生成 alpine:3.21 单阶段 + buildx 多平台（需 Docker Hub 可达或有 alpine 缓存）
#   离线（--offline 或自动探测到 Docker Hub 不可达且 alpine 无缓存）：
#                自动改用 FROM scratch 零拉取构建（scratch 无 wget/curl，故无 HEALTHCHECK）
#
# 用法：
#   ./scripts/build-docker.sh            # 在线（alpine），若 alpine 不可用自动降级 scratch
#   ./scripts/build-docker.sh --offline  # 强制 FROM scratch 离线构建

set -e

PROJECT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"
cd "$PROJECT_DIR"

VERSION=$(cat VERSION)
[ -z "$VERSION" ] && echo "❌ 无法获取版本号" && exit 1

APP_NAME="sticky-note"
IMAGE_NAME="sticky-note-app"
RELEASE_DIR="release/v${VERSION}"

[ ! -d "${RELEASE_DIR}/${APP_NAME}-${VERSION}-linux-amd64" ] && echo "❌ 请先运行 ./scripts/build-all.sh 生成 linux 构建产物" && exit 1

# ---- 离线模式解析 ----
OFFLINE=0
for a in "$@"; do
  [ "$a" = "--offline" ] && OFFLINE=1
done

use_scratch=0
if [ "$OFFLINE" = "1" ]; then
  use_scratch=1
  echo "🧊 离线模式（--offline）：强制 FROM scratch"
else
  # 自动探测：alpine 是否已本地缓存？否则尝试 pull（带超时），失败即视为离线
  if docker image inspect alpine:3.21 >/dev/null 2>&1; then
    use_scratch=0
  elif timeout 25 docker pull alpine:3.21 >/dev/null 2>&1; then
    use_scratch=0
  else
    echo "⚠️ alpine:3.21 无缓存且 Docker Hub 不可达 → 自动切换 FROM scratch 离线构建"
    use_scratch=1
  fi
fi

echo "============================================"
echo "  Docker 镜像构建 v${VERSION}  ($( [ "$use_scratch" -eq 1 ] && echo scratch离线 || echo alpine在线 ))"
echo "============================================"

# 清理旧镜像
docker rmi $(docker images "${IMAGE_NAME}" -q) 2>/dev/null || true

# 删除 .DS_Store
find "${RELEASE_DIR}" -name ".DS_Store" -delete 2>/dev/null || true

# ⚠️ v21.165 修复（docker 安装包失效）：构建前**显式置可执行位**。
# 定罪（真机实证）：scratch 分支的 Dockerfile 无法 `RUN chmod`（FROM scratch 无 shell），
# 只能依赖源文件自带 x 位；而源文件由 **Windows go build → sftp 上传** 产生（Windows 侧
# st_mode=0o666，paramiko put 后 VM 上是 -rw-r--r--）⇒ COPY 原样保留 0644
# ⇒ 容器启动 `exec: "/app/sticky-note": permission denied`（203/205 两版均复现）。
# alpine 分支虽自带 `RUN chmod +x`，但本 chmod 对两个分支通吃、且不依赖 BuildKit
# （`COPY --chmod` 需 BuildKit，此处不引入该依赖）。
BIN_SRC="${RELEASE_DIR}/${APP_NAME}-${VERSION}-linux-amd64/sticky-note"
chmod 755 "${BIN_SRC}"
echo "🔧 已置可执行位: $(ls -l "${BIN_SRC}" | awk '{print $1}') ${BIN_SRC}"

# 切换到 default context
docker context use default 2>/dev/null || true

cd "${RELEASE_DIR}"

if [ "$use_scratch" -eq 1 ]; then
  # ---- FROM scratch 离线构建（零基础镜像拉取，仅 COPY 静态二进制 + www）----
  # v21.112 修复（P2，2026-09-07 深度测试发现）：ENTRYPOINT 不再硬编码 -port=13389，
  # 改用 ENV PORT=13389（应用 config.Load 的 env 兜底链）——此前 Go flag 显式参数
  # 优先于环境变量，导致 `docker run -e PORT=xxx` 完全失效（ENV.md 声称可覆盖），
  # 非 13389 端口映射的容器无法从宿主机访问（真机 -p 19000:19000 实测 000）。
  cat > Dockerfile << EOF
FROM scratch
COPY ${APP_NAME}-${VERSION}-linux-amd64/sticky-note /app/sticky-note
COPY ${APP_NAME}-${VERSION}-linux-amd64/www /app/www
EXPOSE 13389
ENV PORT=13389 DATA_DIR=/app/data UPLOAD_DIR=/app/data/upload
ENTRYPOINT ["/app/sticky-note", "-web-dir=/app/www", "-data-dir=/app/data", "-upload-dir=/app/data/upload"]
EOF
  echo "📄 已生成 scratch Dockerfile（无 HEALTHCHECK：scratch 无 wget/curl，健康用外部 curl 验证；PORT 可经 -e 覆盖）"
else
  # ---- alpine 单阶段（在线，含 CA 证书/时区/wget 健康检查；对齐根 Dockerfile 端口 13389 / uid 1000）----
  cat > Dockerfile << EOF
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata wget \\
    && adduser -D -u 1000 sticky-note

WORKDIR /app
COPY ${APP_NAME}-${VERSION}-linux-amd64/sticky-note /app/sticky-note
COPY ${APP_NAME}-${VERSION}-linux-amd64/www /app/www
RUN chmod +x /app/sticky-note && chown -R sticky-note:sticky-note /app

VOLUME ["/app/data"]
EXPOSE 13389

ENV PORT=13389
ENV DATA_DIR=/app/data
ENV WEB_DIR=/app/www
ENV UPLOAD_DIR=/app/data/upload
ENV TZ=Asia/Shanghai

HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD wget -q -O /dev/null http://localhost:13389/api/health || exit 1

USER sticky-note
ENTRYPOINT ["/app/sticky-note"]
EOF
  echo "📄 已生成 alpine:3.21 单阶段 Dockerfile"
fi

# 构建多平台镜像（默认仅 amd64；如需 arm64 改 DOCKER_PLATFORMS 或下方 --platform）
# 用户规则（2026-08-24）：默认仅 amd64
DOCKER_PLATFORMS="${DOCKER_PLATFORMS:-linux/amd64}"
docker buildx build \
    --builder default \
    --platform ${DOCKER_PLATFORMS} \
    --output type=docker \
    --build-arg VERSION=${VERSION} \
    -t "${IMAGE_NAME}:v${VERSION}" \
    -t "${IMAGE_NAME}:latest" \
    .

rm -f Dockerfile
cd "$PROJECT_DIR"

# ⚠️ v21.165 新增门禁：**构建后必须实启一次**。
# 背景（教训）：此前只做「镜像内二进制 sha256 == release 逐字节一致」的同源校验，
# 该校验**只看内容、不看权限位** ⇒ 权限位缺陷（0644）在线上完全漏过（203/205 均中招）。
# 同源 ≠ 可运行。本断言直接把容器拉起来跑 `-version`，rc!=0 即构建失败。
# 注意：server 二进制**没有 `-version` flag**（那是桌面 exe 的参数），故自检用
# 真实启动（-port 13399 避开可能占用的 13389），以日志中出现启动行为成功判据。
# timeout 超时杀掉属正常（服务已起来），故加 `|| true` 只看日志不停在退出码上。
echo "🔍 启动自检：docker run --rm ${IMAGE_NAME}:v${VERSION} -port 13399"
RUN_OUT=$(timeout 6 docker run --rm "${IMAGE_NAME}:v${VERSION}" -port 13399 2>&1 || true)
if ! echo "${RUN_OUT}" | grep -q "Starting Notepad server"; then
  echo "❌ 镜像启动自检失败（容器起不来，多为 /app/sticky-note 缺可执行位）:"
  echo "${RUN_OUT}"
  echo "   → 排查：docker export 后读 rootfs 内 app/sticky-note 的 mode（应为 0755）"
  exit 1
fi
echo "✅ 启动自检通过：$(echo "${RUN_OUT}" | grep -m1 'Starting Notepad server')"

echo ""
echo "✅ 完成!"
echo ""
echo "📦 本地镜像:"
docker images "${IMAGE_NAME}" --format "  {{.Repository}}:{{.Tag}}" | sort -u
echo ""
echo "💡 运行: docker run -p 13389:13389 -v ./data:/app/data ${IMAGE_NAME}:v${VERSION}"
echo "💡 导出: docker save -o ${IMAGE_NAME}-v${VERSION}-amd64.tar ${IMAGE_NAME}:v${VERSION} && chmod 644 ${IMAGE_NAME}-v${VERSION}-amd64.tar"
