# check-no-listen.ps1 —— 红线 29 断言：桌面进程树零 TCP 监听（批次 7 发布 gate）。
# 启动桌面 exe（真 WebView2 壳），扫描主进程 + 直接子进程（msedgewebview2）的
# LISTEN 套接字；发现任何监听即失败退出。CI desktop job 与本地发布前核对共用。
param([Parameter(Mandatory = $true)][string]$Exe)
$ErrorActionPreference = 'Stop'
if (-not (Test-Path $Exe)) { Write-Error "exe 不存在: $Exe"; exit 2 }

$p = Start-Process $Exe -PassThru
Start-Sleep -Seconds 8
try {
    $tree = @($p.Id)
    $kids = @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($p.Id)" -ErrorAction SilentlyContinue |
        ForEach-Object { $_.ProcessId })
    $tree += $kids
    $listen = @(Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue |
        Where-Object { $tree -contains $_.OwningProcess })
    if ($listen.Count -gt 0) {
        $listen | Format-Table LocalAddress, LocalPort, OwningProcess
        Write-Error "红线 29 违例：桌面进程树存在 TCP 监听（禁止 localhost Gin/文件服务器）"
        exit 1
    }
    Write-Host "红线 29 OK：进程树（主 1 + 子 $($kids.Count)）零 TCP 监听"
}
finally {
    Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
    Get-CimInstance Win32_Process -Filter "ParentProcessId=$($p.Id)" -ErrorAction SilentlyContinue |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
}
