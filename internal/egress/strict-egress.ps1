<#
.SYNOPSIS
  可选的严格模式：Windows 防火墙禁止 Grok Bot / Cursor 直连官方端点（grok、x.ai、Cursor）。

.DESCRIPTION
  只针对官方端点：GitHub、Google 等第三方直连不受影响。做法：
    - 运行时经代理用 DoH 解析官方域名清单（并上本地 DNS 结果），得到官方 IP；
    - 为 Grok Bot.exe 和 Cursor.exe 各建一条出站阻止规则，远端为这些 IP；
    - 计划任务（SYSTEM）开机时和每隔 -RefreshMinutes 分钟重新解析并更新规则。解析失败时保留旧规则。
  走代理的流量连的是 127.0.0.1 上的代理，不受这些规则影响。

  局限：
    - 按 IP 拦截。官方域名多在 Cloudflare 等共享 CDN 上，同一 IP 上的其他网站也会被这两个程序直连拦下（走代理仍正常）。
    - 只覆盖清单里的域名；动态主机（usNN.cursorvm.com）枚举了 us/eu/ap 1..30，清单外的新主机要等加进清单。
      两次刷新之间官方换了 IP 时，新 IP 会漏，直到下次刷新。
    - 规则按程序路径生效；应用换了安装位置要重新 -Enable。
    - 不改 hosts：代理软件常用系统解析做分流，hosts 指向 0.0.0.0 会把走代理的连接一起弄坏。

  需要管理员权限（-Status 除外）。

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\strict-egress.ps1 -Enable
  powershell -ExecutionPolicy Bypass -File scripts\strict-egress.ps1 -Status
  powershell -ExecutionPolicy Bypass -File scripts\strict-egress.ps1 -Disable
#>
param(
  [switch]$Enable,
  [switch]$Disable,
  [switch]$Refresh,
  [switch]$Status,
  [string]$Proxy = '',
  [string[]]$Program = @(),
  [int]$RefreshMinutes = 30
)
$ErrorActionPreference = 'Stop'
$group = 'cursor-inner strict egress (official endpoints)'
$taskName = 'cursor-inner strict egress refresh'
$home2 = Join-Path $env:ProgramData 'cursor-inner'
$stateFile = Join-Path $home2 'strict-egress.json'
$installed = Join-Path $home2 'strict-egress.ps1'
# ---- 官方端点清单（两份脚本里保持一致）----
$OfficialSuffixes = @('grok.com', 'x.ai', 'spacexai.com', 'cursor.com', 'cursor.sh', 'cursorapi.com', 'cursor-cdn.com', 'cursorvm.com', 'anysphere.co')
$OfficialHosts = @(
  'grok.com', 'www.grok.com', 'accounts.grok.com', 'assets.grok.com', 'api.grok.com',
  'x.ai', 'www.x.ai', 'api.x.ai', 'accounts.x.ai', 'auth.x.ai', 'console.x.ai', 'docs.x.ai', 'grok.x.ai', 'cdn.x.ai',
  'spacexai.com', 'www.spacexai.com',
  'cursor.com', 'www.cursor.com', 'api.cursor.com', 'downloads.cursor.com', 'docs.cursor.com', 'forum.cursor.com', 'authenticator.cursor.sh',
  'cursor.sh', 'api1.cursor.sh', 'api2.cursor.sh', 'api3.cursor.sh', 'api4.cursor.sh', 'api5.cursor.sh', 'repo42.cursor.sh', 'metrics.cursor.sh', 'prod.authentication.cursor.sh',
  'marketplace.cursorapi.com', 'cursor-cdn.com', 'anysphere.co'
)
foreach ($region in 'us', 'eu', 'ap') { foreach ($n in 1..30) { $OfficialHosts += "$region$n.cursorvm.com" } }

function Test-OfficialName([string]$name) {
  if (-not $name) { return $false }
  $name = $name.TrimEnd('.').ToLower()
  foreach ($s in $OfficialSuffixes) { if ($name -eq $s -or $name.EndsWith('.' + $s)) { return $true } }
  return $false
}

function Find-ProxyArg {
  $p = Get-CimInstance Win32_Process -Filter "Name='Grok Bot.exe'" -ErrorAction SilentlyContinue | Where-Object { $_.CommandLine -match '--proxy-server=(\S+)' } | Select-Object -First 1
  if ($p -and $p.CommandLine -match '--proxy-server=(\S+)') { return $Matches[1].Trim('"') }
  return ''
}

