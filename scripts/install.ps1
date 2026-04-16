#Requires -Version 5.1
[CmdletBinding()]
param(
  [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repo       = 'koltyakov/quant'
$installDir = "$env:LOCALAPPDATA\Programs\quant"
$binary     = 'quant'

function Show-Usage {
  Write-Host @"
Install quant from GitHub Releases.

Installs the latest release to $installDir.

Examples:
  irm https://raw.githubusercontent.com/koltyakov/quant/main/scripts/install.ps1 | iex
"@
}

if ($Help) {
  Show-Usage
  exit 0
}

function Get-AssetArch {
  switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { return 'x86_64' }
    'ARM64' { return 'arm64' }
    default {
      Write-Error "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE"
      exit 1
    }
  }
}

function Invoke-Download {
  param([string]$Url, [string]$Out)
  try {
    Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing
  } catch {
    Write-Error "Failed to download ${Url}: $_"
    exit 1
  }
}

function Confirm-OllamaInstall {
  if (-not [Environment]::UserInteractive) { return $false }
  $answer = Read-Host 'Install Ollama now? [y/N]'
  return $answer -match '^(y|yes)$'
}

function Install-OllamaIfNeeded {
  if (Get-Command ollama -ErrorAction SilentlyContinue) {
    Write-Host "Ollama already installed: $((Get-Command ollama).Source)"
    return
  }

  Write-Host 'Ollama was not found on PATH.'
  Write-Host 'quant uses Ollama by default for local embeddings.'

  if (-not (Confirm-OllamaInstall)) {
    Write-Host 'Skipping Ollama install.'
    Write-Host 'Install Ollama with:'
    Write-Host '  winget install Ollama.Ollama'
    Write-Host 'Or download it from: https://ollama.com/download'
    return
  }

  if (Get-Command winget -ErrorAction SilentlyContinue) {
    Write-Host 'Installing Ollama via winget...'
    winget install --id Ollama.Ollama -e --silent
    if (Get-Command ollama -ErrorAction SilentlyContinue) {
      Write-Host "Installed Ollama: $((Get-Command ollama).Source)"
      Write-Host 'quant will start Ollama and pull the embedding model on first use if needed.'
    } else {
      Write-Host 'Ollama installer finished, but ollama is still not on PATH.'
      Write-Host 'Open a new terminal or follow the installer output before running quant.'
    }
  } else {
    Write-Host 'winget not found. Download Ollama from: https://ollama.com/download'
  }
}

$arch   = Get-AssetArch
$asset  = "${binary}_Windows_${arch}.zip"
$url    = "https://github.com/$repo/releases/latest/download/$asset"
$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmpDir | Out-Null

try {
  $archive = "$tmpDir\$asset"
  Write-Host "Downloading $asset from $repo..."
  Invoke-Download -Url $url -Out $archive

  Expand-Archive -LiteralPath $archive -DestinationPath $tmpDir -Force
  $exeSrc = "$tmpDir\$binary.exe"
  if (-not (Test-Path $exeSrc)) {
    Write-Error "$binary.exe not found in $asset"
    exit 1
  }

  if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir | Out-Null
  }
  Copy-Item -Path $exeSrc -Destination "$installDir\$binary.exe" -Force

  Write-Host "Installed $binary to $installDir\$binary.exe"

  $userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
  if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable('PATH', "$userPath;$installDir", 'User')
    Write-Host "Added $installDir to your user PATH."
    Write-Host 'Restart your terminal for the PATH change to take effect.'
  }

  Install-OllamaIfNeeded
} finally {
  Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
