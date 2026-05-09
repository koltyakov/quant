#Requires -Version 5.1
[CmdletBinding()]
param(
  [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$installDir = "$env:LOCALAPPDATA\Programs\quant"
$binary     = 'quant'

function Show-Usage {
  Write-Host @"
Uninstall quant installed by scripts/install.ps1.

Removes $installDir\quant.exe and removes $installDir from your user PATH.
User data, MCP client config, and Ollama are left untouched.

Examples:
  irm https://raw.githubusercontent.com/koltyakov/quant/main/scripts/uninstall.ps1 | iex
"@
}

if ($Help) {
  Show-Usage
  exit 0
}

$target = Join-Path $installDir "$binary.exe"

if (Test-Path $target) {
  Remove-Item -LiteralPath $target -Force
  Write-Host "Removed $target"
} else {
  Write-Host "$binary is not installed at $target"
}

if (Test-Path $installDir) {
  $remaining = Get-ChildItem -LiteralPath $installDir -Force -ErrorAction SilentlyContinue
  if (-not $remaining) {
    Remove-Item -LiteralPath $installDir -Force
    Write-Host "Removed empty directory $installDir"
  }
}

$userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
if ($userPath) {
  $parts = $userPath -split ';' | Where-Object { $_ -and ($_ -ne $installDir) }
  $newPath = $parts -join ';'
  if ($newPath -ne $userPath) {
    [Environment]::SetEnvironmentVariable('PATH', $newPath, 'User')
    Write-Host "Removed $installDir from your user PATH."
    Write-Host 'Restart your terminal for the PATH change to take effect.'
  }
}

Write-Host 'User data, MCP client config, and Ollama were not removed.'
