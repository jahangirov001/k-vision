# Installs K-Vision on Windows:
#   %LOCALAPPDATA%\K-Vision\kv.exe     the program (no admin rights needed)
#   kv on your PATH (user PATH)            terminal UI, `kv ui`, `kv help`
#   Start menu: "K-Vision"             opens the window
#
#   powershell -ExecutionPolicy Bypass -File install.ps1             install or update
#   powershell -ExecutionPolicy Bypass -File install.ps1 -Uninstall  remove it
#
# Run it from the unpacked release zip (next to kv.exe), or in the source folder with
# Go installed, and it builds kv.exe itself.
param([switch]$Uninstall)
$ErrorActionPreference = "Stop"

$Dir      = Join-Path $env:LOCALAPPDATA "K-Vision"
$Exe      = Join-Path $Dir "kv.exe"
$Shortcut = Join-Path ([Environment]::GetFolderPath("Programs")) "K-Vision.lnk"
function Say($m) { Write-Host "==> $m" -ForegroundColor Cyan }

function Remove-FromPath {
  $p = [Environment]::GetEnvironmentVariable("Path", "User")
  if ($p) {
    $parts = $p.Split(";") | Where-Object { $_ -and ($_.TrimEnd("\") -ne $Dir.TrimEnd("\")) }
    [Environment]::SetEnvironmentVariable("Path", ($parts -join ";"), "User")
  }
}

if ($Uninstall) {
  Get-Process kv -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $Exe } | Stop-Process -Force
  Remove-Item $Dir -Recurse -Force -ErrorAction SilentlyContinue
  Remove-Item $Shortcut -Force -ErrorAction SilentlyContinue
  Remove-FromPath
  Say "Removed. Your settings and audit log are still in $env:USERPROFILE\.k-vision (delete that folder to remove them too)."
  exit 0
}

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$prebuilt = @(
  (Join-Path $here "kv.exe"),
  (Join-Path $here "kv_windows_$arch.exe"),
  (Join-Path $here "dist\kv_windows_$arch.exe")
) | Where-Object { Test-Path $_ } | Select-Object -First 1

New-Item -ItemType Directory -Force -Path $Dir | Out-Null
Get-Process kv -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $Exe } | Stop-Process -Force

if ($prebuilt) {
  Say "Installing $prebuilt"
  Copy-Item $prebuilt $Exe -Force
} elseif (Get-Command go -ErrorAction SilentlyContinue) {
  Say "Building kv.exe"
  Push-Location $here
  try { go build -trimpath -ldflags "-s -w" -o $Exe ./cmd/kv } finally { Pop-Location }
} else {
  throw "No kv.exe next to this script and Go is not installed. Run 'make release' on another machine and copy dist\kv_windows_$arch.exe here, or install Go."
}

Say "Adding $Dir to your PATH"
Remove-FromPath
$p = [Environment]::GetEnvironmentVariable("Path", "User")
[Environment]::SetEnvironmentVariable("Path", ((@($p, $Dir) | Where-Object { $_ }) -join ";"), "User")

Say "Creating the Start menu shortcut"
# kv.exe is a console program; started from the shortcut directly it would leave a
# console window open next to the app. A tiny VBScript starts it hidden instead.
$Launcher = Join-Path $Dir "K-Vision.vbs"
Set-Content -Path $Launcher -Encoding ASCII -Value @"
CreateObject("WScript.Shell").Run """$Exe"" ui", 0, False
"@
$ws = New-Object -ComObject WScript.Shell
$lnk = $ws.CreateShortcut($Shortcut)
$lnk.TargetPath = Join-Path $env:WINDIR "System32\wscript.exe"
$lnk.Arguments = "`"$Launcher`""
$lnk.WorkingDirectory = $env:USERPROFILE
$lnk.IconLocation = "$Exe,0"
$lnk.Description = "K-Vision"
$lnk.Save()

Say "Done."
Write-Host ""
Write-Host "  Open the app      Start menu: K-Vision   or: kv ui"
Write-Host "  Terminal UI       kv   (open a NEW terminal first, so it sees the PATH)"
Write-Host "  Help              kv help"
Write-Host "  Uninstall         powershell -ExecutionPolicy Bypass -File install.ps1 -Uninstall"
Write-Host ""
