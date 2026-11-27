@echo off
setlocal

rem 从脚本位置切换到仓库根目录，确保可从任意工作目录执行。
pushd "%~dp0.."
if errorlevel 1 exit /b 1

if not "%~2"=="" (
	echo 错误：参数过多，仅支持无参数、all 或 agent-dist。 1>&2
	popd
	exit /b 1
)
if "%~1"=="" goto build
if "%~1"=="all" goto build
if "%~1"=="agent-dist" goto agent_dist

echo 错误：未知构建目标 "%~1"，仅支持 all 和 agent-dist。 1>&2
popd
exit /b 1

:build
npm --prefix web ci
if errorlevel 1 goto fail
npm --prefix web run build
if errorlevel 1 goto fail
if not exist "bin" mkdir "bin"
if errorlevel 1 goto fail
go build -o "bin\euler.exe" ./cmd/euler
if errorlevel 1 goto fail
goto done

:agent_dist
rem 输出目录可由 AGENT_DIST 覆盖，默认放在 bin\agent-dist。
if not defined AGENT_DIST set "AGENT_DIST=bin\agent-dist"
if not exist "%AGENT_DIST%" mkdir "%AGENT_DIST%"
if errorlevel 1 goto fail
set "GOOS=linux"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
go build -trimpath -ldflags "-s -w" -o "%AGENT_DIST%\euler-agent-linux-amd64" ./cmd/euler-agent
if errorlevel 1 goto fail
set "GOARCH=arm64"
go build -trimpath -ldflags "-s -w" -o "%AGENT_DIST%\euler-agent-linux-arm64" ./cmd/euler-agent
if errorlevel 1 goto fail
goto done

:fail
rem 先保存失败码，避免 popd 覆盖原始错误。
set "exit_code=%ERRORLEVEL%"
popd
exit /b %exit_code%

:done
popd
exit /b 0
