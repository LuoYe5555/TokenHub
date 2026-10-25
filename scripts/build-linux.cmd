@echo off
rem TokenHub Linux 版构建脚本（在 Windows 上交叉编译，无需 Linux 环境）
rem 产物: TokenHub-linux-amd64（传到 Linux 服务器后 chmod +x 运行）
setlocal
cd /d "%~dp0.."

where go >nul 2>nul
if errorlevel 1 (
  echo [!] 未找到 go，请安装 Go 或把 C:\Program Files\Go\bin 加入 PATH
  exit /b 1
)

echo [i] 构建 TokenHub-linux-amd64 ...
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
set GOFLAGS=-mod=mod

rem 注入当前 git commit 短哈希（与 release tag 比对；Linux 版不自动更新，仅作版本标识）
for /f %%i in ('git rev-parse --short HEAD 2^>nul') do set GITCOMMIT=%%i
if "%GITCOMMIT%"=="" (
  go build -trimpath -ldflags "-s -w" -o TokenHub-linux-amd64 .
) else (
  echo [i] 版本 commit: %GITCOMMIT%
  go build -trimpath -ldflags "-s -w -X tokenhub/internal/config.GitCommit=%GITCOMMIT%" -o TokenHub-linux-amd64 .
)
if errorlevel 1 (
  echo [!] 构建失败
  exit /b 1
)

echo [i] 打包部署包 TokenHub-linux-amd64.tar.gz ...
rem 包内含：二进制 + start.sh（自动 chmod 并启动）+ README-LINUX.txt（部署说明）
tar -czf TokenHub-linux-amd64.tar.gz -C . TokenHub-linux-amd64 -C scripts\linux-bundle start.sh README-LINUX.txt
if errorlevel 1 (
  echo [!] 打包失败（不影响，二进制已生成）
  echo [√] 完成: TokenHub-linux-amd64
  exit /b 0
)

echo [√] 完成: TokenHub-linux-amd64 + TokenHub-linux-amd64.tar.gz
echo     部署（最简）: 上传 tar.gz 到服务器后执行
echo       tar -xzf TokenHub-linux-amd64.tar.gz ^&^& sh start.sh
echo     详见包内 README-LINUX.txt
endlocal
