$ErrorActionPreference = 'Stop'

$script:RootDir = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Set-Location -LiteralPath $script:RootDir

$script:EulerPort = $env:EULER_PORT
if ([string]::IsNullOrEmpty($script:EulerPort)) {
    $script:EulerPort = '8080'
}
$script:WebPort = $env:WEB_PORT
if ([string]::IsNullOrEmpty($script:WebPort)) {
    $script:WebPort = '5173'
}
$script:EulerConfig = $env:EULER_CONFIG
$script:DatabaseDriver = $env:EULER_DATABASE_DRIVER
if ([string]::IsNullOrEmpty($script:DatabaseDriver)) {
    $script:DatabaseDriver = 'sqlite'
}
$script:UserHome = $env:USERPROFILE
if ([string]::IsNullOrEmpty($script:UserHome)) {
    $script:UserHome = [Environment]::GetFolderPath('UserProfile')
}
if ([string]::IsNullOrEmpty($script:UserHome)) {
    $script:UserHome = $HOME
}
$script:DatabaseUrl = $env:EULER_DATABASE_URL
if ([string]::IsNullOrEmpty($script:DatabaseUrl)) {
    if ($script:DatabaseDriver -eq 'postgres') {
        $script:DatabaseUrl = 'postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable'
    } else {
        $script:DatabaseUrl = Join-Path $script:UserHome '.euler\euler.db'
    }
}
$env:EULER_DATABASE_DRIVER = $script:DatabaseDriver
$env:EULER_DATABASE_URL = $script:DatabaseUrl

if (-not [string]::IsNullOrEmpty($env:EULER_DEV_STATE_DIR)) {
    $script:StateDir = $env:EULER_DEV_STATE_DIR
} elseif (-not [string]::IsNullOrEmpty($env:LOCALAPPDATA)) {
    $script:StateDir = Join-Path $env:LOCALAPPDATA 'Euler\dev'
} else {
    $script:StateDir = Join-Path $script:UserHome 'Euler\dev'
}
$script:ApiBinary = Join-Path $script:StateDir 'euler-dev.exe'
$script:ApiPidFile = Join-Path $script:StateDir 'euler-dev.pid'
$script:ApiStartFile = Join-Path $script:StateDir 'euler-dev.start'
$script:ApiOutputLog = Join-Path $script:StateDir 'euler-dev.out.log'
$script:ApiErrorLog = Join-Path $script:StateDir 'euler-dev.err.log'
$script:WebPidFile = Join-Path $script:StateDir 'euler-web.pid'
$script:WebStartFile = Join-Path $script:StateDir 'euler-web.start'
$script:WebOutputLog = Join-Path $script:StateDir 'euler-web.out.log'
$script:WebErrorLog = Join-Path $script:StateDir 'euler-web.err.log'
$script:ViteScript = Join-Path $script:RootDir 'web\node_modules\vite\bin\vite.js'

function Show-Usage {
    [Console]::Error.WriteLine('用法：scripts\dev.cmd <命令>')
    [Console]::Error.WriteLine('命令：check-env、test-smoke、api、restart-api、restart-web、stop-api、stop-web、')
    [Console]::Error.WriteLine('      reload、restart-dev、start、stop、restart')
}

function Assert-NativeSuccess {
    param([string]$Command)
    if ($LASTEXITCODE -ne 0) {
        throw "命令执行失败（退出码 $LASTEXITCODE）：$Command"
    }
}

