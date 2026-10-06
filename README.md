# TokenHub

把 **Trae**、**WorkBuddy / CodeBuddy**、**ZCode（智谱 GLM）** 三家 AI 编程工具的账号额度，转换成本地 **OpenAI / Anthropic 兼容 API**，供任意第三方客户端（Cherry Studio、Claude Code、Cline、OpenAI SDK 等）使用。

> ⚠️ **仅限个人自用，请勿贩卖或商用。** 请在自己的账号内使用，凭据只保存在本机。

单文件 `TokenHub.exe`（Go 编写，无外部依赖），双击即用：

- **Web 面板**：多账号管理、额度查看、日志、设置，支持 **简体中文 / English** 切换
- **自动领取**：WorkBuddy 国内每日签到、Trae 每日签到（+200 积分）、ZCode 活动套餐自动领取
- **多账号池**：同一家可挂多个账号自动轮换，额度耗尽 / 限流 / 凭据失效自动冷却切换
- **额度查看**：每账号实时余额、权益包明细、到期时间
- **两种协议**：`/v1/chat/completions`（OpenAI）与 `/v1/messages`（Anthropic，Claude Code 可直连）
- **局域网分享**：一键显示局域网地址，同一 WiFi 下填 URL + Key 即可用
- **公网分享**：内置 bore 协议客户端（Go 原生实现，无外部程序），一键生成 `bore.pub` 公网地址，任何网络可访问
- **分享钥匙**：给别人的专用 Key（`sk-share-*`），可按提供商（WorkBuddy / Trae / ZCode）分别开关并设 token 用量上限，随时停用，用量在源头统计

---

## 快速开始

1. 双击 `TokenHub.exe`（首次运行自动生成 `data\` 目录、随机 API Key，并打开面板）
2. 面板 → **账号** → 选择服务商 **添加账号**：
   - **WorkBuddy / CodeBuddy**：选国内版或国际版 → 打开授权页登录 → 自动回填
   - **Trae**：打开 TRAE 授权页扫码登录 → 自动回填（若浏览器提示无法访问回调页，把地址栏链接粘贴进面板即可）
   - **ZCode**：选智谱 BigModel 或 Z.ai 渠道 → 打开授权页登录 → 自动回填；也可一键 **从本机 ZCode 导入**（解密 `~/.zcode/v2/credentials.json` 当前登录账号）
3. 面板 → **API 接口**：复制 Base URL 与 API Key，填进任意客户端

```
Base URL : http://127.0.0.1:8687/v1        （Anthropic 客户端填 http://127.0.0.1:8687）
API Key  : sk-th-xxxxxxxxxxxxxxxx           （面板可重新生成）
```

## 模型与路由

- `GET /v1/models` 聚合三家模型列表
- 请求里的 `model` 支持 **前缀指定提供商**：`trae:glm-5.2`、`workbuddy:claude-sonnet-4.6`、`zcode:glm-5.3`
- 不带前缀的模型按 `defaultProvider` 与 `modelRoutes` 路由（面板 → 设置）

## 自动领取说明

| 提供商 | 方式 | 默认 |
|---|---|---|
| WorkBuddy 国内 | `daily-checkin` 每日签到 | 开启，每小时尝试（当日成功即跳过） |
| WorkBuddy 国际 | 无签到接口，可选“极小对话保活” | 关闭（设置里开启） |
| Trae | `checkin_credits/claim` 每日签到 | 开启 |
| ZCode | `billing/preview` → `billing/claim` 活动套餐轮询领取 | 开启，每 30 分钟一轮 |

需要验证码的 ZCode 活动无法自动领取，会记录在签到消息里，可在面板账号卡片上手动领取。

**ZCode 额度口径**：自动合并三层来源——`billing/balance`（每日免费额度）→ 生效套餐权益（体验包/活动包）→ `billing/current` 里独立领取的体验套餐（按 plan_id 去重，不重复计数）。如果你在 ZCode 领取的额度仍显示不出来，点账号卡片上的 **原始数据** 按钮，把各上游接口的原始返回发出来即可定位。

## 公网分享与分享钥匙

**API 接口页 → 公网分享**：点「开启公网分享」，约 5~30 秒后生成 `http://bore.pub:<端口>` 地址（每次开启端口随机）。把这个地址 + 分享钥匙发给朋友，任何网络下都能调用。

