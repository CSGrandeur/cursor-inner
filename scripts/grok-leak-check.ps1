<#
.SYNOPSIS
  检查 Grok Bot 与 Cursor 的直连里有没有官方端点（只读，不改任何设置）。

.DESCRIPTION
  采样两个应用所有进程的 TCP 连接，对每个既不是回环、也不是代理的远端判断是否官方端点：
    1. 运行时经代理用 DoH 解析官方域名清单（并上本地 DNS 结果），IP 命中即官方；
    2. 443 端口的远端再取一次 TLS 证书，证书名属于官方域名即官方（能认出 usNN.cursorvm.com 这类动态主机）；
    3. 反向解析名属于官方域名即官方。
  官方端点直连标 FAIL（退出码 1）；GitHub、Google 等第三方直连标 INFO，不算失败。
  证书检查会由本脚本直连对方 443 一次，只握手不发数据；不想要可加 -NoTlsCheck。

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\grok-leak-check.ps1
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\grok-leak-check.ps1 -Proxy http://192.0.2.10:1080 -Samples 10
#>
param(
  [string]$Proxy = '',
  [string[]]$ProcessName = @('Grok Bot', 'Cursor'),
  [int]$Samples = 5,
  [int]$IntervalSeconds = 2,
  [switch]$NoTlsCheck
)

$ErrorActionPreference = 'Stop'
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

function Test-Loopback([string]$addr) {
  return ($addr -eq '::1' -or $addr -like '127.*' -or $addr -eq '0.0.0.0' -or $addr -eq '::' -or $addr -like '::ffff:127.*')
}

function Get-CertName([string]$ip) {
  try {
    $c = New-Object Net.Sockets.TcpClient
    $ar = $c.BeginConnect($ip, 443, $null, $null)
    if (-not $ar.AsyncWaitHandle.WaitOne(5000)) { $c.Close(); return '' }
    $c.EndConnect($ar)
    $s = New-Object Net.Security.SslStream($c.GetStream(), $false, { $true })
    $s.AuthenticateAsClient($ip)
    $cert = New-Object Security.Cryptography.X509Certificates.X509Certificate2($s.RemoteCertificate)
    $names = @($cert.GetNameInfo('DnsName', $false))
    $san = $cert.Extensions | Where-Object { $_.Oid.Value -eq '2.5.29.17' }
    if ($san) { $names += ($san.Format($false) -split ',\s*' | ForEach-Object { $_ -replace '^DNS Name=', '' }) }
    $c.Close()
    return ($names | Where-Object { $_ } | Select-Object -Unique) -join ' '
  } catch { return '' }
}

if (-not $Proxy) { $Proxy = Find-ProxyArg }
$proxyEndpoint = ''
if ($Proxy) { try { $u = [Uri]$Proxy; $proxyEndpoint = "{0}:{1}" -f $u.Host, $u.Port } catch { } }

$all = Get-CimInstance Win32_Process | Select-Object ProcessId, Name, CommandLine, ExecutablePath
$apps = @{}
foreach ($name in $ProcessName) {
  $root = Get-Process -Name $name -ErrorAction SilentlyContinue | Select-Object -First 1
  if (-not $root) { Write-Output "未运行：$name"; continue }
  $exe = ($all | Where-Object { $_.ProcessId -eq $root.Id }).ExecutablePath
  foreach ($p in $all) { if ($p.ExecutablePath -and $p.ExecutablePath -ieq $exe) { $apps[[int]$p.ProcessId] = $name } }
  $main = $all | Where-Object { $_.ExecutablePath -ieq $exe -and $_.CommandLine -notmatch '--type=' } | Select-Object -First 1
  Write-Output ("{0}：{1} 个进程，{2}" -f $name, @($apps.Values | Where-Object { $_ -eq $name }).Count, $exe)
  if ($name -eq 'Grok Bot' -and $main) {
    foreach ($flag in '--proxy-server=', '--use-env-proxy', '--disable-quic', '--proxy-bypass-list=', '--force-webrtc-ip-handling-policy=') {
      $hit = [regex]::Match([string]$main.CommandLine, [regex]::Escape($flag) + '\S*')
      if ($hit.Success) { Write-Output ("  参数 {0}" -f $hit.Value) } else { Write-Output ("  缺少 {0}" -f $flag) }
    }
  }
}
if ($apps.Count -eq 0) { exit 2 }

