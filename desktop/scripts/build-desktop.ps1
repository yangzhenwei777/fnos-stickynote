# build-desktop.ps1 —— Windows 桌面发布构建（批次 7 · P7 单一构建事实源）
#
# 本地与 CI desktop job 共用本脚本（禁双源）。职责：
#   1) VITE_DESKTOP 前端构建（web → dist-desktop）并镜像进 desktop/frontend/dist
#   2) wails.json productVersion 与 VERSION 对齐（exe 属性面板可见）
#   3) wails build -trimpath + -ldflags 版本注入（main.Version）
#   4) 产物：**portable zip（唯一在产形态）**；NSIS 安装器已于 2026-10-13 **退役**
#      （交付面 8 件套 = fpk + 桌面绿色版，见 开发纪律.md 文首「交付形态收窄」）
#   5) 发布 gate 断言：exe -version 回读 == VERSION；产物 sha256 落 SHA256SUMS.txt
#
# 用法：
#   powershell -NoProfile -ExecutionPolicy Bypass -File desktop\scripts\build-desktop.ps1
#   （历史兼容开关 [-RequireNSIS]：仅历史追溯时需要，默认不构建安装器）
# 环境要求：go 1.26.7、node 22+、wails CLI v2.16.0 钉版（go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0）

param([switch]$RequireNSIS)
$ErrorActionPreference = 'Stop'

$Desktop = Split-Path -Parent (Split-Path -Parent $PSCommandPath)
$Root    = Split-Path -Parent $Desktop
$Web     = Join-Path $Root 'web'
$Ver     = (Get-Content (Join-Path $Root 'VERSION') -Raw).Trim()
Write-Host "== build-desktop v$Ver =="

# 1) 前端（VITE_DESKTOP）
Push-Location $Web
npm run build:desktop | Out-Null
if ($LASTEXITCODE -ne 0) { throw "vite build:desktop failed" }
Pop-Location
$src = Join-Path $Web 'dist-desktop'
$dst = Join-Path $Desktop 'frontend\dist'
robocopy $src $dst /MIR /NFL /NDL /NJH /NJS | Out-Null
if ($LASTEXITCODE -ge 8) { throw "dist mirror failed rc=$LASTEXITCODE" }
# SW 关断断言（v20 §120）：桌面 index.html 不得含 Service Worker 注册注入
$idx = Get-Content (Join-Path $dst 'index.html') -Raw
if ($idx -match 'serviceWorker|registerSW') { throw "desktop dist 含 SW 注册（§120 违例）" }
Write-Host "  dist ok (SW-disabled verified)"

