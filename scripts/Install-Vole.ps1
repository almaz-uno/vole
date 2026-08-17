# Offline vole installer for Windows.
# Expects this layout next to the script (produced by pack-windows-offline.ps1):
#   payload\bin\       vole.exe + runtime DLLs
#   payload\models\    ggml-large-v3.bin, ggml-silero-v5.1.2.bin
#   payload\vulkan\    VulkanRT.exe (LunarG runtime installer)
#
# Run:  powershell -ExecutionPolicy Bypass -File .\Install-Vole.ps1

[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\vole"),
    [switch]$NonInteractive,
    [string]$Mods,
    [string]$Key,
    [string]$Lang,
    [string]$LangShift
)

$ErrorActionPreference = "Stop"
$Root = $PSScriptRoot
if (-not $Root) { $Root = Split-Path -Parent $MyInvocation.MyCommand.Path }

$PayloadBin = Join-Path $Root "payload\bin"
$PayloadModels = Join-Path $Root "payload\models"
$PayloadVulkan = Join-Path $Root "payload\vulkan"
$DefaultMods = "Control+Alt"
$DefaultKey = "d"
$DefaultLang = "ru"
$DefaultLangShift = "en"
$TaskName = "vole"

function Write-Step($msg) {
    Write-Host ""
    Write-Host "=== $msg ===" -ForegroundColor Cyan
}

function Read-Default([string]$label, [string]$current) {
    if ($NonInteractive) { return $current }
    $answer = Read-Host "$label [$current]"
    if ([string]::IsNullOrWhiteSpace($answer)) { return $current }
    return $answer.Trim()
}

function Get-ExistingHotkey {
    $cfgPath = Join-Path $env:APPDATA "vole\config.yaml"
    $vals = @{
        Mods      = $DefaultMods
        Key       = $DefaultKey
        Lang      = $DefaultLang
        LangShift = $DefaultLangShift
    }
    if (-not (Test-Path $cfgPath)) { return $vals }
    $text = Get-Content $cfgPath -Raw -ErrorAction SilentlyContinue
    if (-not $text) { return $vals }
    if ($text -match '(?m)^\s*mods:\s*(\S+)') { $vals.Mods = $Matches[1] }
    if ($text -match '(?m)^\s*key:\s*(\S+)') { $vals.Key = $Matches[1] }
    if ($text -match '(?m)^\s*lang:\s*(\S+)') { $vals.Lang = $Matches[1] }
    if ($text -match '(?m)^\s*lang_shift:\s*(\S+)') { $vals.LangShift = $Matches[1] }
    return $vals
}

function Test-HotkeyMods([string]$spec) {
    $mask = 0
    foreach ($part in ($spec -split '\+')) {
        switch ($part.Trim().ToLowerInvariant()) {
            { $_ -in @('', 'none') } { }
            { $_ -in @('ctrl', 'control') } { $mask = $mask -bor 1 }
            { $_ -in @('alt', 'mod1') } { $mask = $mask -bor 2 }
            'shift' { $mask = $mask -bor 4 }
            { $_ -in @('super', 'mod4', 'win') } { $mask = $mask -bor 8 }
            default { throw "Unknown modifier '$part'. Use Control, Alt, Shift, Win (joined with +)." }
        }
    }
    if ($mask -eq 0) { throw "At least one modifier is required (Windows cannot observe a modifier-free global hotkey)." }
}

function Test-HotkeyKey([string]$spec) {
    $s = $spec.Trim()
    if ($s -match '^[A-Za-z0-9]$') { return }
    if ($s.ToLowerInvariant() -in @('space', 'pause')) { return }
    throw "Key must be a Latin letter, a digit, space, or pause."
}

function Copy-Tree([string]$src, [string]$dst) {
    New-Item -ItemType Directory -Force -Path $dst | Out-Null
    $rc = & robocopy $src $dst /E /NFL /NDL /NJH /NJS /nc /ns /np /R:2 /W:2
    $code = $LASTEXITCODE
    if ($code -ge 8) { throw "robocopy failed ($code): $src -> $dst" }
}

function Install-VulkanRuntime {
    $loader = Join-Path $env:WINDIR "System32\vulkan-1.dll"
    $rt = Join-Path $PayloadVulkan "VulkanRT.exe"
    if (-not (Test-Path $rt)) { throw "Bundled Vulkan runtime installer not found: $rt" }

    if (Test-Path $loader) {
        Write-Host "Vulkan loader already present: $loader"
        Write-Host "Skipping VulkanRT.exe (still copying vulkan-1.dll next to vole.exe)."
        return
    }

    Write-Host "Installing bundled Vulkan runtime (silent). Administrator approval may be required."
    $p = Start-Process -FilePath $rt -ArgumentList "/S" -Wait -PassThru -Verb RunAs
    if ($p.ExitCode -ne 0) {
        throw "VulkanRT installer failed with exit code $($p.ExitCode)"
    }
    if (-not (Test-Path $loader)) {
        throw "VulkanRT installer finished but $loader is still missing."
    }
    Write-Host "Vulkan runtime installed."
}

