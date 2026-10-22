#!/bin/bash
# build-linux.sh — FPK 交付路径的 Linux 生产二进制构建（纪律 1.2/1.5）
# 产物：server/sticky-note-linux-amd64（build_fpk.py 组包取的就是它）
# 四要素：CGO_ENABLED=0 -trimpath -ldflags="-s -w" + 版本号注入（VERSION 唯一事实源）

set -e

PROJECT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"
cd "$PROJECT_DIR"

VERSION=$(cat VERSION)
[ -z "$VERSION" ] && echo "❌ 无法获取版本号" && exit 1

cd server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags="-s -w -X main.Version=${VERSION}" \
    -o sticky-note-linux-amd64 .

# ELF 头校验（纪律 1.5）
head -c 4 sticky-note-linux-amd64 | grep -q $'\x7fELF' || { echo "❌ ELF 头校验失败"; exit 1; }

echo "✅ server/sticky-note-linux-amd64  v${VERSION}（linux/amd64, -trimpath -s -w, 版本已注入）"
