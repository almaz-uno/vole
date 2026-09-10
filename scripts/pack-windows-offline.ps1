# Assemble an offline Windows install package under .local/vole-windows-offline.
# Does not rebuild vole.exe — copies the existing .bin payload, local models,
# and the LunarG Vulkan runtime installer.

[CmdletBinding()]
param(
    [string]$OutDir,
    [string]$BinDir,
    [string]$ModelsDir,
    [string]$VulkanRT
)

$ErrorActionPreference = "Stop"

# The runtime installer ships with the LunarG SDK; take $env:VULKAN_SDK when the
# SDK is on PATH, else the newest C:\VulkanSDK\<version> present.
function Find-VulkanRT {
    if ($env:VULKAN_SDK) {
        $p = Join-Path $env:VULKAN_SDK "Helpers\VulkanRT.exe"
        if (Test-Path $p) { return $p }
    }
    $found = Get-ChildItem "C:\VulkanSDK\*\Helpers\VulkanRT.exe" -ErrorAction SilentlyContinue |
        Sort-Object { [version]$_.Directory.Parent.Name } -ErrorAction SilentlyContinue |
        Select-Object -Last 1
    if ($found) { return $found.FullName }
    return "C:\VulkanSDK\<version>\Helpers\VulkanRT.exe"
}

$Here = $PSScriptRoot
if (-not $Here) { $Here = Split-Path -Parent $MyInvocation.MyCommand.Path }
if (-not $Here) { $Here = (Get-Location).Path }
$RepoRoot = [IO.Path]::GetFullPath((Join-Path $Here ".."))
if (-not $OutDir) { $OutDir = Join-Path $RepoRoot ".local\vole-windows-offline" }
if (-not $BinDir) { $BinDir = Join-Path $RepoRoot ".bin" }
if (-not $ModelsDir) { $ModelsDir = Join-Path $env:LOCALAPPDATA "vole\models" }
if (-not $VulkanRT) { $VulkanRT = Find-VulkanRT }
$OutDir = [IO.Path]::GetFullPath($OutDir)
$BinDir = [IO.Path]::GetFullPath($BinDir)

function Need-File($path, $hint) {
    if (-not (Test-Path $path)) { throw "Missing $path. $hint" }
}

Need-File (Join-Path $BinDir "vole.exe") "Use the existing Windows build in .bin (do not rebuild)."
Need-File (Join-Path $BinDir "ggml-vulkan.dll") "Runtime DLLs must sit next to vole.exe."
Need-File (Join-Path $BinDir "vulkan-1.dll") "Copy vulkan-1.dll next to vole.exe before packing."
Need-File (Join-Path $ModelsDir "ggml-large-v3.bin") "Models are read from %LOCALAPPDATA%\vole\models."
Need-File (Join-Path $ModelsDir "ggml-silero-v5.1.2.bin") "VAD model missing."
Need-File $VulkanRT "Vulkan runtime installer (VulkanRT.exe) from the LunarG SDK Helpers folder."
Need-File (Join-Path $Here "Install-Vole.ps1") "Installer script lives next to this packer."

$requiredDlls = @(
    "vole.exe",
    "libwhisper.dll",
    "ggml.dll",
    "ggml-base.dll",
    "ggml-cpu.dll",
    "ggml-vulkan.dll",
    "vulkan-1.dll",
    "libgcc_s_seh-1.dll",
    "libstdc++-6.dll",
    "libwinpthread-1.dll",
    "libgomp-1.dll"
)
foreach ($name in $requiredDlls) {
    Need-File (Join-Path $BinDir $name) "Required runtime file is not in .bin."
}

Write-Host "Packing offline installer -> $OutDir"
if (Test-Path $OutDir) {
    Write-Host "Removing previous package directory"
    Remove-Item $OutDir -Recurse -Force
}

