# fetch-webview2-runtime.ps1 —— WebView2 Fixed Version runtime 获取（批次 7 发布轮辅助）
#
# 用途：受限网络/无 EverGreen 的机器需要离线运行时时，从 Microsoft 官方渠道下载
# Fixed Version CAB 并记录 sha256，回填《desktop/README.md》§四登记表。
# 不在 CI 默认链路（体积 ~180MB，节律独立）；发布轮人工执行。
#
# 用法：powershell -File fetch-webview2-runtime.ps1 -Version 1.0.2903.40
#   （版本号取自 https://developer.microsoft.com/en-us/microsoft-edge/webview2/#download-section）

param([Parameter(Mandatory = $true)][string]$Version,
    [string]$OutDir = "$PSScriptRoot\..\build\webview2")
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$url = "https://static.msedge.microsoft.com/edge/webview/webview2-runtime-fixed/$Version/MicrosoftEdgeWebView2RuntimeInstallerX64-$Version.exe"
$dst = Join-Path $OutDir "MicrosoftEdgeWebView2RuntimeInstallerX64-$Version.exe"
Write-Host "GET $url"
Invoke-WebRequest -Uri $url -OutFile $dst -UseBasicParsing
$sha = (Get-FileHash $dst -Algorithm SHA256).Hash.ToLower()
Write-Host "saved: $dst"
Write-Host "sha256: $sha"
Write-Host "→ 请把 (版本 / sha256 / 获取日期) 登记进 desktop/README.md §四"
