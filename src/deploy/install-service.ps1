<#
.SYNOPSIS
  Install (or update) lsh-utproxy as a Windows service.

.DESCRIPTION
  Creates C:\Soft\lsh-utproxy, copies the binary, writes a starter config if
  none exists, registers the "lsh-utproxy" event log source, creates the service
  with sc.exe, configures automatic restart on failure and optionally opens the
  firewall ports and starts the service.

  Run from an elevated PowerShell. Add -WhatIf to see what would happen.

.EXAMPLE
  .\deploy\install-service.ps1 -ExePath .\dist\lsh-utproxy-windows-amd64.exe -TcpPorts 33390 -UdpPorts 5353 -StartService

.EXAMPLE
  .\deploy\install-service.ps1 -ExePath .\lsh-utproxy-windows-amd64.exe -Update -StartService
#>
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]   $InstallDir  = 'C:\Soft\lsh-utproxy',
    [Parameter(Mandatory = $true)]
    [string]   $ExePath,
    [string]   $ServiceName = 'lsh-utproxy',
    [string]   $DisplayName = 'lsh-utproxy TCP/UDP proxy',
    [string]   $Description = 'TCP/UDP proxy with a HAProxy-like configuration.',
    [string]   $ServiceAccount = 'LocalSystem',
    [int[]]    $TcpPorts = @(),
    [int[]]    $UdpPorts = @(),
    [switch]   $Update,
    [switch]   $StartService
)

$ErrorActionPreference = 'Stop'

function Test-IsAdmin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal $id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-ServiceState([string]$name) {
    (Get-CimInstance -ClassName Win32_Service -Filter "Name='$name'" -ErrorAction SilentlyContinue).State
}

if (-not (Test-IsAdmin)) {
    Write-Error 'This script must run from an elevated PowerShell (Run as administrator).'
}
$ExePath = (Resolve-Path -LiteralPath $ExePath).Path
if (-not (Test-Path -LiteralPath $ExePath)) {
    Write-Error "binary not found: $ExePath"
}

$binDir    = Join-Path $InstallDir 'bin'
$confDir   = Join-Path $InstallDir 'config'
$logDir    = Join-Path $InstallDir 'logs'
$targetExe = Join-Path $binDir "$ServiceName.exe"
$confFile  = Join-Path $confDir 'lsh-utproxy.cfg'

if ($PSCmdlet.ShouldProcess($InstallDir, 'create directories')) {
    foreach ($d in @($InstallDir, $binDir, $confDir, $logDir)) {
        if (-not (Test-Path -LiteralPath $d)) {
            New-Item -ItemType Directory -Path $d -Force | Out-Null
            Write-Host "created $d"
        }
    }
}

$state = Get-ServiceState $ServiceName
if ($state -eq 'Running' -and -not $Update) {
    Write-Error "service '$ServiceName' is already running; use -Update to replace the binary"
}
if ($state -eq 'Running' -and $Update) {
    Write-Host "stopping service '$ServiceName' for update"
    Stop-Service -Name $ServiceName -Force
}

if ($PSCmdlet.ShouldProcess($targetExe, 'copy binary')) {
    Copy-Item -LiteralPath $ExePath -Destination $targetExe -Force
    Write-Host "installed $targetExe"
}

if ($PSCmdlet.ShouldProcess($confFile, 'create starter config')) {
    if (-not (Test-Path -LiteralPath $confFile)) {
        @'
global
  log C:\Soft\lsh-utproxy\logs\lsh-utproxy.log format json level info
  maxconn 20000
  grace shutdown 10s

defaults
  mode tcp
  balance roundrobin
  timeout connect 5s
  timeout client 30s
  timeout server 30s
  timeout tunnel 3600s
  option tcpkeepalive

# Edit or replace the block below with your own listeners.
listen rdp_example
  mode tcp
  bind 0.0.0.0:33390
  balance roundrobin
  timeout connect 5s
  timeout tunnel 3600s
  server rdp1 127.0.0.1:3389 check inter 10s fall 3 rise 2
'@ | Set-Content -LiteralPath $confFile -Encoding UTF8
        Write-Host "wrote starter config $confFile (edit it before starting)"
    } else {
        Write-Host "config already exists: $confFile"
    }
}

if ($PSCmdlet.ShouldProcess('Application event log', "register source $ServiceName")) {
    try {
        New-EventLog -LogName Application -Source $ServiceName -ErrorAction Stop
        Write-Host "registered event log source '$ServiceName'"
    } catch {
        Write-Host "event log source '$ServiceName' already registered"
    }
}

if ($PSCmdlet.ShouldProcess($ServiceName, 'create service')) {
    if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
        Write-Host "service '$ServiceName' already exists, updating its binary path"
        & sc.exe config $ServiceName binPath= "`"$targetExe`" -service"
    } else {
        & sc.exe create $ServiceName binPath= "`"$targetExe`" -service" start= auto DisplayName= "$DisplayName" | Out-Null
        & sc.exe description $ServiceName "$Description" | Out-Null
        Write-Host "created service '$ServiceName'"
    }
    # Automatic restart: 5s, 10s, then 60s, with the counter reset after a day.
    & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/10000/restart/60000 | Out-Null
    if ($ServiceAccount -ne 'LocalSystem') {
        & sc.exe obj $ServiceName $ServiceAccount $null | Out-Null
    }
}

foreach ($port in $TcpPorts) {
    $rule = "$ServiceName-in-tcp-$port"
    if ($PSCmdlet.ShouldProcess($rule, 'create inbound firewall rule')) {
        if (-not (Get-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue)) {
            New-NetFirewallRule -DisplayName $rule -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port | Out-Null
            Write-Host "firewall: allow TCP $port"
        }
    }
}
foreach ($port in $UdpPorts) {
    $rule = "$ServiceName-in-udp-$port"
    if ($PSCmdlet.ShouldProcess($rule, 'create inbound firewall rule')) {
        if (-not (Get-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue)) {
            New-NetFirewallRule -DisplayName $rule -Direction Inbound -Action Allow -Protocol UDP -LocalPort $port | Out-Null
            Write-Host "firewall: allow UDP $port"
        }
    }
}

if ($StartService) {
    if ($PSCmdlet.ShouldProcess($ServiceName, 'start service')) {
        Start-Service -Name $ServiceName
        Start-Sleep -Seconds 2
        Get-Service -Name $ServiceName | Format-Table -AutoSize Name, Status, StartType
        Write-Host "logs: $logDir"
    }
} else {
    Write-Host ''
    Write-Host "Next steps:"
    Write-Host "  1. edit $confFile"
    Write-Host "  2. & `"$targetExe`" -c `"$confFile`" -t     # check the config"
    Write-Host "  3. Start-Service $ServiceName"
}
