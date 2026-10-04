# cursor-inner installer for Windows.
#   irm https://raw.githubusercontent.com/CSGrandeur/cursor-inner/main/install.ps1 | iex
# Environment: $env:CURSOR_INNER_VERSION = 'v0.1.0' to pin a release.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'CSGrandeur/cursor-inner'
$version = $env:CURSOR_INNER_VERSION
if (-not $version) {
    $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
}
if ($version -notlike 'v*') { throw 'cannot determine the latest release' }

if (Get-Process -Name 'cursor-inner' -ErrorAction SilentlyContinue) {
    throw 'cursor-inner is running. Quit it first (Quit button or close its window).'
}

$name = "cursor-inner-$version-windows-amd64.exe"
$base = "https://github.com/$repo/releases/download/$version"
$dir = Join-Path $env:LOCALAPPDATA 'Programs\cursor-inner'
$exe = Join-Path $dir 'cursor-inner.exe'
$tmp = Join-Path ([IO.Path]::GetTempPath()) "$name.download"

Write-Host "Downloading $name"
Invoke-WebRequest -UseBasicParsing "$base/$name" -OutFile $tmp
$sums = (Invoke-WebRequest -UseBasicParsing "$base/SHA256SUMS.txt").Content
$expected = ($sums -split "`n" | Where-Object { $_ -match "\s$([regex]::Escape($name))\s*$" } | ForEach-Object { ($_ -split '\s+')[0] })
$actual = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
if (-not $expected -or $expected.ToLower() -ne $actual) {
    Remove-Item $tmp -Force
    throw 'checksum mismatch'
}

New-Item -ItemType Directory -Force -Path $dir | Out-Null
Move-Item -Force $tmp $exe
Unblock-File $exe

$programs = [Environment]::GetFolderPath('Programs')
$shell = New-Object -ComObject WScript.Shell
$link = $shell.CreateShortcut((Join-Path $programs 'cursor-inner.lnk'))
$link.TargetPath = $exe
$link.WorkingDirectory = $dir
$link.Description = 'Bring your own models into Cursor'
$link.Save()

Write-Host "Installed: $exe ($version)"
Write-Host 'Start it from the Start menu, or run:'
Write-Host "  & '$exe'"