function Test-Environment {
    $failed = $false
    $nodeVersion = $null
    $npmVersion = $null
    $goVersion = $null

    $nodeCommand = Get-Command node -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $nodeCommand) {
        [Console]::Error.WriteLine('未检测到 Node.js，请先安装 Node.js 20+ 并将 node 加入 PATH')
        $failed = $true
    } else {
        $nodeVersion = & node --version 2>$null
        if ($LASTEXITCODE -ne 0) {
            [Console]::Error.WriteLine('无法执行 node --version，请确认已正确安装 Node.js 20+')
            $failed = $true
        } else {
            $nodeMajorText = & node -p 'process.versions.node.split(".")[0]' 2>$null
            $nodeMajorExitCode = $LASTEXITCODE
            $nodeMajor = 0
            if ($nodeMajorExitCode -ne 0 -or
                -not [int]::TryParse("$nodeMajorText".Trim(), [ref]$nodeMajor) -or
                $nodeMajor -lt 20) {
                [Console]::Error.WriteLine("Node.js 版本过低或无法识别（当前 $nodeVersion），请先升级到 Node.js 20+")
                $failed = $true
            }
        }
    }

    $npmCommand = Get-Command npm -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $npmCommand) {
        [Console]::Error.WriteLine('未检测到 npm，请先安装 npm 并加入 PATH（通常随 Node.js 一起安装）')
        $failed = $true
    } else {
        $npmVersion = & npm --version 2>$null
        if ($LASTEXITCODE -ne 0) {
            [Console]::Error.WriteLine('无法执行 npm --version，请确认已正确安装 npm')
            $failed = $true
        }
    }

    $goCommand = Get-Command go -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $goCommand) {
        [Console]::Error.WriteLine('未检测到 Go，请先安装 Go 1.26+ 并将 go 加入 PATH')
        $failed = $true
    } else {
        $goVersion = & go version 2>$null
        if ($LASTEXITCODE -ne 0) {
            [Console]::Error.WriteLine('无法执行 go version，请确认已正确安装 Go 1.26+')
            $failed = $true
        }
    }

    if ($failed) {
        return $false
    }
    Write-Host "环境检查通过：Node.js $nodeVersion，npm $npmVersion，$goVersion"
    return $true
}

function Remove-DevMetadata {
    param(
        [string]$Name,
        [string]$PidFile,
        [string]$StartFile
    )
    try {
        foreach ($path in @($PidFile, $StartFile)) {
            if (Test-Path -LiteralPath $path -PathType Leaf) {
                Remove-Item -LiteralPath $path -Force -ErrorAction Stop
            }
        }
    } catch {
        throw "清理${Name}的进程元数据失败：$($_.Exception.Message)"
    }
}

function Get-ProcessStartToken {
    param([System.Diagnostics.Process]$Process)
    return $Process.StartTime.ToUniversalTime().Ticks.ToString(
        [Globalization.CultureInfo]::InvariantCulture)
}

function Wait-ProcessExit {
    param([int]$ProcessId)
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($null -eq (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue)) {
            return $true
        }
        Start-Sleep -Milliseconds 200
    }
    return ($null -eq (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue))
}

function Invoke-ProcessTermination {
    param(
        [string]$Name,
        [int]$ProcessId
    )
    try {
        Stop-Process -Id $ProcessId -ErrorAction Stop
    } catch {
        # 进程可能在检查后自行退出；仍在运行时保留元数据并报告失败。
        if ($null -ne (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue)) {
            throw "无法停止${Name}进程（PID $ProcessId）：$($_.Exception.Message)"
        }
    }
    if (-not (Wait-ProcessExit -ProcessId $ProcessId)) {
        throw "${Name}进程在停止信号发出后 10 秒内未退出（PID $ProcessId）"
    }
}

