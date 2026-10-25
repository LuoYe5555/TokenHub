TokenHub Linux 版部署说明
==========================

只需两步（不用记任何参数）：

  1. 把 TokenHub-linux-amd64.tar.gz 上传到服务器（宝塔面板/ftp/scp 都行）
  2. 在文件所在目录执行：

       tar -xzf TokenHub-linux-amd64.tar.gz
       sh start.sh

  看到日志输出后，浏览器打开 http://127.0.0.1:8687 即面板。
  账号在面板里用设备码登录导入。

常用可选项（都是可选的，加在 start.sh 后面）：

  sh start.sh -port 8688            换端口
  sh start.sh -host 0.0.0.0         允许其他机器访问面板/API
                                    ⚠ 开了之后面板会暴露到公网，
                                    API 调用务必带 Key；管理页建议只留本机
  sh start.sh -data /root/th-data   自定义数据目录

后台常驻运行（关掉 SSH 也不退出）：

  nohup sh start.sh > tokenhub.log 2>&1 &

开机自启（systemd 服务器）：

  sudo tee /etc/systemd/system/tokenhub.service > /dev/null << 'EOF'
  [Unit]
  Description=TokenHub
  After=network.target
  [Service]
  WorkingDirectory=/root/tokenhub
  ExecStart=/root/tokenhub/TokenHub-linux-amd64
  Restart=always
  [Install]
  WantedBy=multi-user.target
  EOF

  （把 /root/tokenhub 换成实际解压目录）
  sudo systemctl daemon-reload && sudo systemctl enable --now tokenhub

注意：
- 自动更新在 Linux 上不生效，更新版本请重新下载替换后重启
- 「一键浏览器导入」需要服务器装有 Chrome/Chromium/Edge，没有就用设备码登录
- 数据（账号/配置/日志）都在程序目录的 data/ 里，备份直接拷走
