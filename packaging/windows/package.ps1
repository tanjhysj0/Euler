<#
.SYNOPSIS
  构建 Windows MSI 安装器，并在提供证书时执行代码签名。

.DESCRIPTION
  需要 WiX v4（dotnet tool install --global wix）。
  可通过环境变量提供签名材料：
    WINDOWS_SIGN_PFX        .pfx 证书的 base64 内容
    WINDOWS_SIGN_PASSWORD   .pfx 密码
    WINDOWS_TIMESTAMP_URL   时间戳服务（默认 http://timestamp.digicert.com）

.EXAMPLE
  ./packaging/windows/package.ps1 -Version 1.2.3 -Arch amd64
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][ValidateSet('amd64', 'arm64')][string]$Arch,
    [string]$ExePath = 'bin\euler.exe',
    [string]$OutDir = 'packaging\out'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
Set-Location $repoRoot

# MSI 的 ProductVersion 只接受点分数字，去掉 v 前缀与预发布后缀。
$normalized = $Version -replace '^v', ''
$msiVersion = ($normalized -split '-')[0]
if ($msiVersion -notmatch '^\d+(\.\d+){0,3}$') {
    throw "版本号 $Version 无法转换为 MSI 版本（需要 1-4 段数字）。"
}

if (-not (Get-Command wix -ErrorAction SilentlyContinue)) {
    throw '未找到 wix 命令；请先执行 dotnet tool install --global wix。'
}

$exeFull = (Resolve-Path -LiteralPath $ExePath).Path
$iconPath = (Resolve-Path -LiteralPath 'packaging\assets\euler.ico').Path
$wxsPath = Join-Path $PSScriptRoot 'euler.wxs'

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$outFull = (Resolve-Path -LiteralPath $OutDir).Path
$msiPath = Join-Path $outFull "euler_${msiVersion}_windows_${Arch}.msi"

$platform = 'x64'
if ($Arch -eq 'arm64') {
    $platform = 'arm64'
}

function Find-SignTool {
    $kitsBin = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
    if (-not (Test-Path -LiteralPath $kitsBin)) {
        return $null
    }
    Get-ChildItem -Path $kitsBin -Filter 'signtool.exe' -Recurse -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -match '\\x64\\signtool\.exe$' } |
        Sort-Object -Property FullName -Descending |
        Select-Object -First 1 -ExpandProperty FullName
}

$script:signTool = $null
$script:timestampUrl = 'http://timestamp.digicert.com'
$script:pfxPath = $null

function Invoke-SignFile {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not $script:signTool) {
        return
    }
    & $script:signTool sign /fd SHA256 /td SHA256 /tr $script:timestampUrl `
        /f $script:pfxPath /p $env:WINDOWS_SIGN_PASSWORD $Path
    if ($LASTEXITCODE -ne 0) {
        throw "代码签名失败：$Path"
    }
}

$script:pfxPath = $null
if ($env:WINDOWS_SIGN_PFX) {
    if (-not $env:WINDOWS_SIGN_PASSWORD) {
        throw '设置了 WINDOWS_SIGN_PFX 但没有 WINDOWS_SIGN_PASSWORD。'
    }
    $script:signTool = Find-SignTool
    if (-not $script:signTool) {
        throw '未找到 signtool.exe，请确认 Windows SDK 已安装。'
    }
    if ($env:WINDOWS_TIMESTAMP_URL) {
        $script:timestampUrl = $env:WINDOWS_TIMESTAMP_URL
    }
    $script:pfxPath = Join-Path ([IO.Path]::GetTempPath()) `
        ("euler-sign-" + [guid]::NewGuid().ToString('N') + '.pfx')
    [IO.File]::WriteAllBytes($script:pfxPath, [Convert]::FromBase64String($env:WINDOWS_SIGN_PFX))
}

try {
    if ($script:pfxPath) {
        # 先签名可执行文件，确保 MSI 内嵌的是已签名版本。
        Invoke-SignFile -Path $exeFull
    }

    & wix build $wxsPath -arch $platform -d "Version=$msiVersion" `
        -d "ExePath=$exeFull" -d "IconPath=$iconPath" -o $msiPath
    if ($LASTEXITCODE -ne 0) {
        throw "wix build 失败（退出码 $LASTEXITCODE）。"
    }

    if ($script:pfxPath) {
        Invoke-SignFile -Path $msiPath
    }
}
finally {
    if ($script:pfxPath) {
        Remove-Item -LiteralPath $script:pfxPath -Force -ErrorAction SilentlyContinue
    }
}

Write-Host "完成：$msiPath"
