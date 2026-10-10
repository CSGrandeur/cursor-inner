#requires -version 5
<#
 .SYNOPSIS
  cursor-inner 的 sing-box TUN 管理脚本：只把官方域名（grok/x.ai/Cursor 等）经 TUN 转到用户的代理，
  其它流量不碰。需要管理员权限（TUN 要装 wintun 并改路由）。由 cursor-inner 在用户确认后经 UAC 调用，
  也可由用户手动右键"以管理员身份运行"。任何失败都不改坏网络：sing-box 退出时自动撤销路由。

 .NOTES
  启动失败或验证不过时，脚本会杀掉 sing-box、打印日志末尾若干行，并以非零码退出；cursor-inner 据此回退到普通代理接管。
#>
[CmdletBinding()]
param(
  [ValidateSet('start','stop','status')]
  [string]$Action = 'start',
  [string]$ConfigPath,
  [string]$BinaryPath,
  [string]$LogPath,
  [string]$PidPath,
  [string]$Url      = 'https://github.com/SagerNet/sing-box/releases/download/v1.14.3/sing-box-1.14.3-windows-amd64.zip',
  [string]$ZipSha256 = 'd8b86ef58936dba9230ec6a57ffa4243933f3c14ef4809828b6c553440ed1518',
  [int]$VerifySeconds = 6
)

$ErrorActionPreference = 'Stop'
function Info($m){ Write-Host "[tun] $m" }
function Tail($path, $n){
  if ($path -and (Test-Path $path)) {
    $lines = Get-Content $path -Tail $n -ErrorAction SilentlyContinue
    if ($lines) { Write-Host ("---- {0} (末 {1} 行) ----" -f $path, $n); $lines | ForEach-Object { Write-Host $_ } }
  }
}
function Die($m){
  Write-Host "[tun][FAIL] $m" -ForegroundColor Red
  Tail $script:RunErr 20
  Tail $script:RunOut 20
  Tail $LogPath 20
  exit 1
}

# 必须管理员。非管理员时自我提权重跑一遍（供用户手动双击）。
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) {
  Info '需要管理员权限，正在请求提权……'
  $argl = @('-NoProfile','-ExecutionPolicy','Bypass','-File',$PSCommandPath,'-Action',$Action)
  if ($ConfigPath){ $argl += @('-ConfigPath',$ConfigPath) }
  if ($BinaryPath){ $argl += @('-BinaryPath',$BinaryPath) }
  if ($LogPath){ $argl += @('-LogPath',$LogPath) }
  if ($PidPath){ $argl += @('-PidPath',$PidPath) }
  try { $p = Start-Process powershell -Verb RunAs -PassThru -Wait -ArgumentList $argl } catch { Write-Host "[tun][FAIL] 提权被取消：$($_.Exception.Message)" -ForegroundColor Red; exit 1 }
  exit $p.ExitCode
}

if (-not $BinaryPath){ $BinaryPath = Join-Path $PSScriptRoot 'sing-box.exe' }
if (-not $ConfigPath){ $ConfigPath = Join-Path $PSScriptRoot 'singbox-tun.json' }
if (-not $PidPath){ $PidPath = Join-Path $PSScriptRoot 'singbox-tun.pid' }
# sing-box 控制台输出单独落盘，不与配置里的 log.output 抢同一个文件（否则会互相截断成空）。
$script:RunOut = Join-Path (Split-Path $ConfigPath) 'singbox-run.out.log'
$script:RunErr = Join-Path (Split-Path $ConfigPath) 'singbox-run.err.log'

function Stop-Tun {
  if (Test-Path $PidPath) {
    $old = Get-Content $PidPath -ErrorAction SilentlyContinue
    if ($old) { Get-Process -Id $old -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $BinaryPath } | Stop-Process -Force -ErrorAction SilentlyContinue }
    Remove-Item $PidPath -ErrorAction SilentlyContinue
  }
  # 兜底：按路径杀掉本数据目录下的 sing-box（不碰 v2rayN 等其它代理）。
  Get-Process sing-box -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $BinaryPath } | Stop-Process -Force -ErrorAction SilentlyContinue
}