# 2.5) 图标门禁（v21.140 新增）：桌面图标必须是**自定义品牌图标**。
#      背景：工程长期挂着 Wails 模板自带的默认「W」logo（appicon.png / icon.ico 两者皆是），
#      构建链没有任何断言，于是「安装图标不是我自定义的图标」一路带到用户手上。
#      这里只做**结构断言**（不写死 md5，避免换图标即红）：
#        - appicon.png 为 1024x1024（Wails 各平台图标源头规范）
#        - icon.ico 存在、含 256 大帧、且各帧均为 PNG 压缩帧
#      注意：**不能用 System.Drawing 读 ico 判断尺寸**——它解析不了 PNG 压缩的 256 帧，
#      会静默回退到最大可解析的 BMP 帧（实测返回 128），据此断言会误判。
#      改为直接解析 ICO 目录头（ICONDIR + ICONDIRENTRY），零图像编解码依赖。
if (-not (Test-Path (Join-Path $Desktop 'build\appicon.png'))) { throw "缺 desktop\build\appicon.png（Wails 图标源头）" }
$icoPath = Join-Path $Desktop 'build\windows\icon.ico'
if (-not (Test-Path $icoPath)) { throw "缺 desktop\build\windows\icon.ico（exe 与 NSIS 图标来源）" }
Add-Type -AssemblyName System.Drawing
$appIconPath = Join-Path $Desktop 'build\appicon.png'
$ap = [System.Drawing.Image]::FromFile($appIconPath)
try {
    if ($ap.Width -ne 1024 -or $ap.Height -ne 1024) {
        throw "appicon.png 应为 1024x1024，实际 $($ap.Width)x$($ap.Height)"
    }
} finally { $ap.Dispose() }
$icoBytes = [System.IO.File]::ReadAllBytes($icoPath)
if ($icoBytes.Length -lt 22) { throw "icon.ico 过小（$($icoBytes.Length) B），不是有效 ICO" }
$reserved = [System.BitConverter]::ToUInt16($icoBytes, 0)
$type     = [System.BitConverter]::ToUInt16($icoBytes, 2)
$count    = [System.BitConverter]::ToUInt16($icoBytes, 4)
if ($reserved -ne 0 -or $type -ne 1) { throw "icon.ico 头非法（reserved=$reserved type=$type）" }
if ($count -lt 5) { throw "icon.ico 帧数过少（$count）——小图标会糊" }
$has256 = $false
$nonPng = 0
for ($i = 0; $i -lt $count; $i++) {
    $e = 6 + $i * 16
    $w = [int]$icoBytes[$e]; if ($w -eq 0) { $w = 256 }   # 目录项宽高为 0 表示 256
    if ($w -ge 256) { $has256 = $true }
    $sz = [System.BitConverter]::ToUInt32($icoBytes, $e + 8)
    $off = [System.BitConverter]::ToUInt32($icoBytes, $e + 12)
    if ($off + 8 -gt $icoBytes.Length) { throw "icon.ico 第 $i 帧偏移越界" }
    $isPng = ($icoBytes[$off] -eq 0x89 -and $icoBytes[$off+1] -eq 0x50 -and $icoBytes[$off+2] -eq 0x4E -and $icoBytes[$off+3] -eq 0x47)
    if (-not $isPng) { $nonPng++ }
}
if (-not $has256) { throw "icon.ico 无 256 帧——任务栏/资源管理器大图标视图会糊" }
if ($nonPng -gt 0) { throw "icon.ico 有 $nonPng 帧不是 PNG 压缩帧（Vista+ 建议全 PNG，BMP 256 帧体积大且部分系统渲染异常）" }
Write-Host "  icons ok (appicon 1024x1024, icon.ico $count frames all PNG, has 256)"

# 2) wails.json productVersion 对齐（构建期改写；显式 UTF8 读写——PS5.1 默认 ANSI 会
#    读坏中文 JSON；写回必须无 BOM，wails 的 Go json 解析器不接受 BOM）
$wj = Join-Path $Desktop 'wails.json'
$cfg = [System.IO.File]::ReadAllText($wj, [System.Text.Encoding]::UTF8) | ConvertFrom-Json
$cfg.info.productVersion = $Ver
$json = $cfg | ConvertTo-Json -Depth 8
[System.IO.File]::WriteAllText($wj, $json, (New-Object System.Text.UTF8Encoding($false)))

# 3) WebView2 Fixed Version runtime 就位 —— **必须先于 wails build -nsis**（2026-09-24 生产实锤修复）：
#    project.nsi 在**编译期**以 `!if /FILEEXISTS ${ARG_WAILS_AMD64_BINARY}\..\webview2-runtime\msedgewebview2.exe`
#    条件打包 runtime；镜像若晚于 wails build 完成，条件判假 → runtime 静默缺席 → 安装版回退系统
#    Evergreen（Evergreen 自动升级到 153.0.4234.48 后，安装版全部白屏/崩溃循环；189/190/191 安装器
#    均 ~9.4MB 即此缺陷的物证）。zip 由下游 Compress 显式带上同一目录，不受顺序影响。
#    获取方法：从 GitHub westinyang/WebView2RuntimeArchive release 133.0.3065.92 下载
#    x64 cab → `expand <cab> -F:* <tmp>` → 把解出的目录内容镜像到 build\webview2-runtime\。
# v21.196 O10（资产外置）：runtime 源镜像共 424MB，不该常驻仓库（`desktop/build` 单轮即 3.75GB）。
#    现支持外置缓存：优先 $env:STICKYNOTE_WV2_SRC → %LOCALAPPDATA%\StickyNoteBuildCache\webview2-runtime-<版本>
#    → 回落仓内 build\webview2-runtime（保持既有流程可用）。
$wv2Default = Join-Path $env:LOCALAPPDATA ("StickyNoteBuildCache\webview2-runtime-" + $Ver.Replace('.', '.'))
$wv2Cached = if ($env:STICKYNOTE_WV2_SRC) { $env:STICKYNOTE_WV2_SRC }
             elseif (Test-Path (Join-Path $wv2Default 'msedgewebview2.exe')) { $wv2Default }
             else { Join-Path $Desktop 'build\webview2-runtime' }