function Register-VoleAutostart([string]$exe, [string]$workDir) {
    $action = New-ScheduledTaskAction -Execute $exe -Argument "daemon" -WorkingDirectory $workDir
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -DontStopOnIdleEnd `
        -StartWhenAvailable `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -MultipleInstances IgnoreNew
    $principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Principal $principal `
        -Description "vole voice dictation daemon (tray app; run at logon)" -Force | Out-Null
    Write-Host "Scheduled task '$TaskName' registered (At log on, current user)."
}

function Start-VoleDaemon([string]$exe, [string]$workDir) {
    $alive = $false
    try {
        $alive = [bool]([System.IO.Directory]::GetFiles('\\.\pipe\') | Where-Object { $_ -match '\\vole$' })
    } catch { $alive = $false }
    if ($alive) {
        Write-Host "Daemon already running (named pipe \\.\pipe\vole)."
        return
    }
    Start-Process -FilePath $exe -ArgumentList "daemon" -WorkingDirectory $workDir -WindowStyle Hidden | Out-Null
    $deadline = (Get-Date).AddSeconds(60)
    do {
        Start-Sleep -Milliseconds 300
        try {
            $alive = [bool]([System.IO.Directory]::GetFiles('\\.\pipe\') | Where-Object { $_ -match '\\vole$' })
        } catch { $alive = $false }
        if ($alive) { break }
    } while ((Get-Date) -lt $deadline)
    if (-not $alive) { throw "Daemon did not start (named pipe \\.\pipe\vole did not appear)." }
    Write-Host "Daemon started."
}

function Write-VoleConfig($hotkey, [string]$model, [string]$vad) {
    $dir = Join-Path $env:APPDATA "vole"
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $path = Join-Path $dir "config.yaml"
    $modelYaml = $model -replace '\\', '/'
    $vadYaml = $vad -replace '\\', '/'
    @"
# Written by Install-Vole.ps1. Restart the daemon after editing.
hotkey:
  mods: $($hotkey.Mods)
  key: $($hotkey.Key)
  lang: $($hotkey.Lang)
  lang_shift: $($hotkey.LangShift)
model: $modelYaml
vad: $vadYaml
"@ | Set-Content -Path $path -Encoding utf8
    Write-Host "Config written: $path"
}

foreach ($p in @($PayloadBin, $PayloadModels, $PayloadVulkan)) {
    if (-not (Test-Path $p)) { throw "Package payload missing: $p" }
}
if (-not (Test-Path (Join-Path $PayloadBin "vole.exe"))) { throw "payload\bin\vole.exe is missing" }

Write-Host "vole offline installer"
Write-Host "Installs vole, the Vulkan loader, and whisper models without network access."
Write-Host ""

$InstallDir = Read-Default "Install directory" $InstallDir
$InstallDir = [IO.Path]::GetFullPath($InstallDir)

Write-Step "Vulkan runtime"
Install-VulkanRuntime

Write-Step "Copy vole and models"
Write-Host "Binaries -> $InstallDir"
Copy-Tree $PayloadBin $InstallDir
$modelsDest = Join-Path $env:LOCALAPPDATA "vole\models"
Write-Host "Models   -> $modelsDest  (this can take a few minutes)"
Copy-Tree $PayloadModels $modelsDest

$exe = Join-Path $InstallDir "vole.exe"
$model = Join-Path $modelsDest "ggml-large-v3.bin"
$vad = Join-Path $modelsDest "ggml-silero-v5.1.2.bin"
foreach ($f in @($exe, $model, $vad, (Join-Path $InstallDir "vulkan-1.dll"), (Join-Path $InstallDir "ggml-vulkan.dll"))) {
    if (-not (Test-Path $f)) { throw "Expected file missing after copy: $f" }
}

Write-Step "Activation hotkey"
$existing = Get-ExistingHotkey
if (-not $Mods) { $Mods = $existing.Mods }
if (-not $Key) { $Key = $existing.Key }
if (-not $Lang) { $Lang = $existing.Lang }
if (-not $LangShift) { $LangShift = $existing.LangShift }

Write-Host "Windows default is Control+Alt+D (Russian) / Control+Alt+Shift+D (English)."
Write-Host "Values below are the current defaults for this install. Press Enter to keep a value."
Write-Host "Avoid Win+Ctrl+D (virtual desktops) and Win+H (Windows voice typing)."
Write-Host ""
$Mods = Read-Default "Modifiers (e.g. Control+Alt)" $Mods
$Key = Read-Default "Key (letter/digit)" $Key
$Lang = Read-Default "Language without Shift" $Lang
$LangShift = Read-Default "Language with Shift" $LangShift
Test-HotkeyMods $Mods
Test-HotkeyKey $Key
Write-Host ""
Write-Host "Will bind:"
Write-Host "  Hold $Mods+$Key          -> $Lang"
Write-Host "  Hold $Mods+Shift+$Key    -> $LangShift"

$hotkey = @{ Mods = $Mods; Key = $Key; Lang = $Lang; LangShift = $LangShift }
Write-VoleConfig $hotkey $model $vad

Write-Step "Autostart (logon)"
Register-VoleAutostart $exe $InstallDir

Write-Step "Start daemon"
Start-VoleDaemon $exe $InstallDir

Write-Host ""
Write-Host "Done. Hold $Mods+$Key and speak; release to type the transcript." -ForegroundColor Green
Write-Host "Tray icon: notification area (it may sit in the overflow ^). Overlay: top of the screen."