switch ($Action) {
  'stop' { Stop-Tun; Info '已停止，路由随 sing-box 退出自动撤销'; exit 0 }
  'status' {
    $running = $false
    if (Test-Path $PidPath) { $pid0 = Get-Content $PidPath -ErrorAction SilentlyContinue; if ($pid0 -and (Get-Process -Id $pid0 -ErrorAction SilentlyContinue)) { $running = $true } }
    Info ("running=$running")
    exit 0
  }
}

# ---- start ----
# 1) 确保 sing-box.exe 就位（优先已存在；否则下载并校验 zip 后解压）。
if (-not (Test-Path $BinaryPath)) {
  Info "下载 sing-box：$Url"
  $tmp = Join-Path $env:TEMP ("sb-" + [guid]::NewGuid().ToString('N') + '.zip')
  try {
    Invoke-WebRequest -Uri $Url -OutFile $tmp -UseBasicParsing
    if ($ZipSha256) {
      $got = (Get-FileHash -Algorithm SHA256 -Path $tmp).Hash.ToLower()
      if ($got -ne $ZipSha256.ToLower()) { Die "sing-box 下载校验失败：$got" }
    }
    $ext = Join-Path $env:TEMP ("sb-" + [guid]::NewGuid().ToString('N'))
    Expand-Archive -Path $tmp -DestinationPath $ext -Force
    $exe = Get-ChildItem -Path $ext -Recurse -Filter 'sing-box.exe' | Select-Object -First 1
    if (-not $exe) { Die '压缩包里找不到 sing-box.exe' }
    New-Item -ItemType Directory -Force -Path (Split-Path $BinaryPath) | Out-Null
    Copy-Item $exe.FullName $BinaryPath -Force
  } finally {
    Remove-Item $tmp -ErrorAction SilentlyContinue
    if ($ext) { Remove-Item $ext -Recurse -Force -ErrorAction SilentlyContinue }
  }
}

if (-not (Test-Path $ConfigPath)) { Die "找不到配置：$ConfigPath" }

# 2) 配置自检（schema）。注意：check 不校验运行期规则，启动仍可能失败，故下面还要验证。
Info '校验配置……'
& $BinaryPath check -c $ConfigPath
if ($LASTEXITCODE -ne 0) { Die 'sing-box 配置校验未通过' }

# 3) 若已在跑，先停。
Stop-Tun

# 4) 启动（后台），stdout/stderr 分别落盘，便于失败时回看。
Info '启动 sing-box TUN……'
Remove-Item $script:RunOut, $script:RunErr -ErrorAction SilentlyContinue
$sbargs = @('run','-c',$ConfigPath)
$proc = Start-Process -FilePath $BinaryPath -ArgumentList $sbargs -PassThru -WindowStyle Hidden -RedirectStandardOutput $script:RunOut -RedirectStandardError $script:RunErr
Set-Content -Path $PidPath -Value $proc.Id

# 5) 验证：进程存活 + TUN 接口出现。否则清理并失败回退（Die 会打印日志末尾）。
Start-Sleep -Seconds $VerifySeconds
if ($proc.HasExited) { Remove-Item $PidPath -ErrorAction SilentlyContinue; Die ("sing-box 启动后立即退出（exit={0}）" -f $proc.ExitCode) }
$tun = Get-NetAdapter -ErrorAction SilentlyContinue | Where-Object { $_.InterfaceDescription -match 'WireGuard|wintun|sing-box|TUN' -or $_.Name -match 'sing-box|tun' }
if (-not $tun) {
  Info '未检测到 TUN 网卡，回退并清理'
  Stop-Tun
  Die 'TUN 网卡未建立'
}
Info ("TUN 已就绪：{0}；pid={1}" -f ($tun.Name -join ','), $proc.Id)
exit 0
