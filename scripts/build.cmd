@echo off
rem TokenHub 构建脚本（需要 Go 1.22+，PATH 里有 go）
setlocal
cd /d "%~dp0.."

where go >nul 2>nul
if errorlevel 1 (
  echo [!] 未找到 go，请安装 Go 或把 C:\Program Files\Go\bin 加入 PATH
  exit /b 1
)

rem 嵌入 exe 图标（rsrc 源文件已生成；若改了 tokenhub.ico 需重新运行 rsrc）
if not exist rsrc_windows_amd64.syso (
  echo [i] 生成图标资源...
  go run github.com/akavel/rsrc@latest -ico tokenhub.ico -o rsrc_windows_amd64.syso
)

echo [i] 构建 TokenHub.exe ...
set GOFLAGS=-mod=mod
rem 注入当前 git commit 短哈希，自动更新用它与 release tag（release-<commit>）比对
for /f %%i in ('git rev-parse --short HEAD 2^>nul') do set GITCOMMIT=%%i
if "%GITCOMMIT%"=="" (
  echo [!] 未取到 git commit（不在 git 仓库或未安装 git），自动更新将按开发构建跳过
  go build -trimpath -ldflags "-s -w -H windowsgui" -o TokenHub.exe .
) else (
  echo [i] 版本 commit: %GITCOMMIT%
  go build -trimpath -ldflags "-s -w -H windowsgui -X tokenhub/internal/config.GitCommit=%GITCOMMIT%" -o TokenHub.exe .
)
if errorlevel 1 (
  echo [!] 构建失败
  exit /b 1
)

echo [i] 构建 mock 上游（联调用）...
go build -o scripts\mock\mock.exe .\scripts\mock

echo [√] 完成: TokenHub.exe
endlocal
