# Build whisper.cpp with Vulkan on Windows
#
# Prerequisites:
#   - MinGW-w64 gcc/g++ (CGO requires gcc; MSVC is not used)
#   - CMake 3.16+
#   - Ninja (recommended) or MinGW Makefiles
#   - Vulkan SDK (https://vulkan.lunarg.com/) so $env:VULKAN_SDK is set
#
# Result: deps/whisper/{include,lib,bin} consumed by internal/whisper/cgo_windows.go
# and copied next to vole.exe by scripts/build-windows.ps1.

param(
    [string]$Prefix = (Join-Path $PSScriptRoot "..\deps\whisper"),
    [string]$SourceDir = (Join-Path $PSScriptRoot "..\deps\whisper.cpp")
)

$ErrorActionPreference = "Stop"
$Prefix = [IO.Path]::GetFullPath($Prefix)
$RepoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))

function Need($cmd) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        throw "Required tool not on PATH: $cmd"
    }
}

Need gcc
Need g++
Need cmake

if (-not $env:VULKAN_SDK -or -not (Test-Path $env:VULKAN_SDK)) {
    throw "VULKAN_SDK is not set. Install the LunarG Vulkan SDK and reopen the shell."
}

New-Item -ItemType Directory -Force -Path (Split-Path $Prefix) | Out-Null

if (-not (Test-Path $SourceDir)) {
    Write-Host "Cloning whisper.cpp into $SourceDir"
    git clone --depth 1 https://github.com/ggerganov/whisper.cpp $SourceDir
}

$BuildDir = Join-Path $SourceDir "build-vulkan"
$VulkanInclude = Join-Path $env:VULKAN_SDK "Include"
$VulkanLib = Join-Path $env:VULKAN_SDK "Lib"
$VulkanLib64 = Join-Path $env:VULKAN_SDK "Lib"
if (Test-Path (Join-Path $env:VULKAN_SDK "Lib")) {
    $VulkanLib = Join-Path $env:VULKAN_SDK "Lib"
}

$Generator = "MinGW Makefiles"
if (Get-Command ninja -ErrorAction SilentlyContinue) {
    $Generator = "Ninja"
}

Write-Host "Configuring whisper.cpp (Vulkan) with $Generator"
$cmakeArgs = @(
    "-S", $SourceDir
    "-B", $BuildDir
    "-G", $Generator
    "-DCMAKE_BUILD_TYPE=Release"
    "-DCMAKE_C_COMPILER=gcc"
    "-DCMAKE_CXX_COMPILER=g++"
    "-DCMAKE_INSTALL_PREFIX=$Prefix"
    "-DBUILD_SHARED_LIBS=ON"
    "-DGGML_VULKAN=ON"
    "-DWHISPER_BUILD_EXAMPLES=OFF"
    "-DWHISPER_BUILD_TESTS=OFF"
    "-DVulkan_INCLUDE_DIR=$VulkanInclude"
)
if (Test-Path (Join-Path $VulkanLib "vulkan-1.lib")) {
    $cmakeArgs += "-DVulkan_LIBRARY=$(Join-Path $VulkanLib 'vulkan-1.lib')"
} elseif (Test-Path (Join-Path $VulkanLib "libvulkan-1.dll.a")) {
    $cmakeArgs += "-DVulkan_LIBRARY=$(Join-Path $VulkanLib 'libvulkan-1.dll.a')"
}

cmake @cmakeArgs
if ($LASTEXITCODE -ne 0) { throw "cmake configure failed" }

cmake --build $BuildDir --config Release
if ($LASTEXITCODE -ne 0) { throw "cmake build failed" }

cmake --install $BuildDir --prefix $Prefix
if ($LASTEXITCODE -ne 0) { throw "cmake install failed" }

# MinGW looks for lib*.a / *.dll.a. Also copy DLLs into prefix/bin.
$LibDir = Join-Path $Prefix "lib"
$BinDir = Join-Path $Prefix "bin"
$IncDir = Join-Path $Prefix "include"
New-Item -ItemType Directory -Force -Path $LibDir, $BinDir | Out-Null

Get-ChildItem -Path $BuildDir -Recurse -Include *.dll, *.dll.a, *.a, *.lib |
    ForEach-Object {
        if ($_.Extension -eq ".dll") {
            Copy-Item $_.FullName $BinDir -Force
        } else {
            Copy-Item $_.FullName $LibDir -Force
        }
    }

if (-not (Test-Path (Join-Path $IncDir "whisper.h"))) {
    $srcInc = Join-Path $SourceDir "include\whisper.h"
    if (Test-Path $srcInc) {
        New-Item -ItemType Directory -Force -Path $IncDir | Out-Null
        Copy-Item $srcInc $IncDir -Force
    }
    $ggmlInc = Join-Path $SourceDir "ggml\include"
    if (Test-Path $ggmlInc) {
        Copy-Item (Join-Path $ggmlInc "*") $IncDir -Force -Recurse
    }
}

Write-Host ""
Write-Host "whisper.cpp Vulkan install: $Prefix"
Write-Host "  include: $IncDir"
Write-Host "  lib:     $LibDir"
Write-Host "  bin:     $BinDir"
Write-Host ""
Write-Host "Next: .\scripts\build-windows.ps1"
Write-Host "Vulkan log marker (after vole transcribe / daemon): ggml_vulkan:"
