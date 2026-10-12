<#
.SYNOPSIS
  检查 Grok Bot 与 Cursor 的直连里有没有官方端点，并实测代理范围（全局 / 仅官方）的分流（只读，不改任何设置）。

.DESCRIPTION
  一、连接采样：采样两个应用（以及 cursor-inner 本身）所有进程的 TCP 连接，对每个既不是回环、也不是代理的远端判断是否官方端点：
    1. 运行时经代理用 DoH 解析官方域名清单（并上本地 DNS 结果），IP 命中即官方；
    2. 443 端口的远端再取一次 TLS 证书，证书名属于官方域名即官方（能认出 usNN.cursorvm.com 这类动态主机）；
    3. 反向解析名属于官方域名即官方。
  官方端点直连标 FAIL（退出码 1）；GitHub、Google 等第三方直连标 INFO，不算失败。
  证书检查会由本脚本直连对方 443 一次，只握手不发数据；不想要可加 -NoTlsCheck。

  二、路由实测：经 Grok Bot / Cursor 实际使用的本机代理地址（Grok 的 --proxy-server、Cursor 的 http.proxy，
  或 -Via 指定）各 CONNECT 一个官方主机和一个第三方主机，并完成一次 TLS 握手，同时看提供这个地址的进程
  （cursor-inner）新开的连接：
    - 官方主机：两种范围下都必须走代理；cursor-inner 直连了官方 IP 标 FAIL；
    - 第三方主机：全局时应走代理、仅官方时应直连，不符标 FAIL。
  代理范围默认从 cursor-inner 的 config.json 读取，也可以用 -Scope 指定。-NoRouteProbe 跳过这一步；
  -RouteOnly 只做这一步（不采样连接，只解析两个探测主机）。

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\grok-leak-check.ps1
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\grok-leak-check.ps1 -Proxy http://192.0.2.10:1080 -Samples 10
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\grok-leak-check.ps1 -Via http://127.0.0.1:50123 -Scope official
#>
param(
  [string]$Proxy = '',
  [string[]]$ProcessName = @('Grok Bot', 'Cursor'),
  [int]$Samples = 5,
  [int]$IntervalSeconds = 2,
  [switch]$NoTlsCheck,
  [ValidateSet('auto', 'global', 'official')]
  [string]$Scope = 'auto',
  [string]$ConfigDir = (Join-Path $env:APPDATA 'cursor-inner'),
  [string[]]$Via = @(),
  [string]$ProbeOfficial = 'api.x.ai',
  [string]$ProbeThirdParty = 'www.microsoft.com',
  [switch]$NoRouteProbe,
  [switch]$RouteOnly
)

