# wsvpns/wsvpnc 单机 TUN 测试脚本
# 需要以管理员权限运行 PowerShell

param(
    [switch]$Cleanup
)

$ErrorActionPreference = "Stop"

if ($Cleanup) {
    Write-Host "Cleaning up..." -ForegroundColor Yellow
    Get-Process wsvpns, wsvpnc -ErrorAction SilentlyContinue | Stop-Process -Force
    route delete 10.66.0.0/24 2>$null
    Write-Host "Done." -ForegroundColor Green
    exit 0
}

# Check admin
$isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "ERROR: This script requires administrator privileges." -ForegroundColor Red
    Write-Host "Right-click PowerShell -> Run as Administrator, then re-run." -ForegroundColor Yellow
    exit 1
}

$ProjectDir = Split-Path -Parent $PSScriptRoot
if (-not $ProjectDir) { $ProjectDir = $PSScriptRoot }
$BinDir = Join-Path $ProjectDir "bin"
$ServerExe = Join-Path $BinDir "wsvpns.exe"
$ClientExe = Join-Path $BinDir "wsvpnc.exe"
$ServerCfg = Join-Path $ProjectDir "configs\local\server.yaml"
$ClientACfg = Join-Path $ProjectDir "configs\local\client-a.yaml"
$ClientBCfg = Join-Path $ProjectDir "configs\local\client-b.yaml"
$WintunDll = Join-Path $BinDir "wintun.dll"

# Verify files exist
foreach ($f in @($ServerExe, $ClientExe, $ServerCfg, $ClientACfg, $ClientBCfg, $WintunDll)) {
    if (-not (Test-Path $f)) {
        Write-Host "Missing: $f" -ForegroundColor Red
        exit 1
    }
}

Write-Host "=== wsvpns/wsvpnc TUN test ===" -ForegroundColor Cyan
Write-Host "Killing old processes..."
Get-Process wsvpns, wsvpnc -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 1

# Start server
Write-Host "Starting server..." -ForegroundColor Green
$serverProc = Start-Process -FilePath $ServerExe -ArgumentList "-c", $ServerCfg, "--log-level", "debug" -PassThru -NoNewWindow

Start-Sleep -Seconds 1

# Start client A
Write-Host "Starting client A (wsvpn0, expects VIP 10.66.0.2)..." -ForegroundColor Green
$clientAProc = Start-Process -FilePath $ClientExe -ArgumentList "-c", $ClientACfg, "--log-level", "debug" -PassThru -NoNewWindow

Start-Sleep -Seconds 3

# Check if client A is still running (TUN created OK?)
if ($clientAProc.HasExited) {
    Write-Host "Client A exited early! Check output above." -ForegroundColor Red
    $serverProc | Stop-Process -Force
    exit 1
}

Write-Host "Client A running (PID: $($clientAProc.Id))" -ForegroundColor Green

# Start client B
Write-Host "Starting client B (wsvpn1, expects VIP 10.66.0.3)..." -ForegroundColor Green
$clientBProc = Start-Process -FilePath $ClientExe -ArgumentList "-c", $ClientBCfg, "--log-level", "debug" -PassThru -NoNewWindow

Start-Sleep -Seconds 3

if ($clientBProc.HasExited) {
    Write-Host "Client B exited early! Check output above." -ForegroundColor Red
    $clientAProc | Stop-Process -Force
    $serverProc | Stop-Process -Force
    exit 1
}

Write-Host "Client B running (PID: $($clientBProc.Id))" -ForegroundColor Green

# Show network interfaces
Write-Host "`n=== Network interfaces ===" -ForegroundColor Cyan
Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -like "10.66.*" } | Format-Table IPAddress, InterfaceAlias, PrefixLength

# Ping test
Write-Host "=== Ping test: from 10.66.0.2 to 10.66.0.3 ===" -ForegroundColor Cyan
ping -S 10.66.0.2 10.66.0.3 -n 4

Write-Host "`n=== Ping test: from 10.66.0.3 to 10.66.0.2 ===" -ForegroundColor Cyan
ping -S 10.66.0.3 10.66.0.2 -n 4

# Cleanup
Write-Host "`n=== Cleanup ===" -ForegroundColor Yellow
$clientAProc | Stop-Process -Force -ErrorAction SilentlyContinue
$clientBProc | Stop-Process -Force -ErrorAction SilentlyContinue
$serverProc | Stop-Process -Force -ErrorAction SilentlyContinue

Write-Host "Done." -ForegroundColor Green
