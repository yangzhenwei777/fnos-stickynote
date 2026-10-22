# desktop/ · Windows 桌面客户端

Windows 离线客户端，基于 Wails v2 + Vue 3 + 本地 SQLite。桌面端通过 Wails Binding 调用本地服务，不启动 localhost HTTP 服务。

## 前置要求

- Go 1.26.10+
- Node.js 22+
- Wails CLI v2.16.0
- Windows 10/11

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
```

## 开发构建

```powershell
# 1. 构建桌面版前端
cd web
npm ci
npm run build:desktop

# 2. 复制到 Wails 嵌入目录
robocopy dist-desktop ..\desktop\frontend\dist /MIR

# 3. 构建桌面程序
cd ..\desktop
go mod download
wails build -s -trimpath
```

产物位于 `desktop/build/bin/sticky-note-desktop.exe`。

## 发布构建

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File desktop/scripts/build-desktop.ps1
```

发布产物位于 `desktop/build/release/`，包含绿色版 zip 与 SHA256SUMS。

## WebView2 运行时

绿色版需要固定版本的 WebView2 运行时。CI 会自动下载并缓存；本地可通过 `desktop/scripts/fetch-webview2-runtime.ps1` 获取，默认放在 `desktop/build/webview2-runtime/`，该目录不入库。

## 数据目录

桌面端本地数据默认位于：

```text
%LOCALAPPDATA%\StickyNote\
```

绿色版模式可将数据保存在程序同目录，删除程序即可清理。