function Stop-DevProcess {
    param(
        [string]$Name,
        [string]$PidFile,
        [string]$StartFile
    )

    if (-not (Test-Path -LiteralPath $PidFile -PathType Leaf)) {
        if (Test-Path -LiteralPath $StartFile -PathType Leaf) {
            Write-Host "${Name}的 PID 文件缺失，仅清理陈旧启动身份文件"
            Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
        } else {
            Write-Host "${Name}未运行（PID 文件不存在）"
        }
        return
    }
    $pidText = Get-Content -LiteralPath $PidFile -TotalCount 1
    $processId = 0
    if (-not [int]::TryParse("$pidText".Trim(), [ref]$processId) -or $processId -le 0) {
        [Console]::Error.WriteLine("${Name}的 PID 文件内容无效，未停止任何进程，仅清理陈旧元数据")
        Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
        return
    }
    if (-not (Test-Path -LiteralPath $StartFile -PathType Leaf)) {
        [Console]::Error.WriteLine("${Name}缺少启动身份文件，未停止 PID $processId，仅清理陈旧元数据")
        Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
        return
    }
    $expectedToken = Get-Content -LiteralPath $StartFile -TotalCount 1
    if ([string]::IsNullOrWhiteSpace("$expectedToken")) {
        [Console]::Error.WriteLine("${Name}的启动身份文件无效，未停止 PID $processId，仅清理陈旧元数据")
        Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
        return
    }
    $process = Get-Process -Id $processId -ErrorAction SilentlyContinue
    if ($null -eq $process) {
        Write-Host "${Name}进程已退出（PID $processId），清理元数据"
        Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
        return
    }
    try {
        $actualToken = Get-ProcessStartToken -Process $process
    } catch {
        throw "无法读取${Name}进程的启动时间，未停止 PID $processId 并保留元数据：$($_.Exception.Message)"
    }
    if ($actualToken -cne "$expectedToken".Trim()) {
        [Console]::Error.WriteLine("${Name}的 PID $processId 启动身份不匹配，可能已复用；未停止该进程，仅清理陈旧元数据")
        Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
        return
    }
    try {
        Invoke-ProcessTermination -Name $Name -ProcessId $processId
    } catch {
        throw "${Name}停止失败，保留 PID 与启动身份文件：$($_.Exception.Message)"
    }
    Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
    Write-Host "已停止$Name（PID $processId）"
}

function Stop-DevApi {
    Stop-DevProcess -Name '后端' -PidFile $script:ApiPidFile -StartFile $script:ApiStartFile
}

function Stop-DevWeb {
    Stop-DevProcess -Name '前端' -PidFile $script:WebPidFile -StartFile $script:WebStartFile
}

function Stop-DevAll {
    $failed = $false
    try {
        Stop-DevWeb
    } catch {
        [Console]::Error.WriteLine("前端停止失败：$($_.Exception.Message)")
        $failed = $true
    }
    try {
        Stop-DevApi
    } catch {
        [Console]::Error.WriteLine("后端停止失败：$($_.Exception.Message)")
        $failed = $true
    }
    if ($failed) {
        throw '部分开发进程停止失败，请根据上述提示处理'
    }
}

function Assert-PortFree {
    param(
        [string]$Name,
        [string]$Port
    )
    $portNumber = [int]$Port
    $listener = Get-NetTCPConnection -State Listen -LocalPort $portNumber -ErrorAction SilentlyContinue
    if ($null -ne $listener) {
        throw "${Name}启动前检测到端口 $Port 已有监听者，未启动新进程"
    }
}

function Write-ProcessMetadata {
    param(
        [int]$ProcessId,
        [string]$StartToken,
        [string]$PidFile,
        [string]$StartFile
    )
    Set-Content -LiteralPath $StartFile -Value $StartToken -Encoding Ascii -ErrorAction Stop
    Set-Content -LiteralPath $PidFile -Value $ProcessId -Encoding Ascii -ErrorAction Stop
}