$wv2Src = $wv2Cached
$wv2Ready = Test-Path (Join-Path $wv2Src 'msedgewebview2.exe')
if ($wv2Ready -and $wv2Src -ne (Join-Path $Desktop 'build\webview2-runtime')) {
    Write-Host "  webview2-runtime source = external cache: $wv2Src"
}
$bin = Join-Path $Desktop 'build\bin'
$xzPath = Join-Path $bin 'webview2-runtime.tar.xz'
if ($wv2Ready) {
    # v21.194 方案 A（包体积瘦身，用户拍板）：不再整目录镜像——
    # ① 裁剪：Locales 保留 zh-CN/en-US（84 种语言占 119MB，笔记场景只需中文/英文）、
    #    去 WidevineCdm（DRM，笔记场景零使用）→ 558MB → 424MB；
    # ② 高压缩：tar.xz（xz -J）→ 约 150MB；分发包整体 271MB → 约 160MB（实测 159MB）。
    # ③ 首启解压：main.go ensureRuntimeExtracted() 经 System32 bsdtar（原生 xz）解到 exe 旁，
    #    实测 7s/424MB；解压失败降级系统 Evergreen（既有 fixedWebview2Dir 行为）。
    Write-Host "  webview2-runtime pinned (133.0.3065.92) -> trim (Locales zh-CN/en-US, no WidevineCdm) + tar.xz"
    New-Item -ItemType Directory -Force -Path $bin | Out-Null
    $trim = Join-Path $env:TEMP ("wv2-trim-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force -Path (Join-Path $trim 'webview2-runtime') | Out-Null
    robocopy $wv2Src (Join-Path $trim 'webview2-runtime') /MIR /NFL /NDL /NJH /NJS | Out-Null
    if ($LASTEXITCODE -ge 8) { throw "webview2-runtime copy failed rc=$LASTEXITCODE" }
    $loc = Join-Path $trim 'webview2-runtime\Locales'
    Get-ChildItem $loc -Exclude 'zh-CN.pak', 'en-US.pak' -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force
    Remove-Item -Recurse -Force (Join-Path $trim 'webview2-runtime\WidevineCdm') -ErrorAction SilentlyContinue
    if (Test-Path $xzPath) { Remove-Item -Force $xzPath }
    & "$env:SystemRoot\System32\tar.exe" -cJf $xzPath -C $trim webview2-runtime
    if ($LASTEXITCODE -ne 0) { throw "runtime tar.xz pack failed rc=$LASTEXITCODE" }
    # v21.196 修订：NSIS 模板在**编译期**以 `!if /FILEEXISTS ..\webview2-runtime\msedgewebview2.exe`
    # 决定是否打包 runtime —— 它只认 build\bin 旁的目录。故把刚打好的 tar.xz 解回 $bin，
    # 既保证安装器载荷与 zip 完全一致（同一份裁剪产物），也保住「镜像先于 wails build」的不变式。
    # （O10 外置的是**未裁剪源镜像** 558MB；此处 424MB 裁剪产物是 NSIS 的硬性输入，仍需落在 build\bin。）
    $binRt = Join-Path $bin 'webview2-runtime'
    Remove-Item -Recurse -Force $binRt -ErrorAction SilentlyContinue
    & "$env:SystemRoot\System32\tar.exe" -xf $xzPath -C $bin
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path (Join-Path $binRt 'msedgewebview2.exe'))) {
        throw "runtime 解回 build\bin 失败（NSIS 将静默漏包）rc=$LASTEXITCODE"
    }
    Remove-Item -Recurse -Force $trim -ErrorAction SilentlyContinue
    Write-Host ("  webview2-runtime.tar.xz = {0} MB (trimmed); mirrored back to build\bin for NSIS" -f [math]::Round((Get-Item $xzPath).Length / 1MB, 1))
} else {
    # R-3 加固（v21.153）：要出安装器却无钉版 runtime = 直接拒绝（189~191 安装器漏带
    # runtime → Evergreen 153 下安装版全部白屏的生产 P0 教训）。非发布构建仍允许（仅警告）。
    if ($RequireNSIS) {
        throw "-RequireNSIS：webview2-runtime 缺失（build\webview2-runtime\msedgewebview2.exe 不在位）——安装器必含钉版 runtime；请先镜像 runtime 再重跑（获取方式见上方注释）"
    }
    Write-Host "  ⚠ webview2-runtime 缺失：产物将依赖系统 Evergreen（有 kind4/6 崩溃风险）——建议补齐后重打"
}