🔒 **安全设计**（重要）：

- 公网**只暴露 API**（`/v1/*`）：隧道指向一个绑定 `127.0.0.1` 的「仅 API 监听」，管理面板、账号凭据、日志**永远不会**出现在公网地址上（已实测：公网访问面板路径返回 404）
- 隧道仅做出站连接（bore 协议客户端），不在本机/路由器开放任何入站端口，无被反向渗透入口
- 所有公网调用必须带 Key：主人用主 Key，别人用分享钥匙；无 Key 返回 401

**分享钥匙**（API 接口页 → 分享钥匙）：

- 每把钥匙可对 WorkBuddy / Trae / ZCode 分别开关，只分享想分享的额度
- 每个提供商可设 token 用量上限（0 = 不限）；WorkBuddy / Trae 的「积分」上游无法逐次读取，程序按 token 用量近似控制，建议设保守值
- 每个提供商可设**模型白名单**（逗号分隔，如 `glm-5.7`，留空 = 全部允许），白名单外请求直接 429 拦截，不消耗额度
- 随时停用 / 删除（立即生效）；面板实时显示每把钥匙的请求次数与 token 用量
- 主 API Key 不要外发，发分享钥匙即可

> bore 协议参考 [ekzhang/bore](https://github.com/ekzhang/bore) v0.5.0（MIT License），Go 原生实现，无需安装任何额外程序。

## 配置（data/config.json）

| 字段 | 说明 | 默认 |
|---|---|---|
| `host` / `port` | 监听地址 / 端口 | `127.0.0.1` / `8687` |
| `apiKey` | 网关与面板密钥（局域网访问面板也需要它） | 首次随机生成 |
| `defaultProvider` | 裸模型名的默认路由 | `workbuddy` |
| `autoCheckin` | 每日自动签到 | `true` |
| `zcodeAutoClaim` / `zcodeClaimIntervalMin` | ZCode 活动领取与周期 | `true` / `30` |
| `zcodeInjectShape` | ZCode 请求形状注入：`overwrite`/`prepend`/`off` | `overwrite`（实测最稳） |
| `windowIcon` | 窗口图标图片路径（jpg/png），显示在标题栏和任务栏；留空用内置图标 | 空 |
| `quotaRefreshMin` | 额度自动刷新周期 | `30` |
| `providers.*.apiBase` 等 | 上游地址覆盖（接口变更时用） | 内置 |

账号凭据存于 `data/accounts.json`（明文本机保存，0600 权限）；分享钥匙存于 `data/shares.json`；运行状态与冷却记录同库。

## 从源码构建

```
scripts\build.cmd        （需要 Go 1.22+，产物 TokenHub.exe）
scripts\mock\mock.exe    （mock 三家上游，本地联调用：先起 mock 再用 testdata 配置跑主程序）
```

## 许可证

[MIT](LICENSE) © 2026 TokenHub Contributors

- `data\`（账号凭据 / API Key / 日志）已被 `.gitignore` 排除，不会被提交
- 本项目仅供个人学习与自用，请遵守各服务商的用户协议；使用本项目产生的账号风险由使用者自行承担

## 常见问题

- **端口占用**：改 `data/config.json` 的 `port`，或启动参数 `-port 8688`
- **Trae 登录回调打不开**：正常现象，把浏览器地址栏完整链接粘贴到面板「提交回调链接」
- **ZCode 提示 401 / 凭据失效**：JWT 过期，在 ZCode 客户端重新登录后重新导入
- **某个账号额度耗尽**：会自动冷却 6 小时并轮换其他账号，无需手动处理
- **数据都在哪**：全部在 exe 同目录的 `data\` 下，删除即卸载
