#!/bin/sh
# TokenHub Linux 一键启动：自动补可执行权限并运行
# 用法： sh start.sh        （默认 127.0.0.1:8687，面板仅本机访问）
#       sh start.sh -host 0.0.0.0 -port 8688   （允许其他机器访问，可加参数透传）
cd "$(dirname "$0")" || exit 1
chmod +x TokenHub-linux-amd64 2>/dev/null
exec ./TokenHub-linux-amd64 "$@"
