# Build vole.exe on Windows against deps/whisper (see build-whisper-windows.ps1).
# Copies ggml/whisper/Vulkan DLLs next to the binary.

param(
    [string]$OutDir = (Join-Path $PSScriptRoot "..\.bin"),
    [string]$WhisperPrefix = (Join-Path $PSScriptRoot "..\deps\whisper")
)

$ErrorActionPreference = "Stop"
$RepoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$OutDir = [IO.Path]::GetFullPath($OutDir)
$WhisperPrefix = [IO.Path]::GetFullPath($WhisperPrefix)

if (-not (Get-Command gcc -ErrorAction SilentlyContinue)) {
    throw "gcc not on PATH. Install MinGW-w64 (CGO) and reopen the shell."
}
if (-not (Test-Path (Join-Path $WhisperPrefix "include\whisper.h"))) {
    throw "whisper.h not found under $WhisperPrefix. Run scripts\build-whisper-windows.ps1 first."
}

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$LibDir = Join-Path $WhisperPrefix "lib"
$IncDir = Join-Path $WhisperPrefix "include"
$BinDir = Join-Path $WhisperPrefix "bin"

$env:CGO_ENABLED = "1"
$env:CC = "gcc"
$cflags = "-I$IncDir"
$ldflags = "-L$LibDir -lwhisper -lggml -lggml-base -lggml-cpu -lggml-vulkan -lstdc++ -lm"
if ($env:VULKAN_SDK) {
    $vlib = Join-Path $env:VULKAN_SDK "Lib"
    $ldflags += " -L$vlib -lvulkan-1"
}
$env:CGO_CFLAGS = $cflags
$env:CGO_LDFLAGS = $ldflags

Push-Location $RepoRoot
try {
    $version = "dev"
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $described = git describe --tags --always --dirty 2>$null
        if ($described) { $version = $described.Trim() }
    }
    Write-Host "CGO_CFLAGS=$env:CGO_CFLAGS"
    Write-Host "CGO_LDFLAGS=$env:CGO_LDFLAGS"
    go build -ldflags "-X main.version=$version" -o (Join-Path $OutDir "vole.exe") ./cmd/vole
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
} finally {
    Pop-Location
}

# Runtime DLLs beside vole.exe (working directory for Task Scheduler autostart).
$dllSources = @()
if (Test-Path $BinDir) {
    $dllSources += Get-ChildItem $BinDir -Filter *.dll
}
if (Test-Path $LibDir) {
    $dllSources += Get-ChildItem $LibDir -Filter *.dll
}
if ($env:VULKAN_SDK) {
    foreach ($rel in @("Bin\vulkan-1.dll", "Bin32\vulkan-1.dll")) {
        $p = Join-Path $env:VULKAN_SDK $rel
        if (Test-Path $p) { $dllSources += Get-Item $p; break }
    }
}
foreach ($dll in $dllSources) {
    Copy-Item $dll.FullName $OutDir -Force
}

Write-Host "Built $(Join-Path $OutDir 'vole.exe')"
Write-Host "Keep the copied DLLs next to vole.exe. Working directory must be this folder."