$payloadBin = Join-Path $OutDir "payload\bin"
$payloadModels = Join-Path $OutDir "payload\models"
$payloadVulkan = Join-Path $OutDir "payload\vulkan"
New-Item -ItemType Directory -Force -Path $payloadBin, $payloadModels, $payloadVulkan | Out-Null

function Copy-Robo([string]$srcDir, [string]$dstDir, [string[]]$files) {
    New-Item -ItemType Directory -Force -Path $dstDir | Out-Null
    $roboArgs = @($srcDir, $dstDir) + $files + @("/NFL", "/NDL", "/NJH", "/NJS", "/nc", "/ns", "/np", "/R:2", "/W:2")
    & robocopy @roboArgs | Out-Null
    if ($LASTEXITCODE -ge 8) { throw "robocopy failed ($LASTEXITCODE): $srcDir -> $dstDir" }
}

Write-Host "Copying vole.exe and DLLs"
Copy-Robo $BinDir $payloadBin $requiredDlls

Write-Host "Copying whisper models (about 3 GB; this can take a few minutes)"
Copy-Robo $ModelsDir $payloadModels @("ggml-large-v3.bin", "ggml-silero-v5.1.2.bin")

Write-Host "Copying Vulkan runtime installer"
Copy-Item $VulkanRT (Join-Path $payloadVulkan "VulkanRT.exe") -Force

Copy-Item (Join-Path $Here "Install-Vole.ps1") (Join-Path $OutDir "Install-Vole.ps1") -Force

$readme = @"
vole offline Windows package
============================

Self-contained install: vole.exe, GPU whisper DLLs, Vulkan runtime, and
ggml models. Nothing is downloaded.

On the target PC (Windows 10/11, microphone, GPU with a vendor Vulkan driver):

  1. Copy this whole folder (USB / share). Do not run from a network path
     if Windows blocks scripts.
  2. Right-click Install-Vole.ps1 -> Run with PowerShell
     or:  powershell -ExecutionPolicy Bypass -File .\Install-Vole.ps1

The installer:

  - Installs the bundled Vulkan runtime (VulkanRT.exe /S) if vulkan-1.dll
    is not already in System32. Admin approval may be required for that step.
  - Copies vole next to its DLLs (default: %LOCALAPPDATA%\Programs\vole).
  - Copies models to %LOCALAPPDATA%\vole\models.
  - Asks for the push-to-talk hotkey. Defaults are shown; press Enter to keep
    them or type new values:
      Control+Alt+D        Russian
      Control+Alt+Shift+D  English
  - Registers a per-user Task Scheduler task "vole" (At log on).
  - Starts the daemon.

This is a tray app, not a Windows Service. A GPU vendor driver with Vulkan
support must already be installed (Intel / NVIDIA / AMD). The package ships
the Vulkan *loader* (runtime), not a GPU driver.

Unattended (keep defaults):

  powershell -ExecutionPolicy Bypass -File .\Install-Vole.ps1 -NonInteractive
"@
Set-Content -Path (Join-Path $OutDir "README.txt") -Value $readme -Encoding utf8

$manifest = [ordered]@{
    packed_at     = (Get-Date).ToString("o")
    source_bin    = $BinDir
    vole_exe      = (Get-Item (Join-Path $BinDir "vole.exe")).Length
    vulkan_rt     = (Get-Item $VulkanRT).Length
    model         = (Get-Item (Join-Path $ModelsDir "ggml-large-v3.bin")).Length
    vad           = (Get-Item (Join-Path $ModelsDir "ggml-silero-v5.1.2.bin")).Length
}
$manifest | ConvertTo-Json | Set-Content (Join-Path $OutDir "manifest.json") -Encoding utf8

Write-Host ""
Write-Host "Package ready: $OutDir"
Get-ChildItem $OutDir -Recurse -File | Sort-Object FullName | ForEach-Object {
    "{0,12:N0}  {1}" -f $_.Length, $_.FullName.Substring($OutDir.Length + 1)
} | Write-Host
exit 0
