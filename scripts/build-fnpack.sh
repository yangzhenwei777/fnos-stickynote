#!/bin/bash
# build-fnpack.sh — 本地 FPK 组包入口（Windows fnpack.exe）
# 2026-11-03 发布工程轮：组包核心逻辑抽至 scripts/pack-fpk.sh（本地与 CI 单一事实源），
# 本脚本只负责 Windows 侧 fnpack 解析与架构循环。
# 源 fnpack/manifest 不再备份/恢复——组包在临时 stage 内改副本，源树零改动
# （取代 v21.57 的 manifest.bak 环节，同时消除 .bak 被 cp -r 带进 fpk 的隐患）。

set -e

PROJECT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"
cd "$PROJECT_DIR"

# fnpack CLI（本机隔离安装于 .workbuddy/binaries，不入系统 PATH）
FNPACK="${FNPACK:-$HOME/.workbuddy/binaries/fnpack/fnpack.exe}"
[ -f "$(cygpath -u "${FNPACK}")" ] || { echo "❌ 未找到 fnpack CLI：${FNPACK}（下载：developer.fnnas.com/docs/cli/fnpack/）" && exit 1; }

VERSION=$(cat VERSION)
[ -z "$VERSION" ] && echo "❌ 无法获取版本号" && exit 1

APP_NAME="sticky-note"
# v21.96 改名：fnpack 产物文件名取自 manifest.appname（stickynote，fnOS 包标识），
# 与 release 目录名（sticky-note-<ver>-linux-amd64，二进制名）不同——两者必须显式区分，
# 否则 fnpack build 输出 stickynote.fpk 后脚本找不到产物导致校验失败。
FPK_NAME="stickynote"
RELEASE_DIR="release/v${VERSION}"

[ ! -d "${RELEASE_DIR}/${APP_NAME}-${VERSION}-linux-amd64" ] && echo "❌ 请先运行 ./scripts/build-all.sh" && exit 1

echo "============================================"
echo "  飞牛 fnOS 打包 v${VERSION}"
echo "============================================"


echo ""
# 用户规则（2026-08-24）：仅保留 x86/amd64，不做 arm。如需 arm 可 FNPACK_ARCHS="amd64 arm64"
ARCHS="${FNPACK_ARCHS:-amd64}"
for a in ${ARCHS}; do
    case "$a" in
        amd64) build_arch="amd64"; build_platform="x86"; build_label="fnos-amd64" ;;
        arm64) build_arch="arm64"; build_platform="arm"; build_label="fnos-arm64" ;;
        *)     echo "  ⚠️ 忽略未知架构: $a"; continue ;;
    esac
    echo -n "  📦 ${build_label}.fpk... "
    ARCH="$build_arch" PLATFORM="$build_platform" LABEL="$build_label" \
    VERSION="$VERSION" APP_NAME="$APP_NAME" FPK_NAME="$FPK_NAME" \
    RELEASE_DIR="$RELEASE_DIR" SRC_ROOT="$PROJECT_DIR" \
    FNPACK="$(cygpath -w "${FNPACK}" 2>/dev/null || echo "${FNPACK}")" \
        bash scripts/pack-fpk.sh || { echo ""; echo "❌ fpk 组包/校验失败，中止发布" >&2; exit 1; }
done

echo ""
echo "✅ 完成!"
ls -lh "${RELEASE_DIR}"/*.fpk | awk '{print "  " $NF " (" $5 ")"}'
