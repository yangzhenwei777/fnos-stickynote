#!/bin/bash

set -e

PROJECT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"
cd "$PROJECT_DIR"

VERSION=$(cat VERSION)
[ -z "$VERSION" ] && echo "❌ 无法获取版本号" && exit 1

APP_NAME="sticky-note"
RELEASE_DIR="release/v${VERSION}"

echo "============================================"
echo "  多平台打包 v${VERSION}"
echo "============================================"

mkdir -p "${RELEASE_DIR}"

echo ""
echo "📦 编译前端..."
# v21.52 审计修复（发布工程）：npm install → npm ci 严格按 package-lock.json 恢复依赖，
# 保证 release 构建可重复（install 可能重算依赖树）。
# ⭐ v2.2.2 R2 修复（2026-10-28 实锤）：原 `cd web && npm ci && npm run build && cd ..`
# 是 set -e 不覆盖的经典 AND-list 陷阱——链中段（npm ci/npm run build）失败时脚本
# **不终止**，静默用旧 dist 出全平台包 + fpk（本轮实际发生：vite dev server 锁
# node_modules 致 npm ci EPERM，旧包被原样重出，verify-fpk 因旧包自洽而全过）。
# 改为逐条显式失败即退，前端构建失败绝不允许继续打包。
cd web || exit 1
npm ci || exit 1
npm run build || exit 1
cd .. || exit 1

compile() {
    GOOS=$1
    GOARCH=$2
    LABEL=$3
    echo -n "  📦 ${LABEL}... "
    
    DIR="${RELEASE_DIR}/${APP_NAME}-${VERSION}-${LABEL}"
    rm -rf "${DIR}"
    mkdir -p "${DIR}/www"

    # 复制前端构建产物到 server/static/dist 供 Go embed 使用
    cd "${PROJECT_DIR}"
    rm -rf server/static/dist
    cp -r web/dist server/static/dist

    cd server
    # 纪律 1.2/1.5：-trimpath 去路径 + -s -w 瘦身 + 注入版本号（VERSION 文件为唯一事实源）
    CGO_ENABLED=0 GOOS=${GOOS} GOARCH=${GOARCH} go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" -o "../${DIR}/sticky-note" . 2>/dev/null
    cd ..
    
    if [ "${GOOS}" = "windows" ]; then
        mv "${DIR}/sticky-note" "${DIR}/sticky-note.exe"
        echo '@echo off
cd /d "%~dp0"
sticky-note.exe %*' > "${DIR}/start.bat"
    else
        echo '#!/bin/bash
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"
chmod +x sticky-note 2>/dev/null || true
./sticky-note "$@"' > "${DIR}/start.sh"
        chmod +x "${DIR}/start.sh"
    fi
    
    cp -r web/dist/* "${DIR}/www/"

    # 复制使用说明文档（如果存在）
    for doc in release/v${VERSION}/README-*.md; do
        if [ -f "$doc" ]; then
            cp "$doc" "${DIR}/"
        fi
    done

    find "${DIR}" -name ".DS_Store" -delete 2>/dev/null || true

    echo "✅"
}

echo ""
echo "🔨 编译平台..."
echo ""
compile "linux" "amd64" "linux-amd64"
compile "linux" "arm64" "linux-arm64"
compile "darwin" "amd64" "macos-amd64"
compile "darwin" "arm64" "macos-arm64"
compile "windows" "amd64" "windows-amd64"

echo ""
echo "📦 创建压缩包..."
cd "${RELEASE_DIR}"
for dir in ${APP_NAME}-${VERSION}-*/; do
    name=$(basename "$dir")
    tar -czf "${name}.tar.gz" --exclude='.DS_Store' "$dir"
done
cd "$PROJECT_DIR"

echo ""
echo "✅ 完成!"
ls -lh "${RELEASE_DIR}"/*.tar.gz | awk '{print "  " $NF " (" $5 ")"}'