# 4) wails 构建（-s 跳过内置前端命令=用现成 dist；NSIS 按开关）
# v21.196 A1：wails 的 bindings 生成阶段会**短暂运行真实应用**（日志实证：摸到
#    %LOCALAPPDATA%\StickyNoteDev\profiles\localdev 并打印 localstore active generation）。
#    那会把构建产物写进开发者真实档案、甚至触发一次 AUTH_REVOKED（挂账 X-新）。
#    处置：构建期把档案重定向到一次性沙箱，构建后清理（不改变任何交付行为）。
$buildSandbox = Join-Path $env:TEMP ("sn-build-sbx-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $buildSandbox | Out-Null
$env:STICKYNOTE_PROFILE_DIR = $buildSandbox
# v21.196：bindings 生成阶段会短暂运行应用，此时**禁止任何模态弹窗**（首启解压提示 /
# runtime 降级告警）——否则构建被MessageBox 锁死（实测挂 19 分钟）。
$env:STICKYNOTE_NO_DIALOG = '1'
Push-Location $Desktop
$nsis = @()
# 2026-10-13：NSIS 安装器形态**退役**。此前判据是「makensis 在 PATH 就构建」（CI 每轮都产出
#   installer）；现改为**只有显式 -RequireNSIS 才构建**，默认一律不构建。
$makensis = Get-Command makensis -ErrorAction SilentlyContinue
if ($RequireNSIS) {
    if (-not $makensis) { throw "-RequireNSIS 但 makensis 不在 PATH（历史开关；本地: 见 desktop/README.md）" }
    $nsis = @('-nsis')
    Write-Host "  [warn] -RequireNSIS：正在构建**已退役**的 NSIS 安装器（仅历史追溯用）"
}
wails build -s -trimpath @nsis "-ldflags=-s -w -X main.Version=$Ver"
if ($LASTEXITCODE -ne 0) { throw "wails build failed" }
Pop-Location
Remove-Item Env:\STICKYNOTE_PROFILE_DIR -ErrorAction SilentlyContinue
Remove-Item Env:\STICKYNOTE_NO_DIALOG -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force $buildSandbox -ErrorAction SilentlyContinue

# 5) 双形态产物 + 6) gate 断言
$exe = Join-Path $bin 'sticky-note-desktop.exe'
# 注意：wails 发布构建为 GUI 子系统 exe（-H windowsgui），& 直调 stdout 不可见——
# 必须经 Start-Process 重定向管道回读（本行语义即版本注入断言的正确姿势）。
$vout = Join-Path $env:TEMP ("snver-" + [guid]::NewGuid().ToString('N') + '.txt')
Start-Process -FilePath $exe -ArgumentList '-version' -Wait -NoNewWindow -RedirectStandardOutput $vout | Out-Null
$got = (Get-Content $vout -Raw -ErrorAction SilentlyContinue).Trim()
Remove-Item $vout -Force -ErrorAction SilentlyContinue
if ("$got" -ne $Ver) { throw "版本注入断言失败: exe=[$got] want=[$Ver]" }
Write-Host "  exe -version == $Ver OK"

$outDir = Join-Path $Desktop "build\release"
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$base = "sticky-note-desktop-$Ver-windows-amd64"
$zip = Join-Path $outDir "$base.zip"
if (Test-Path $zip) { Remove-Item $zip -Force }
$zipItems = @($exe)
if ($wv2Ready) { $zipItems += $xzPath }
# S0（2026-09-23 用户需求）：便携（绿色）版标记 —— exe 同目录存在 portable.mode 时，
# main.go portableRoot() 把全部数据（Go 档案 + WebView2 存储）指向 <exeDir>\sticky-note-data\，
# 删除该目录即无任何残留（防数据泄露）。NSIS 安装版不带此标记，行为不变。
$portableFlag = Join-Path $outDir 'portable.mode'
Set-Content -Path $portableFlag -Value "portable build: all data is kept in .\sticky-note-data\ — delete this folder and the exe to remove every local trace." -Encoding ASCII
$zipItems += $portableFlag
Compress-Archive -Path $zipItems -DestinationPath $zip
$sums = Join-Path $outDir "$base-SHA256SUMS.txt"
$lines = @((Get-FileHash $zip -Algorithm SHA256 | ForEach-Object { "{0}  {1}" -f $_.Hash.ToLower(), (Split-Path $zip -Leaf) }))
# 安装器已退役（2026-10-13）：默认不产出、不入 SHA256SUMS；仅 -RequireNSIS 历史追溯时才处理。
# wails NSIS 产物名跟随 wails.json 的 name 字段（stickynote-desktop-amd64-installer.exe），
# 用 glob 匹配防命名漂移；-RequireNSIS 时缺失即 gate 失败。
$installerSrc = @(Get-ChildItem (Join-Path $bin '*-installer.exe') -ErrorAction SilentlyContinue)
if ($RequireNSIS -and $installerSrc.Count -eq 0) { throw "-RequireNSIS 已指定但 build/bin 无 *-installer.exe 产物" }
if ($RequireNSIS -and $installerSrc.Count -gt 0) {
    $inst = Join-Path $outDir "$base-installer.exe"
    Move-Item $installerSrc[0].FullName $inst -Force
    # R-3 加固（v21.153）：安装器必含钉版 runtime——**体积即断言**（含 runtime ≈160MB（xz 后），
    # 189~191 漏带时仅 ~9.4MB；阈值 100MB 双向安全）。makensis 编译期 `!if /FILEEXISTS`
    # 判假会静默漏包，任何顺序回归都必须在这里被拦下。
    if ($wv2Ready) {
        $instSize = (Get-Item $inst).Length
        if ($instSize -lt 100MB) {
            throw "安装器体积 $([math]::Round($instSize/1MB,1))MB < 100MB——钉版 runtime 未入载荷（漏带缺陷复发），拒绝出包"
        }
        Write-Host ("  installer size check OK: {0} MB (runtime bundled)" -f [math]::Round($instSize/1MB,1))
    }
    $lines += (Get-FileHash $inst -Algorithm SHA256 | ForEach-Object { "{0}  {1}" -f $_.Hash.ToLower(), (Split-Path $inst -Leaf) })
}
Set-Content -Path $sums -Value $lines -Encoding ASCII
Get-Content $sums | ForEach-Object { Write-Host "  $_" }
Write-Host "== build-desktop done: $outDir =="
