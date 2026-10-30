<#
.SYNOPSIS
  Remove the lsh-utproxy Windows service (and, with -Purge, its files).

.EXAMPLE
  .\deploy\uninstall-service.ps1

.EXAMPLE
  .\deploy\uninstall-service.ps1 -Purge
#>
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string] $InstallDir  = 'C:\Soft\lsh-utproxy',
    [string] $ServiceName = 'lsh-utproxy',
    [switch] $Purge
)

$ErrorActionPreference = 'Stop'

function Test-IsAdmin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal $id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if (-not (Test-IsAdmin)) {
    Write-Error 'This script must run from an elevated PowerShell (Run as administrator).'
}

$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($svc) {
    if ($svc.Status -eq 'Running') {
        Write-Host "stopping service '$ServiceName'"
        Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 2
    }
    if ($PSCmdlet.ShouldProcess($ServiceName, 'delete service')) {
        & sc.exe delete $ServiceName | Out-Null
        Write-Host "deleted service '$ServiceName'"
    }
} else {
    Write-Host "service '$ServiceName' is not installed"
}

foreach ($rule in (Get-NetFirewallRule -DisplayName "$ServiceName-in-*" -ErrorAction SilentlyContinue)) {
    if ($PSCmdlet.ShouldProcess($rule.DisplayName, 'remove firewall rule')) {
        Remove-NetFirewallRule -DisplayName $rule.DisplayName
        Write-Host "firewall rule removed: $($rule.DisplayName)"
    }
}

if ($PSCmdlet.ShouldProcess('Application event log', "remove source $ServiceName")) {
    try {
        Remove-EventLog -Source $ServiceName -ErrorAction Stop
        Write-Host "event log source '$ServiceName' removed"
    } catch {
        Write-Host "event log source '$ServiceName' was not registered"
    }
}

if ($Purge) {
    if (Test-Path -LiteralPath $InstallDir) {
        Write-Warning "about to delete everything under $InstallDir (config and logs included)"
        if ($PSCmdlet.ShouldProcess($InstallDir, 'remove directory')) {
            Remove-Item -LiteralPath $InstallDir -Recurse -Force
            Write-Host "removed $InstallDir"
        }
    }
} else {
    Write-Host "files kept in $InstallDir (config, logs). Add -Purge to delete them."
}
