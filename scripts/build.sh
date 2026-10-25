#!/usr/bin/env bash
# TokenHub 构建脚本（Linux / macOS）。
# 用法：scripts/build.sh [输出文件名]   默认输出仓库根目录的 TokenHub
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  echo "[!] 未找到 go，请安装 Go 1.27+ 或把 go 加入 PATH"
  exit 1
fi

out="${1:-TokenHub}"

echo "[i] 构建 $out ..."
# CGO_ENABLED=0：modernc.org/sqlite 是纯 Go 实现，静态链接免依赖
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$out" .
echo "[√] 完成: $out"

echo "[i] 构建 mock 上游（联调用）..."
CGO_ENABLED=0 go build -o scripts/mock/mock ./scripts/mock
echo "[√] 完成: scripts/mock/mock"

echo
echo "启动：./$out            （自动打开浏览器，面板 http://127.0.0.1:8687）"
echo "后台：./$out -no-window  （服务器 / systemd 场景）"