# Resolve-Official 返回 ip -> 域名。先经代理用 DoH（本地 DNS 可能被污染），再并上本地 DNS 的结果
# （直连进程拿到的就是本地结果）。DoH 全部失败时 $script:DohFailed 为真。
function Resolve-Official([string]$proxy) {
  $map = @{}
  $script:DohFailed = $true
  $endpoints = @('https://cloudflare-dns.com/dns-query?name={0}&type={1}', 'https://dns.google/resolve?name={0}&type={1}')
  foreach ($h in $OfficialHosts) {
    foreach ($type in 'A', 'AAAA') {
      foreach ($ep in $endpoints) {
        try {
          $req = @{ Uri = ($ep -f $h, $type); Headers = @{ accept = 'application/dns-json' }; TimeoutSec = 10; UseBasicParsing = $true }
          if ($proxy) { $req.Proxy = $proxy }
          $r = Invoke-RestMethod @req
          $script:DohFailed = $false
          foreach ($a in @($r.Answer)) { if ($a.type -eq 1 -or $a.type -eq 28) { $map[[string]$a.data] = $h } }
          break
        } catch { }
      }
    }
    try {
      foreach ($a in @(Resolve-DnsName $h -DnsOnly -ErrorAction Stop)) {
        if ($a.Type -eq 'A' -or $a.Type -eq 'AAAA') { if (-not $map.ContainsKey($a.IPAddress)) { $map[$a.IPAddress] = "$h (local DNS)" } }
      }
    } catch { }
  }
  return $map
}
# ---- 清单结束 ----

function Assert-Admin {
  $id = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
  if (-not $id.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw '需要管理员权限：请用「以管理员身份运行」打开 PowerShell。' }
}

function Find-Programs {
  $found = @()
  foreach ($name in 'Grok Bot', 'Cursor') {
    $p = Get-Process -Name $name -ErrorAction SilentlyContinue | Where-Object { $_.Path } | Select-Object -First 1
    if ($p) { $found += $p.Path; continue }
    foreach ($guess in (Join-Path $env:LOCALAPPDATA "Programs\$name\$name.exe"), (Join-Path $env:LOCALAPPDATA "Programs\cursor\Cursor.exe")) {
      if ((Split-Path $guess -Leaf) -eq "$name.exe" -and (Test-Path $guess)) { $found += $guess; break }
    }
  }
  return $found
}

function Apply-Rules([string[]]$programs, [string]$proxy) {
  $map = Resolve-Official $proxy
  if ($script:DohFailed) { throw 'DoH 解析失败（代理不通？），保留现有规则不变。' }
  $ips = @($map.Keys | Sort-Object -Unique)
  if ($ips.Count -eq 0) { throw '没有解析到官方 IP，保留现有规则不变。' }
  Get-NetFirewallRule -Group $group -ErrorAction SilentlyContinue | Remove-NetFirewallRule
  foreach ($p in $programs) {
    New-NetFirewallRule -DisplayName ("Block direct official endpoints: {0}" -f (Split-Path $p -Leaf)) -Group $group `
      -Direction Outbound -Action Block -Program $p -RemoteAddress $ips -Profile Any | Out-Null
  }
  Write-Output ("已更新：{0} 个程序 × {1} 个官方 IP（{2}）" -f $programs.Count, $ips.Count, (Get-Date -Format s))
}

if ($Status) {
  $rules = @(Get-NetFirewallRule -Group $group -ErrorAction SilentlyContinue)
  if ($rules.Count -eq 0) { Write-Output '严格模式：未开启'; exit 0 }
  foreach ($r in $rules) {
    $n = @(($r | Get-NetFirewallAddressFilter).RemoteAddress).Count
    $app = ($r | Get-NetFirewallApplicationFilter).Program
    Write-Output ("严格模式：开启  {0}  {1} 个 IP" -f $app, $n)
  }
  exit 0
}

if ($Disable) {
  Assert-Admin
  Get-NetFirewallRule -Group $group -ErrorAction SilentlyContinue | Remove-NetFirewallRule
  Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
  Remove-Item -Force -ErrorAction SilentlyContinue $stateFile, $installed
  Write-Output '已关闭严格模式，规则和计划任务已删除。'
  exit 0
}

if ($Refresh) {
  Assert-Admin
  $state = Get-Content $stateFile -Raw | ConvertFrom-Json
  Apply-Rules @($state.programs) ([string]$state.proxy)
  exit 0
}

if ($Enable) {
  Assert-Admin
  if (-not $Proxy) { $Proxy = Find-ProxyArg }
  if ($Program.Count -eq 0) { $Program = Find-Programs }
  $Program = @($Program | Where-Object { $_ -and (Test-Path $_) })
  if ($Program.Count -eq 0) { throw '找不到 Grok Bot.exe / Cursor.exe，请先打开它们或用 -Program 指定路径。' }
  New-Item -ItemType Directory -Force $home2 | Out-Null
  Copy-Item -Force $PSCommandPath $installed
  @{ programs = $Program; proxy = $Proxy } | ConvertTo-Json | Set-Content -Encoding UTF8 $stateFile
  Apply-Rules $Program $Proxy
  $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument ('-NoProfile -ExecutionPolicy Bypass -File "{0}" -Refresh' -f $installed)
  $triggers = @(
    (New-ScheduledTaskTrigger -AtStartup),
    (New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes $RefreshMinutes))
  )
  Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $triggers -User 'SYSTEM' -RunLevel Highest -Force | Out-Null
  Write-Output ("已开启：每 {0} 分钟和开机时刷新。代理：{1}" -f $RefreshMinutes, $(if ($Proxy) { $Proxy } else { '（未检测到，DoH 直连）' }))
  exit 0
}

Write-Output '用法：-Enable / -Disable / -Status（见脚本开头说明）'
