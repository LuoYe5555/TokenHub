# TokenHub

把 **Trae**、**WorkBuddy / CodeBuddy**、**ZCode（智谱 GLM）**、**千问网页版**四家 AI 工具的账号额度，转换成本地 **OpenAI / Anthropic 兼容 API**，供任意第三方客户端（Cherry Studio、Claude Code、Cline、OpenAI SDK 等）使用。

> ⚠️ **仅限个人自用，请勿贩卖或商用。** 凭据只保存在本机。

单文件 `TokenHub.exe`（Go 编写，无外部依赖），双击即用。可执行文件从 [Releases](https://github.com/LuoYe5555/TokenHub/releases) 下载，或自行[从源码构建](#从源码构建)（仓库不存放构建产物）：

- **Web 面板**：多账号管理、额度查看、用量日志、中英双语
- **多账号池**：同一家可挂多个账号自动轮换；额度耗尽 / 限流 / 凭据失效自动冷却切换
- **自动领取**：WorkBuddy 每日签到、Trae 每日签到（+100 积分/天/号）、ZCode 活动套餐自动领取
- **两种协议**：`/v1/chat/completions`（OpenAI）与 `/v1/messages`（Anthropic，Claude Code 可直连）
- **分享**：局域网一键分享；公网分享（内置 bore 协议客户端，只暴露 API 不暴露面板）；分享钥匙可按提供商开关、设用量上限与模型白名单

## 快速开始

1. 双击 `TokenHub.exe`（首次运行自动生成 `data\` 目录、随机 API Key 并打开面板）
2. 面板 → **账号** → 添加账号：
   - **WorkBuddy / CodeBuddy**：选国内/国际版 → 授权页登录 → 自动回填；也可「从本机 CodeBuddy 导入」，或「导入 wb-switch 文件」批量导入（见下）
   - **Trae**：授权页扫码登录 → 自动回填（回调页打不开时把地址栏链接粘贴进面板）
   - **ZCode**：选 BigModel / Z.ai 渠道 → 授权登录，或一键从本机 ZCode 客户端导入
   - **千问**：点「一键浏览器导入」→ 在弹出的窗口里登录 chat.qwen.ai → 自动完成（令牌过期自动续期）；也可手动粘贴 Token
3. 面板 → **API 接口**：复制 Base URL 与 API Key 填进任意客户端

```
Base URL : http://127.0.0.1:8687/v1        （Anthropic 客户端填 http://127.0.0.1:8687）
API Key  : sk-th-xxxxxxxxxxxxxxxx           （面板可重新生成）
```

## 模型与路由

- `GET /v1/models` 聚合四家模型列表
- `model` 支持 **前缀指定提供商**：`trae:glm-5.2`、`workbuddy:claude-sonnet-4.6`、`zcode:glm-5.3`、`qwen:qwen3.8-max`（简写 `qw:` / `qwen-web:` 也认）
- 不带前缀按 `defaultProvider` 与 `modelRoutes` 路由（面板 → 设置）
- 思考控制（千问）：模型名加后缀 `-thinking` / `-no-thinking`

## 导入 wb-switch 账号文件

**wb-switch** 是第三方 WorkBuddy 账号切换工具，它能把自己收藏的多个账号导出成一个 JSON 文件。TokenHub 可以直接吃这个文件：

面板 → **账号** → 添加账号 → **WorkBuddy** → 「📄 导入 wb-switch 文件」，选文件或把文件拖到按钮上：

1. 先**预演**（不落盘）：逐条列出新增 / 更新 / 跳过 / 无效，以及每条的到期时间和警告
2. 确认无误后点「确认导入」才真正写入

去重按 `uid`：已有账号更新凭据（保留原 ID、启用状态与签到记录），新账号直接新增；凭据完全相同的记为「跳过」。

支持的字段（对命名风格宽容，`snake_case` / `camelCase` 都认）：

| wb-switch 字段 | 映射到 | 说明 |
|---|---|---|
| `access_token` / `refresh_token` | `AccessToken` / `RefreshToken` | 缺失时回退到 `auth_raw` 里的同名字段 |
| `uid` | `UID` | 去重键；缺失时尝试从 `profile_raw.uid` 或令牌载荷推断 |
| `variant: cn` / `intl` | `Realm: cn` / `global` | 缺失时按 `domain` 推断（`*.workbuddy.ai` → 国际版） |
| `expiresAt` / `refreshExpiresAt` | `ExpiresAt` | 毫秒自动折算成秒；缺失时用 `auth_raw.expiresIn` 推算 |
| `domain` / `nickname` / `email` / `enterpriseId` | 同名字段 | 为 `null` 时忽略 |
| `auth_raw.sessionState`、`profile_raw.uin/phoneNumber` | `Extra` | 仅保留作展示与诊断，不参与鉴权 |

`access_token` 已过期但 `refresh_token` 仍有效的记录照样能导入（首次调用会自动续期）；两个都过期的会带警告导入，需要重新登录。

## 自动领取

| 提供商 | 方式 | 默认 |
|---|---|---|
| WorkBuddy 国内 | 每日签到 | 开启（当日成功即跳过） |
| WorkBuddy 国际 | 无签到接口，可选极小对话保活 | 关闭 |
| Trae | 每日签到（每号每天 +100 积分） | 开启 |
| ZCode | 活动套餐轮询领取（每 30 分钟） | 开启 |
| 千问 | 无签到/领取接口 | — |

需要验证码的 ZCode 活动会弹窗处理或记录在签到消息里，可手动领取。

## 安全设计

- 公网**只暴露 API**（`/v1/*`）：管理面板、凭据、日志永不出现公网地址；隧道仅做出站连接，不开放任何入站端口
- 公网调用必须带 Key：主人用主 Key，别人用分享钥匙（可随时停用，用量源头统计）

## 配置与数据

- 配置：`data\config.json`（端口 `8687`、`defaultProvider`、自动签到开关、上游地址覆盖等，均有注释）
- 数据：账号凭据 `data\accounts.json`、分享钥匙 `data\shares.json`、日志 `data\logs\`——删除 `data\` 即卸载
- 端口占用：改 `config.json` 的 `port`，或启动参数 `-port 8688`

## 从源码构建

```
scripts\build.cmd        （需要 Go 1.22+，产物 TokenHub.exe）
```

构建产物（`TokenHub.exe`、`scripts\mock\mock.exe`）不入库，`.gitignore` 已排除；需要可执行文件请自行构建，或从 [Releases](https://github.com/LuoYe5555/TokenHub/releases) 下载。

## 许可证

[MIT](LICENSE)。仅供个人学习与自用，请遵守各服务商用户协议，账号风险自行承担；`data\` 已被 `.gitignore` 排除，凭据不会被提交。

> bore 协议参考 [ekzhang/bore](https://github.com/ekzhang/bore) v0.5.0（MIT），Go 原生实现，无需额外程序。