$ErrorActionPreference = 'Stop'
# ---- 官方端点清单（两份脚本里保持一致）----
$OfficialSuffixes = @('grok.com', 'x.ai', 'spacexai.com', 'cursor.com', 'cursor.sh', 'cursorapi.com', 'cursor-cdn.com', 'cursorvm.com', 'anysphere.co', 'openai.com', 'chatgpt.com', 'oaistatic.com', 'oaiusercontent.com', 'oaistatsig.com')
$OfficialHosts = @(
  'grok.com', 'www.grok.com', 'accounts.grok.com', 'assets.grok.com', 'api.grok.com',
  'x.ai', 'www.x.ai', 'api.x.ai', 'accounts.x.ai', 'auth.x.ai', 'console.x.ai', 'docs.x.ai', 'grok.x.ai', 'cdn.x.ai',
  'spacexai.com', 'www.spacexai.com',
  'cursor.com', 'www.cursor.com', 'api.cursor.com', 'downloads.cursor.com', 'docs.cursor.com', 'forum.cursor.com', 'authenticator.cursor.sh',
  'cursor.sh', 'api1.cursor.sh', 'api2.cursor.sh', 'api3.cursor.sh', 'api4.cursor.sh', 'api5.cursor.sh', 'repo42.cursor.sh', 'metrics.cursor.sh', 'prod.authentication.cursor.sh',
  'marketplace.cursorapi.com', 'cursor-cdn.com', 'anysphere.co',
  'openai.com', 'api.openai.com', 'auth.openai.com', 'chatgpt.com', 'ab.chatgpt.com', 'chat.openai.com',
  'oaistatic.com', 'cdn.oaistatic.com', 'oaiusercontent.com', 'oaistatsig.com'
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

function Read-InnerConfig([string]$dir) {
  try { return Get-Content -Raw -Encoding UTF8 (Join-Path $dir 'config.json') | ConvertFrom-Json } catch { return $null }
}

function Test-LoopbackUrl([string]$raw) {
  try { $u = [Uri]$raw } catch { return $false }
  return ($u.Host -eq '127.0.0.1' -or $u.Host -eq 'localhost' -or $u.Host -eq '::1' -or $u.Host -eq '[::1]')
}

# Find-ViaProxies 返回 Grok / Cursor 实际使用、且在本机回环上的代理地址（由 cursor-inner 提供）。
function Find-ViaProxies {
  $out = @()
  $grok = Find-ProxyArg
  if ($grok -and (Test-LoopbackUrl $grok)) { $out += $grok }
  try {
    $settings = Get-Content -Raw -Encoding UTF8 (Join-Path $env:APPDATA 'Cursor\User\settings.json')
    if ($settings -match '"http\.proxy"\s*:\s*"([^"]+)"' -and (Test-LoopbackUrl $Matches[1])) { $out += $Matches[1] }
  } catch { }
  return @($out | Select-Object -Unique)
}

function Get-ListenerPid([int]$port) {
  $l = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($l) { return [int]$l.OwningProcess }
  return 0
}

function Get-RemoteSet([int]$procId) {
  $set = @{}
  foreach ($c in @(Get-NetTCPConnection -OwningProcess $procId -ErrorAction SilentlyContinue)) {
    if ($c.State -eq 'Listen' -or $c.State -eq 'Bound') { continue }
    if (Test-Loopback $c.RemoteAddress) { continue }
    $set[("{0}|{1}|{2}" -f $c.RemoteAddress, $c.RemotePort, $c.LocalPort)] = $c
  }
  return $set
}

function Resolve-Local([string]$name) {
  try { return @(Resolve-DnsName $name -DnsOnly -ErrorAction Stop | Where-Object { $_.Type -eq 'A' -or $_.Type -eq 'AAAA' } | ForEach-Object { [string]$_.IPAddress }) } catch { return @() }
}

# Invoke-RouteProbe 经 $via CONNECT 到 $target:443 并握手 TLS，返回这段时间里提供 $via 的进程新开的非回环连接。
function Invoke-RouteProbe([string]$via, [string]$target, [int]$procId) {
  $u = [Uri]$via
  $before = Get-RemoteSet $procId
  $r = [pscustomobject]@{ Ok = $false; Error = ''; New = @() }
  $client = New-Object Net.Sockets.TcpClient
  try {
    $client.ReceiveTimeout = 15000; $client.SendTimeout = 15000
    $client.Connect($u.Host, $u.Port)
    $stream = $client.GetStream()
    $req = [Text.Encoding]::ASCII.GetBytes("CONNECT ${target}:443 HTTP/1.1`r`nHost: ${target}:443`r`n`r`n")
    $stream.Write($req, 0, $req.Length)
    $head = ''
    while (-not $head.EndsWith("`r`n`r`n")) {
      $b = $stream.ReadByte()
      if ($b -lt 0) { break }
      $head += [char]$b
      if ($head.Length -gt 8192) { break }
    }
    if ($head -notmatch '^HTTP/1\.[01] 200') { $r.Error = 'CONNECT 未接通：' + (($head -split "`r`n")[0]); return $r }
    $ssl = New-Object Net.Security.SslStream($stream, $true, { $true })
    try { $ssl.AuthenticateAsClient($target) } catch { $r.Error = 'TLS 握手失败：' + $_.Exception.InnerException.Message }
    Start-Sleep -Milliseconds 400
    $after = Get-RemoteSet $procId
    $r.New = @($after.Keys | Where-Object { -not $before.ContainsKey($_) } | ForEach-Object { $after[$_] })
    $r.Ok = $true
    try { $ssl.Dispose() } catch { }
  } catch {
    $r.Error = $_.Exception.Message
  } finally {
    $client.Close()
  }
  return $r
}

if (-not $Proxy) { $Proxy = Find-ProxyArg }
$proxyEndpoint = ''
if ($Proxy) { try { $u = [Uri]$Proxy; $proxyEndpoint = "{0}:{1}" -f $u.Host, $u.Port } catch { } }
$innerCfg = Read-InnerConfig $ConfigDir
$upstreamEndpoint = ''
if ($innerCfg -and $innerCfg.proxy -and $innerCfg.proxy.address) {
  $a = [string]$innerCfg.proxy.address
  if ($a -notmatch '://') { $a = 'http://' + $a }
  try { $uu = [Uri]$a; $upstreamEndpoint = "{0}:{1}" -f $uu.Host, $uu.Port } catch { }
}
if ($Scope -eq 'auto') {
  $Scope = 'global'
  if ($innerCfg -and $innerCfg.proxy -and ([string]$innerCfg.proxy.scope) -eq 'official') { $Scope = 'official' }
  $scopeFrom = "来自 $(Join-Path $ConfigDir 'config.json')"
} else { $scopeFrom = '参数指定' }
Write-Output ("代理范围：{0}（{1}）" -f $(if ($Scope -eq 'official') { '仅官方' } else { '全局' }), $scopeFrom)
$Via = @($Via | ForEach-Object { $_ -split ',' } | ForEach-Object { $_.Trim() } | Where-Object { $_ })
if (-not $NoRouteProbe -and $Via.Count -eq 0) { $Via = Find-ViaProxies }
if ($RouteOnly) { $NoRouteProbe = $false; $ProcessName = @(); $Samples = 0 }
$innerPids = @{}
foreach ($v in $Via) { try { $p = Get-ListenerPid ([Uri]$v).Port; if ($p) { $innerPids[$v] = $p } } catch { } }

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
if (-not $RouteOnly) { foreach ($p in ($innerPids.Values | Sort-Object -Unique)) { $apps[[int]$p] = 'cursor-inner' } }
if ($apps.Count -eq 0 -and ($NoRouteProbe -or $Via.Count -eq 0)) { exit 2 }

if ($RouteOnly) { $OfficialHosts = @($ProbeOfficial) }
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
    if ($upstreamEndpoint -and $ep -ieq $upstreamEndpoint) { continue }
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
$routeFail = 0
if (-not $NoRouteProbe) {
  if ($Via.Count -eq 0) { Write-Output 'WARN 路由实测跳过：没找到 Grok / Cursor 使用的本机代理地址（可用 -Via 指定）' }
  $officialIps = @{}
  foreach ($k in $official.Keys) { $officialIps[$k] = $true }
  foreach ($ip in (Resolve-Local $ProbeOfficial)) { $officialIps[$ip] = $true }
  $thirdIps = @{}
  foreach ($ip in (Resolve-Local $ProbeThirdParty)) { $thirdIps[$ip] = $true }
  foreach ($v in $Via) {
    $procId = $innerPids[$v]
    if (-not $procId) { Write-Output "WARN 路由 $v：没有进程在监听这个地址"; continue }
    foreach ($target in @($ProbeOfficial, $ProbeThirdParty)) {
      $isOfficial = ($target -eq $ProbeOfficial)
      $res = Invoke-RouteProbe $v $target $procId
      $label = "路由 $v -> ${target}:443 pid=$procId"
      if (-not $res.Ok) { Write-Output "WARN $label  $($res.Error)"; continue }
      $ips = if ($isOfficial) { $officialIps } else { $thirdIps }
      $hit = @($res.New | Where-Object { $ips.ContainsKey([string]$_.RemoteAddress) -or ($_.RemotePort -eq 443 -and -not $isOfficial -and $thirdIps.Count -eq 0) })
      $other = @($res.New | Where-Object { $_.RemotePort -eq 443 -and -not $ips.ContainsKey([string]$_.RemoteAddress) })
      $directTo = (@($hit | ForEach-Object { "{0}:{1}" -f $_.RemoteAddress, $_.RemotePort }) -join ' ')
      $note = ''
      if ($res.Error) { $note = "（$($res.Error)）" }
      if ($isOfficial) {
        if ($hit.Count -gt 0) { $routeFail++; Write-Output "FAIL $label  官方端点被 cursor-inner 直连 $directTo" }
        else {
          Write-Output "PASS $label  走代理$note"
          if ($other.Count -gt 0) { Write-Output ("WARN $label  同时段另有直连 {0}（可能是别的请求）" -f ((@($other | ForEach-Object { "{0}:{1}" -f $_.RemoteAddress, $_.RemotePort })) -join ' ')) }
        }
      } elseif ($Scope -eq 'official') {
        if ($hit.Count -gt 0) { Write-Output "PASS $label  第三方直连 $directTo$note" }
        else { $routeFail++; Write-Output "FAIL $label  仅官方模式下第三方应直连，实际走了代理$note" }
      } else {
        if ($hit.Count -gt 0) { $routeFail++; Write-Output "FAIL $label  全局模式下第三方应走代理，实际直连 $directTo" }
        else { Write-Output "PASS $label  第三方走代理$note" }
      }
    }
  }
}

if ($fail -gt 0 -or $routeFail -gt 0) {
  $parts = @()
  if ($fail -gt 0) { $parts += "$fail 条官方端点直连（应走代理）" }
  if ($routeFail -gt 0) { $parts += "$routeFail 项路由与代理范围不符" }
  Write-Output ("结论：{0}。" -f ($parts -join '；'))
  exit 1
}
Write-Output '结论：没有发现官方端点直连，路由与代理范围一致。'
exit 0
