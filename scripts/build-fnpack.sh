#!/bin/bash

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

cp fnpack/manifest fnpack/manifest.bak
# v21.57 P1 审计修复：sed -i '' 是 BSD/macOS 语法，Linux GNU sed 下会把 '' 当输入文件直接失败。
# 统一封装跨平台函数（GNU/Linux 与 BSD/macOS 均可运行）。
fnpack_sed() {
    if sed --version >/dev/null 2>&1; then
        # GNU sed（Linux / Git Bash）
        sed -i "$1" "$2"
    else
        # BSD sed（macOS）
        sed -i '' "$1" "$2"
    fi
}

# ⭐ v21.171 组包污染门禁（2026-10-12 清理 fnpack/app/sticky-note 时补）。
#
# 事故背景：206 轮为做Docker 同源校验，用裸 `go build` 在 **fnpack/app/** 下复刻了
# 一个 37.8MB 的二进制（标准链应为 26.6MB）。它**不属于 fnpack 模板**，却被下面
# `cp -r fnpack/*` 原样带进 app.tgz —— 最终 fpk 里出现**两个** sticky-note
# （server/ 下的正确产物 + 根目录的旧残留）。210 轮在生产机实测到该多余二进制
# （sha c6af2438… = 206 坏产物）。
#
# 为何此前无人发现：启动脚本只引用 `${TRIM_APPDEST}/server/sticky-note`，
# 残留**不被执行** ⇒ 功能不受影响只白占体积；verify-fpk 只校验固定路径，
# 不查"多出来的文件"。
#
# 修法：组包前**显式拦截** —— fnpack/app/ 下只应保留 ui/（桌面注册，模板自带）；
# server/ 与 www/ 由 build_fnpack() 显式创建。任何**顶层散落文件**均为历史污染残留。
POLLUTE=0
for f in fnpack/app/*; do
    [ -e "$f" ] || continue
    if [ ! -d "$f" ]; then
        echo "❌ 组包中止：fnpack/app/ 下存在顶层散落文件 $f" >&2
        echo "   该目录只应保留 ui/（桌面注册）；残留二进制会被 cp -r 原样打进 fpk" >&2
        POLLUTE=1
    fi
done
if [ "$POLLUTE" -ne 0 ]; then
    echo "   处理：确认无用后删除（备份放 .sync-tmp/），再重新组包" >&2
    exit 1
fi
echo "  ✓ fnpack/app 洁净（无历史污染残留）"

build_fnpack() {
    ARCH=$1
    PLATFORM=$2
    LABEL=$3
    echo -n "  📦 ${LABEL}.fpk... "
    
    DIR="${RELEASE_DIR}/fnpack-${ARCH}"
    rm -rf "${DIR}"
    mkdir -p "${DIR}"
    
    # 复制飞牛模板，排除 .DS_Store
    find fnpack -name ".DS_Store" -delete 2>/dev/null || true
    cp -r fnpack/* "${DIR}/"
    fnpack_sed "s/^platform.*/platform              = ${PLATFORM}/" "${DIR}/manifest"
    
    # v21.78 打包修复：app/ 下必须含 server/ www/ 目录（缺目录 cp 会失败）；
    # app/ui/ 桌面注册目录由 fnpack/app 模板带入，fnpack build 整体打包进 app.tgz
    mkdir -p "${DIR}/app/server" "${DIR}/app/www"

    cp "${RELEASE_DIR}/${APP_NAME}-${VERSION}-linux-${ARCH}/sticky-note" "${DIR}/app/server/"
    cp -r "${RELEASE_DIR}/${APP_NAME}-${VERSION}-linux-${ARCH}/www/"* "${DIR}/app/www/"

    # 复制使用说明文档到 app 目录
    for doc in "${PROJECT_DIR}/release/v${VERSION}/README-"*.md; do
        if [ -f "$doc" ]; then
            cp "$doc" "${DIR}/app/"
        fi
    done
    
    # 删除 .DS_Store
    find "${DIR}" -name ".DS_Store" -delete 2>/dev/null || true
    
    cd "${DIR}"
    # fnpack build 在当前目录执行，避免向 Windows exe 传 POSIX 路径（Git-Bash 路径转换坑）
    "${FNPACK}" build
    cd "$PROJECT_DIR"

    # v21.93 P2：verify-fpk.sh 强制绑定发布链——此前校验与否全靠人工记得，
    # 而本项目历史上出过「源码正确但 fpk 内容错误」（115 停用事故）的交付事故，
    # 因此产物校验必须在 mv 交付之前执行，失败即中止。
    if [ -f "${PROJECT_DIR}/scripts/verify-fpk.sh" ]; then
        bash "${PROJECT_DIR}/scripts/verify-fpk.sh" "${DIR}/${FPK_NAME}.fpk" || {
            echo "❌ fpk 校验失败，中止发布" >&2
            exit 1
        }
    else
        echo "⚠️  未找到 scripts/verify-fpk.sh，跳过产物校验" >&2
    fi

    mv "${DIR}/${FPK_NAME}.fpk" "${RELEASE_DIR}/${FPK_NAME}-v${VERSION}-${LABEL}.fpk"
    rm -rf "${DIR}"
    echo "✅"
}

echo ""
# 用户规则（2026-08-24）：仅保留 x86/amd64，不做 arm。如需 arm 可 FNPACK_ARCHS="amd64 arm64"
ARCHS="${FNPACK_ARCHS:-amd64}"
for a in ${ARCHS}; do
    case "$a" in
        amd64) build_fnpack "amd64" "x86" "fnos-amd64" ;;
        arm64) build_fnpack "arm64" "arm" "fnos-arm64" ;;
        *) echo "  ⚠️ 忽略未知架构: $a" ;;
    esac
done

mv fnpack/manifest.bak fnpack/manifest
# v21.107 修复：备份恢复会把 version 行回退到打包前旧值（1.0.147 轮被 CI version
# consistency 门禁抓回），恢复后必须重新对齐 VERSION 唯一事实源。
fnpack_sed "s/^version.*/version               = ${VERSION}/" fnpack/manifest

echo ""
echo "✅ 完成!"
ls -lh "${RELEASE_DIR}"/*.fpk | awk '{print "  " $NF " (" $5 ")"}'