function Register-DevProcess {
    param(
        [string]$Name,
        [System.Diagnostics.Process]$Process,
        [string]$PidFile,
        [string]$StartFile
    )
    try {
        $startToken = Get-ProcessStartToken -Process $Process
        Write-ProcessMetadata -ProcessId $Process.Id -StartToken $startToken `
            -PidFile $PidFile -StartFile $StartFile
    } catch {
        $reason = $_.Exception.Message
        [Console]::Error.WriteLine("写入${Name}的 PID 或启动身份文件失败，将清理新进程（PID $($Process.Id)）")
        try {
            Invoke-ProcessTermination -Name $Name -ProcessId $Process.Id
        } catch {
            throw "记录${Name}进程元数据失败且新进程清理失败（PID $($Process.Id)）：$($_.Exception.Message)"
        }
        try {
            Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
        } catch {
            [Console]::Error.WriteLine($_.Exception.Message)
        }
        throw "记录${Name}进程元数据失败，新进程已停止：$reason"
    }
}

function Show-RecentLog {
    param([string]$LogFile)
    if (Test-Path -LiteralPath $LogFile -PathType Leaf) {
        $lines = Get-Content -LiteralPath $LogFile -Tail 20
        foreach ($line in $lines) {
            [Console]::Error.WriteLine($line)
        }
    }
}

function Show-ServiceLogs {
    param(
        [string]$OutputLog,
        [string]$ErrorLog
    )
    [Console]::Error.WriteLine("标准输出日志：$OutputLog")
    Show-RecentLog -LogFile $OutputLog
    [Console]::Error.WriteLine("标准错误日志：$ErrorLog")
    Show-RecentLog -LogFile $ErrorLog
}

function Wait-DevService {
    param(
        [string]$Name,
        [int]$ProcessId,
        [string]$Url,
        [string]$OutputLog,
        [string]$ErrorLog,
        [string]$PidFile,
        [string]$StartFile
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    while ([DateTime]::UtcNow -lt $deadline) {
        $process = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
        if ($null -eq $process) {
            [Console]::Error.WriteLine("${Name}进程提前退出（PID $ProcessId），最近日志：")
            Show-ServiceLogs -OutputLog $OutputLog -ErrorLog $ErrorLog
            Remove-DevMetadata -Name $Name -PidFile $PidFile -StartFile $StartFile
            throw "${Name}进程启动失败"
        }
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 1
            if ($response.StatusCode -eq 200 -and
                $null -ne (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue)) {
                Write-Host "${Name}已就绪 $Url，标准输出日志 $OutputLog，标准错误日志 $ErrorLog"
                return
            }
        } catch {
            # 服务尚未就绪时继续轮询，进程退出与绝对超时分别处理。
        }
        Start-Sleep -Seconds 1
    }
    [Console]::Error.WriteLine("${Name}启动超时（约 60 秒），最近日志：")
    Show-ServiceLogs -OutputLog $OutputLog -ErrorLog $ErrorLog
    try {
        Stop-DevProcess -Name $Name -PidFile $PidFile -StartFile $StartFile
    } catch {
        [Console]::Error.WriteLine("${Name}超时后的进程清理失败：$($_.Exception.Message)")
    }
    throw "${Name}启动超时"
}

function Invoke-DevApi {
    Assert-PortFree -Name '后端' -Port $script:EulerPort
    $arguments = @('run', './cmd/euler')
    if (-not [string]::IsNullOrEmpty($script:EulerConfig)) {
        $arguments += @('-config', $script:EulerConfig)
    }
    & go @arguments
    Assert-NativeSuccess 'go run ./cmd/euler'
}

function Restart-DevApi {
    New-Item -ItemType Directory -Path $script:StateDir -Force | Out-Null
    $tempBinary = Join-Path $script:StateDir ("euler-dev.tmp.{0}.exe" -f $PID)
    & go build -o $tempBinary ./cmd/euler
    $buildExitCode = $LASTEXITCODE
    if ($buildExitCode -ne 0) {
        if (Test-Path -LiteralPath $tempBinary -PathType Leaf) {
            Remove-Item -LiteralPath $tempBinary -Force -ErrorAction SilentlyContinue
        }
        throw "后端构建失败，旧服务保持运行（退出码 $buildExitCode）"
    }
    try {
        Stop-DevApi
        Assert-PortFree -Name '后端' -Port $script:EulerPort
        Move-Item -LiteralPath $tempBinary -Destination $script:ApiBinary -Force -ErrorAction Stop
    } catch {
        if (Test-Path -LiteralPath $tempBinary -PathType Leaf) {
            Remove-Item -LiteralPath $tempBinary -Force -ErrorAction SilentlyContinue
        }
        throw
    }

    $arguments = @()
    if (-not [string]::IsNullOrEmpty($script:EulerConfig)) {
        $arguments = @('-config', ('"{0}"' -f $script:EulerConfig))
    }
    $startParameters = @{
        FilePath = $script:ApiBinary
        WorkingDirectory = $script:RootDir
        RedirectStandardOutput = $script:ApiOutputLog
        RedirectStandardError = $script:ApiErrorLog
        PassThru = $true
        NoNewWindow = $true
    }
    if ($arguments.Count -gt 0) {
        $startParameters['ArgumentList'] = $arguments
    }
    $process = Start-Process @startParameters
    try {
        Register-DevProcess -Name '后端' -Process $process `
            -PidFile $script:ApiPidFile -StartFile $script:ApiStartFile
    } catch {
        Show-ServiceLogs -OutputLog $script:ApiOutputLog -ErrorLog $script:ApiErrorLog
        throw
    }
    Wait-DevService -Name '后端' -ProcessId $process.Id `
        -Url "http://127.0.0.1:$script:EulerPort/health" `
        -OutputLog $script:ApiOutputLog -ErrorLog $script:ApiErrorLog `
        -PidFile $script:ApiPidFile -StartFile $script:ApiStartFile
}

function Restart-DevWeb {
    Stop-DevWeb
    Assert-PortFree -Name '前端' -Port $script:WebPort
    New-Item -ItemType Directory -Path $script:StateDir -Force | Out-Null
    $nodePath = (Get-Command node -CommandType Application -ErrorAction Stop).Source
    $webRoot = Join-Path $script:RootDir 'web'
    $arguments = @(
        ('"{0}"' -f $script:ViteScript),
        ('"{0}"' -f $webRoot),
        '--host',
        '127.0.0.1',
        '--port',
        $script:WebPort,
        '--strictPort'
    )
    $process = Start-Process -FilePath $nodePath -ArgumentList $arguments `
        -WorkingDirectory $script:RootDir `
        -RedirectStandardOutput $script:WebOutputLog `
        -RedirectStandardError $script:WebErrorLog -PassThru -NoNewWindow
    try {
        Register-DevProcess -Name '前端' -Process $process `
            -PidFile $script:WebPidFile -StartFile $script:WebStartFile
    } catch {
        Show-ServiceLogs -OutputLog $script:WebOutputLog -ErrorLog $script:WebErrorLog
        throw
    }
    Wait-DevService -Name '前端' -ProcessId $process.Id `
        -Url "http://127.0.0.1:$script:WebPort/" `
        -OutputLog $script:WebOutputLog -ErrorLog $script:WebErrorLog `
        -PidFile $script:WebPidFile -StartFile $script:WebStartFile
}

if ($args.Count -ne 1) {
    Show-Usage
    exit 2
}
$command = [string]$args[0]

switch -CaseSensitive ($command) {
    'check-env' {
        if (-not (Test-Environment)) {
            exit 1
        }
    }
    'test-smoke' {
        if ([string]::IsNullOrEmpty($env:OPENAI_API_KEY)) {
            [Console]::Error.WriteLine('缺少 OPENAI_API_KEY，请先设置后再运行真实模型冒烟测试')
            exit 1
        }
        & npm --prefix web run test:smoke
        Assert-NativeSuccess 'npm --prefix web run test:smoke'
    }
    'api' {
        Invoke-DevApi
    }
    'restart-api' {
        Restart-DevApi
    }
    'restart-web' {
        Restart-DevWeb
    }
    'stop-api' {
        Stop-DevApi
    }
    'stop-web' {
        Stop-DevWeb
    }
    'reload' {
        & npm --prefix web run build
        Assert-NativeSuccess 'npm --prefix web run build'
        Restart-DevApi
    }
    'restart-dev' {
        Restart-DevApi
        Restart-DevWeb
    }
    'start' {
        if (-not (Test-Environment)) {
            exit 1
        }
        & npm --prefix web ci
        Assert-NativeSuccess 'npm --prefix web ci'
        & npm --prefix web run build
        Assert-NativeSuccess 'npm --prefix web run build'
        Restart-DevApi
        Restart-DevWeb
    }
    'stop' {
        Stop-DevAll
    }
    'restart' {
        if (-not (Test-Environment)) {
            exit 1
        }
        & (Join-Path $script:RootDir 'scripts\build.cmd') all
        Assert-NativeSuccess 'scripts\build.cmd all'
        Restart-DevApi
        Restart-DevWeb
    }
    default {
        [Console]::Error.WriteLine("未知命令：$command")
        Show-Usage
        exit 2
    }
}