Write-Output ("解析官方域名（{0} 个，DoH 经 {1}）…" -f $OfficialHosts.Count, $(if ($Proxy) { $Proxy } else { '直连' }))
$official = Resolve-Official $Proxy
if ($script:DohFailed) { Write-Output 'WARN DoH 全部失败，只用了本地 DNS（可能被污染），IP 判定不完整' }
Write-Output ("  得到 {0} 个官方 IP" -f $official.Count)

$remotes = @{}
for ($i = 0; $i -lt $Samples; $i++) {
  foreach ($c in @(Get-NetTCPConnection -ErrorAction SilentlyContinue | Where-Object { $apps.ContainsKey([int]$_.OwningProcess) })) {
    if ($c.State -eq 'Listen' -or $c.State -eq 'Bound') { continue }
    if (Test-Loopback $c.RemoteAddress) { continue }
    $ep = "{0}:{1}" -f $c.RemoteAddress, $c.RemotePort
    if ($proxyEndpoint -and $ep -ieq $proxyEndpoint) { continue }
    $role = ($all | Where-Object { $_.ProcessId -eq $c.OwningProcess } | ForEach-Object { if ($_.CommandLine -match '--utility-sub-type=(\S+)') { $Matches[1] } elseif ($_.CommandLine -match '--type=(\S+)') { $Matches[1] } else { 'main' } }) -join ''
    $remotes["$ep|$($c.OwningProcess)"] = [pscustomobject]@{ App = $apps[[int]$c.OwningProcess]; Ip = [string]$c.RemoteAddress; Port = $c.RemotePort; Pid = $c.OwningProcess; Role = $role; State = [string]$c.State }
  }
  if ($i + 1 -lt $Samples) { Start-Sleep -Seconds $IntervalSeconds }
}

$fail = 0
$certCache = @{}
foreach ($r in $remotes.Values | Sort-Object App, Ip) {
  $why = ''
  $ptr = ''
  if ($official.ContainsKey($r.Ip)) { $why = "官方 IP（$($official[$r.Ip])）" }
  if (-not $why) {
    try { $ptr = (Resolve-DnsName $r.Ip -Type PTR -DnsOnly -ErrorAction Stop | Select-Object -First 1).NameHost } catch { }
    if (Test-OfficialName $ptr) { $why = "反向解析 $ptr" }
  }
  $cert = ''
  if (-not $why -and $r.Port -eq 443 -and -not $NoTlsCheck) {
    if (-not $certCache.ContainsKey($r.Ip)) { $certCache[$r.Ip] = Get-CertName $r.Ip }
    $cert = $certCache[$r.Ip]
    foreach ($n in ($cert -split ' ')) { if (Test-OfficialName ($n -replace '^\*\.', '')) { $why = "证书 $n"; break } }
  }
  $line = "{0} {1}:{2} pid={3} role={4} state={5}" -f $r.App, $r.Ip, $r.Port, $r.Pid, $r.Role, $r.State
  if ($why) { $fail++; Write-Output "FAIL $line  <- $why" }
  else { Write-Output ("INFO {0}  第三方{1}" -f $line, $(if ($cert) { "（证书 $(($cert -split ' ')[0])）" } elseif ($ptr) { "（$ptr）" } else { '' })) }
}
if ($fail -gt 0) { Write-Output "结论：$fail 条官方端点直连（应走代理）。"; exit 1 }
Write-Output '结论：没有发现官方端点直连。'
exit 0
