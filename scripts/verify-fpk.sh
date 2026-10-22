#!/bin/bash
# verify-fpk.sh — 交付前 fpk 结构完整性校验（对照 fnpack build 官方校验规则）
# 用法: ./scripts/verify-fpk.sh <xxx.fpk> [more.fpk ...]
# 任一校验失败退出码非 0，禁止交付。v21.78 起强制使用（115 停用事故教训：漏 ui/ 未被发现）。
set -u

FAIL=0
fail() { echo "  ❌ $1"; FAIL=1; }
ok()   { echo "  ✅ $1"; }

[ $# -ge 1 ] || { echo "用法: $0 <fpk 文件> [...]"; exit 2; }

for FPK in "$@"; do
  echo "===== 校验 $FPK ====="
  [ -f "$FPK" ] || { echo "  ❌ 文件不存在"; FAIL=1; continue; }
  TMP=$(mktemp -d)
  # Git Bash 下 mktemp 可能返回 Windows 路径（C:\...），tar 只认 POSIX 路径
  case "$TMP" in
    [A-Za-z]:*) TMP=$(cygpath -u "$TMP") ;;
  esac
  # v21.95 修复：fpk 文件名含非 ASCII（如中文「便签-…fpk」）时，Git-Bash 下 tar 因路径 locale
  # 解包失败（报「解包失败」），但 fpk 本身完好。先拷到 ASCII 临时名再解包，消除路径坑。
  FPK_SAFE="$TMP/$(echo "$(basename "$FPK")" | tr -c 'A-Za-z0-9._-' '_')"
  [ -z "$FPK_SAFE" ] && FPK_SAFE="$TMP/pkg.fpk"
  cp "$FPK" "$FPK_SAFE" 2>/dev/null || FPK_SAFE="$FPK"
  tar -xzf "$FPK_SAFE" -C "$TMP" 2>/dev/null || { echo "  ❌ 解包失败"; FAIL=1; rm -rf "$TMP"; continue; }

  # 1) 外层必备条目
  for e in manifest cmd config/privilege config/resource wizard ICON.PNG ICON_256.PNG app.tgz; do
    [ -e "$TMP/$e" ] && ok "外层存在 $e" || fail "外层缺失 $e"
  done

  # 2) manifest 必填字段
  MF="$TMP/manifest"
  for k in appname version display_name platform checksum desktop_uidir os_min_version; do
    grep -q "^${k}[[:space:]]*=" "$MF" 2>/dev/null && ok "manifest 字段 $k" || fail "manifest 缺失字段 $k"
  done
  UIDIR=$(sed -n 's/^desktop_uidir[[:space:]]*=[[:space:]]*\(.*\)/\1/p' "$MF" | tr -d '[:space:]')

  # 3) config JSON 合法性（python 为 Windows 程序，须传 Windows 路径）
  for j in config/privilege config/resource; do
    if [ -f "$TMP/$j" ]; then
      JPATH=$(cygpath -w "$TMP/$j" 2>/dev/null || echo "$TMP/$j")
      python -m json.tool "$JPATH" >/dev/null 2>&1 && ok "JSON 合法 $j" || fail "JSON 非法 $j"
    else
      fail "config 缺失 $j"
    fi
  done

  # 4) checksum == app.tgz MD5
  CS=$(sed -n 's/^checksum[[:space:]]*=[[:space:]]*\([0-9a-fA-F]\{32\}\).*/\1/p' "$MF" | head -1)
  REAL=$(md5sum "$TMP/app.tgz" | cut -d' ' -f1)
  if [ -n "$CS" ] && [ "$CS" = "$REAL" ]; then
    ok "checksum 匹配 ($CS)"
  else
    fail "checksum 不匹配 (manifest=$CS real=$REAL)"
  fi

  # 5) 【事故点】app.tgz 解压后 {desktop_uidir} 目录必须存在
  mkdir -p "$TMP/app_inner"
  tar -xzf "$TMP/app.tgz" -C "$TMP/app_inner" 2>/dev/null || { fail "app.tgz 解压失败"; rm -rf "$TMP"; continue; }
  if [ -n "$UIDIR" ]; then
    if [ -d "$TMP/app_inner/$UIDIR" ]; then
      ok "app/{desktop_uidir} 存在 → $UIDIR/"
      ls "$TMP/app_inner/$UIDIR"/* 2>/dev/null >/dev/null && ok "  $UIDIR/ 内含文件" || fail "  $UIDIR/ 为空目录"
    else
      fail "app/{desktop_uidir} 缺失 → $UIDIR/（manifest 声明 desktop_uidir 时目录必须存在，否则安装后应用停用）"
    fi
  else
    fail "desktop_uidir 解析为空"
  fi

  # 6) app.tgz 内 server/ www/ 存在；可执行位仅提示（Windows 打包归档权限位不可控，
  #    91 官方包归档亦非 755 却正常安装，fnOS 安装器自行处理权限）
  for e in server/sticky-note www/index.html; do
    [ -e "$TMP/app_inner/$e" ] && ok "app 内存在 $e" || fail "app 内缺失 $e"
  done
  PERM=$(tar -tvzf "$TMP/app.tgz" 2>/dev/null | awk '$NF ~ /server\/sticky-note$/{print $1; exit}')
  case "$PERM" in
    *x*) ok "server/sticky-note 可执行位 (tar=$PERM)" ;;
    *)   echo "  ⚠️ 提示: server/sticky-note 归档无 x 位 (tar=$PERM)，fnOS 安装器会处理，非阻断项" ;;
  esac

  # 7) 版本号一致性：manifest.version == VERSION 文件
  MV=$(sed -n 's/^version[[:space:]]*=[[:space:]]*\(.*\)/\1/p' "$MF" | tr -d '[:space:]')
  FV=$(cat "$(dirname "$(readlink -f "$0")")/../VERSION" 2>/dev/null | tr -d '[:space:]')
  if [ -n "$FV" ] && [ "$MV" = "$FV" ]; then
    ok "版本号一致 manifest=$MV == VERSION=$FV"
  else
    fail "版本号不一致 manifest=$MV VERSION文件=$FV"
  fi

  # 7b) ⭐ v21.166 新增：二进制内必须真的注入了版本号字面量。
  # 事故背景：206 轮交付的 fpk 内server/sticky-note 是「裸 go build」产物
  # （37.7MB，而标准 -ldflags="-s -w -X main.Version=..." 产物为 26.6MB），
  # 漏了整串 ldflags ⇒ main.Version 落到源码默认值 "v1.0.0"，
  # 用户装完在「关于」里看到 "Sticky note vv1.0.0"。
  # 第7 项只校验 manifest↔VERSION 两处文本，对二进制本身零覆盖，故漏过；
  # 此处直接对 app.tgz 内的二进制做字面量断言，让构建事故在组包阶段即被拦下。
  BIN="$TMP/app_inner/server/sticky-note"
  if [ -f "$BIN" ] && [ -n "$FV" ]; then
    if grep -qaF "$FV" "$BIN"; then
      ok "二进制内含版本号字面量 → $FV（ldflags 注入生效）"
    else
      fail "二进制内未找到版本号字面量 '$FV' —— 构建漏了 -ldflags=\"-s -w -X main.Version=\$VERSION\"，「关于」将显示错误版本号"
    fi
  fi

  rm -rf "$TMP"
  echo ""
done

if [ $FAIL -eq 0 ]; then
  echo "🎉 全部校验通过，可交付"
  exit 0
else
  echo "🛑 存在失败项，禁止交付"
  exit 1
fi
