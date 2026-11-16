# TailConnect installer for Windows 10/11 (PowerShell 5.1+).
#
#   irm https://github.com/imbikramsaha/TailConnect/releases/latest/download/install.ps1 | iex
#
# Downloads the latest installer, checks its SHA-256 against the release's
# SHA256SUMS and - when an OpenSSL 3 is available (e.g. from Git for Windows)
# - checks SHA256SUMS against TailConnect's release signing key, then installs
# for the current user (no administrator rights needed) and starts TailConnect.
# Pin a version with: $env:TAILCONNECT_VERSION = "0.3.9"

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"   # much faster downloads in PowerShell 5.1
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$Repo = "imbikramsaha/TailConnect"
# Release signing key (also compiled into the app; see SECURITY.md).
$PubKey = "bBlRWaSOYVipG6PdclZi7adlxdc3UxN7JM+LXHjdT6w="
# Signature over "tailconnect-release-v1`nselftest`n" - proves openssl can verify Ed25519.
$SelfTestSig = "ukresXeaXD3tmSFJePmj5T3Gpnns26rmxnO+v9vQUWCiUkp2tQv55faN8LUS/+/KNSIc2blwGe8UdPmFWuKwAg=="

function Say($m) { Write-Host $m -ForegroundColor Cyan }
function Fail($m) { Write-Host "error: $m" -ForegroundColor Red; throw $m }

if ($env:TAILCONNECT_VERSION) {
  $Base = "https://github.com/$Repo/releases/download/v$($env:TAILCONNECT_VERSION.TrimStart('v'))"
} else {
  $Base = "https://github.com/$Repo/releases/latest/download"
}

$Tmp = Join-Path ([IO.Path]::GetTempPath()) ("tailconnect-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $Tmp | Out-Null
try {
  Say "Downloading TailConnect for Windows..."
  $Sums = Join-Path $Tmp "SHA256SUMS"
  $Sig = Join-Path $Tmp "SHA256SUMS.sig"
  $Setup = Join-Path $Tmp "TailConnect-windows-setup.exe"
  Invoke-WebRequest -UseBasicParsing -Uri "$Base/SHA256SUMS" -OutFile $Sums
  Invoke-WebRequest -UseBasicParsing -Uri "$Base/SHA256SUMS.sig" -OutFile $Sig
  Invoke-WebRequest -UseBasicParsing -Uri "$Base/TailConnect-windows-setup.exe" -OutFile $Setup

  # 1) Checksum (always).
  $line = Get-Content $Sums | Where-Object { ($_ -split '\s+')[1] -eq "TailConnect-windows-setup.exe" } | Select-Object -First 1
  if (-not $line) { Fail "the installer is not listed in the release checksums" }
  $want = ($line -split '\s+')[0].ToLower()
  $got = (Get-FileHash -Algorithm SHA256 $Setup).Hash.ToLower()
  if ($got -ne $want) { Fail "checksum mismatch - refusing to install" }

  # 2) Release signature (when an Ed25519-capable openssl exists).
  $candidates = @("openssl.exe", "$env:ProgramFiles\Git\usr\bin\openssl.exe", "$env:ProgramFiles\OpenSSL-Win64\bin\openssl.exe")
  $der = [byte[]](0x30,0x2a,0x30,0x05,0x06,0x03,0x2b,0x65,0x70,0x03,0x21,0x00) + [Convert]::FromBase64String($PubKey)
  $pem = Join-Path $Tmp "pub.pem"
  Set-Content -Encoding ascii -Path $pem -Value ("-----BEGIN PUBLIC KEY-----`n" + [Convert]::ToBase64String($der) + "`n-----END PUBLIC KEY-----")
  $stMsg = Join-Path $Tmp "selftest.msg"; $stSig = Join-Path $Tmp "selftest.sig"
  [IO.File]::WriteAllBytes($stMsg, [Text.Encoding]::ASCII.GetBytes("tailconnect-release-v1`nselftest`n"))
  [IO.File]::WriteAllBytes($stSig, [Convert]::FromBase64String($SelfTestSig))
  $ossl = $null
  foreach ($c in $candidates) {
    $cmd = Get-Command $c -ErrorAction SilentlyContinue
    if (-not $cmd) { continue }
    & $cmd.Source pkeyutl -verify -pubin -inkey $pem -rawin -in $stMsg -sigfile $stSig *> $null
    if ($LASTEXITCODE -eq 0) { $ossl = $cmd.Source; break }
  }
  if ($ossl) {
    $msg = Join-Path $Tmp "sums.msg"; $sigBin = Join-Path $Tmp "sums.sig"
    [IO.File]::WriteAllBytes($msg, [Text.Encoding]::ASCII.GetBytes("tailconnect-release-v1`n") + [IO.File]::ReadAllBytes($Sums))
    [IO.File]::WriteAllBytes($sigBin, [Convert]::FromBase64String((Get-Content -Raw $Sig).Trim()))
    & $ossl pkeyutl -verify -pubin -inkey $pem -rawin -in $msg -sigfile $sigBin *> $null
    if ($LASTEXITCODE -ne 0) { Fail "release signature is NOT valid - refusing to install" }
    Say "Checksum and release signature verified."
  } else {
    Say "Checksum verified. (Install Git for Windows or OpenSSL 3 to also verify the release signature.)"
  }

  # 3) Install for this user and start TailConnect (the installer launches it).
  Say "Installing..."
  # Not Start-Process -Wait: in Windows PowerShell that also waits for every
  # process the installer starts, and the installer starts TailConnect,
  # which keeps running. Wait for the installer process alone.
  $p = Start-Process -FilePath $Setup -ArgumentList "/SILENT", "/SUPPRESSMSGBOXES", "/NORESTART" -PassThru
  $null = $p.Handle # keeps the exit code readable after the process ends
  $p.WaitForExit()
  if ($p.ExitCode -ne 0) { Fail "the installer exited with code $($p.ExitCode)" }
  Say "TailConnect is installed and starting - look for its icon in the system tray."
  Say "Open it any time from the Start menu. The 'tailconnect' command works in new terminals."

  # 4) Screen sharing with RustDesk: asked, unless TAILCONNECT_SCREEN=1/0
  #    decides. Not asked in automated installs (no console to ask in).
  $screen = $env:TAILCONNECT_SCREEN
  if (-not $screen -and -not $env:CI -and [Environment]::UserInteractive -and -not [Console]::IsInputRedirected) {
    Write-Host ""
    Write-Host "Also set up screen sharing with RustDesk?" -ForegroundColor Cyan -NoNewline
    Write-Host " It lets your paired devices see and control this"
    Write-Host "screen through TailConnect (free, open source; Windows asks for permission once). [Y/n] " -NoNewline
    $ans = Read-Host
    if ($ans -eq "" -or $ans -match '^(y|yes)$') { $screen = "1" } else { $screen = "0" }
  }
  if ($screen -match '^(1|y|yes)$') {
    $cli = Join-Path $env:LOCALAPPDATA "Programs\TailConnect\tailconnect.exe"
    $ready = $false
    for ($i = 0; $i -lt 40; $i++) {
      & $cli screen status *> $null
      if ($LASTEXITCODE -eq 0) { $ready = $true; break }
      Start-Sleep 1
    }
    if ($ready) {
      & $cli screen setup
      if ($LASTEXITCODE -ne 0) { Write-Host "Screen sharing wasn't set up; you can do it later in Settings - Screen sharing." -ForegroundColor Yellow }
    } else {
      Write-Host "TailConnect didn't start in time; set up screen sharing later in Settings - Screen sharing." -ForegroundColor Yellow
    }
  }
} finally {
  Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}
