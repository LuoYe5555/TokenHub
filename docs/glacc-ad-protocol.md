# glacc（给梨加速器 v26.10.2.6）免费时长机制逆向报告

> 分析对象：`C:\Program Files (x86)\glacc\Program\resources\app\out.asar`
> 提取产物：`glacc_extract\`（asar 解包）、`glacc_src_main\`（main.js.map 还原的 TS 源码）

---

## 1. 总体架构

| 组件 | 说明 |
|---|---|
| `main.js` | Electron 主进程（有 sourcemap，TS 源码已还原到 `glacc_src_main\src`） |
| `main-renderer/renderer.js` | 主渲染层，业务逻辑全在这（无 sourcemap，已从压缩包提取关键函数） |
| `embeded-webview-renderer/renderer.js` | 嵌入 H5 容器 |
| `webview-preload.js` | H5 ↔ 客户端桥接 preload（`window.native`） |
| `ylh.js` + `ylh_sdk` | 广告 SDK 原生窗体进程（优量汇/YLH） |
| 资产结算 | **全部服务端**：`https://api-xacc.xunlei.com` |

## 2. 免费时长从哪来

金币体系：`1 金币 = 1/80 分钟`（`DEFAULT_COIN_2_DURATION_RATE = 80`，服务端可下发 `coin_2_duration_rate` 覆盖）。

- 钱包：`GET /xlppc.gacs/api/gxsdn/gold/get_user_wallet` → `{score, ticket, vip, remain_acc_second, ...}`
- 加速：`POST /xlppc.gacs/api/gxsdn/gold/start_speed` / `end_speed`
- 金币来源 = 看广告 + 任务中心 + 签到 + 兑换码（`rewardType 3` → 时长）

## 3. 看广告链路（核心）

```
广告 H5 (mercuryh5.ixiaochuan.cn/gsAccelerator)
   │  window.native.CallNativeFunction(...)
   ▼
webview-preload 桥 ──ipc──▶ main-renderer onJsCallNativeFunction
   │  host 白名单 O = [".xunlei.com","mercuryh5.ixiaochuan.cn","geilijiasu.*",
   │                    "localhost:3000", ...]   ← 开发残留！
   ▼
clickWebView / exposeWebView / completeWebview  (C 函数)
   │  body = {advert_id, advert_type, event_type, ts, count, timestamp, sign}
   ▼
POST https://api-xacc.xunlei.com/xlppc.gacs/api/gxsdn/act/advert/stats
   │  code==0 → 500ms 后广播 AD_TASK_CHANGE_EVENT
   ▼
任务列表刷新：GET /xlppc.gacs/api/gxsdn/act/advert/config/list
   → task_list[].complete_status 变化 → 服务端发金币 → get_user_wallet 里 score 涨
```

### 3.1 签名算法（`guangshanSignParams`，密钥硬编码）

```js
function guangshanSign(params) {
  const p = { ...params, key: "07f21229eab0bb7d7c4" };
  const str = Object.keys(p).sort()
    .map((k, i) => `${i === 0 ? "" : "&"}${k}=${p[k]}`)
    .join("");
  return md5(str).toLowerCase();
}
```

advert/stats 的 body：
```js
{ advert_id, advert_type, event_type, ts, count, timestamp: Math.ceil(Date.now()/1e3) }
// sign = guangshanSign(上述字段)   ← timestamp 参与签名
```

无 nonce、无设备指纹入签、时间戳秒级——**纯本地可复现**。

### 3.2 请求头（`fetchApiEx` 自动附加）

```
package-name: glacc
is-login / is-vip: 1
userid / user-id: <用户ID>
Authorization: Bearer <UserGetAccessToken()>
device-id: <GetDeviceID()>
peerid: <AccHelper.getPeerId()>
app-version / bundle-version / commit-tag / install-channel / x-channel-id
```

## 4. 本地攻击面

### 4.1 DevTools / 广告页替换后门（`DevEnvHelperNS`）

```ini
; C:\ETW_LOG\log.ini
[Log]
enable=1
[AD]
url=http://localhost:3000/index.html   ; 广告 H5 URL 整体替换
banner=...                              ; 首页 banner 同理
vip=1                                   ; [AD] vip 开关
```

- `getADUrl()` 优先于内置 `URL_AD` → 广告窗口加载任意本地页
- `[Log] enable=1` → `Ctrl+Alt+T`、`F12` 开 DevTools（webview 与主窗）

### 4.2 H5 → Native 桥（webview-preload 暴露的完整 API 面）

```js
window.native.NativeReady()
window.native.CallNativeFunction(name, ...args, cb)   // 核心通道
window.native.CheckNativeFunction(name, cb)
window.native.RegisterJSFunction(name, fn)             // 客户端回调 H5
window.native.AttachNativeEvent(evt, fn)
```

`view_ad` 窗口注册的 native 函数（H5 可调）：

| 函数 | 作用 |
|---|---|
| `GetADCompletionInfo` | GET config/list，拿任务列表（真实 advert_id 配置） |
| `ExposeWebView` | 上报曝光事件 → advert/stats |
| `ClickWebView` | 上报点击事件 → advert/stats |
| `CompleteWebview` | 上报完成事件 → advert/stats |
| `TransferFetch` | **万能转发**：任意 `{url, method, body}`，自动带全部登录态头 |
| `GetADHwnd` / `CreateWnd` / `ShowWebview` / `CloseWebview` | 窗口控制 |
| `SDKInit` / `IsAppInstalled` / `QueryAppVersion` | 环境 |

### 4.3 host 白名单（`O` 数组，`endsWith` 匹配 `URL.host`）

```
.xunlei.com | h5-pccloud.onethingpcs.com | h5-pciaas.onethingpcs.com
h5-pccloud.onethingcloud.com | h5-pcxl.hiveshared.com
localhost:3000                        ← 开发残留，本地 3000 端口直通
mercuryh5.ixiaochuan.cn | mercuryh5-test.ixiaochuan.cn
geilijiasu.com | geilijiasu.net | geilijiasu.cn
```

### 4.4 已排除路线

- **支付 Mock**（`paymentTouchpointsMock`）：`RUN_ENV==="development"` 硬门禁 + 仅 mock 投放 UI、不动服务端钱包 → 产品版无效。
- **改本地响应**（mitmproxy 改 `code:0`）：纯 UI 欺骗，金币在服务端 → 无效。
- **直接改 asar**：结算在服务端，改客户端 UI/逻辑拿不到币。

## 5. 可行方案

### 方案 A：本地假广告 H5（推荐，走正常链路）

1. 写 `C:\ETW_LOG\log.ini`（见 `log.ini`）
2. `node fake_ad_server.js` 起本地 3000 端口
3. 启动 glacc → 任务中心 → "看广告领免排队卡"
4. 加载的是假页面，自动批量 `ExposeWebView` / `ClickWebView` / `CompleteWebview`
5. 服务端按正常流程累计进度、发金币 → 时长到账

### 方案 B：直接打 API（脱离客户端）

`advert_stats_bot.js`：读 `headers.json`（登录态头，可从 DevTools Network 抓一次），
先 `config/list` 拿真实 advert 配置，再循环 POST `advert/stats`（本地算 sign）。
请求头一样要带全，token 从 DevTools 里拿一次可用较久。

### 方案 C：TransferFetch 万能通道

log.ini 替换的广告页里可直接：

```js
window.native.CallNativeFunction("TransferFetch",
  JSON.stringify({url: "https://api-xacc.xunlei.com/xlppc.gacs/api/gxsdn/gold/get_user_wallet",
                  method: "get", body: {}}),
  (err, ret) => console.log(ret));   // ret = {status, data:{code, data:{score...}}}
```

任意 API（含 start_speed/end_speed、任务接口）都能以本人登录态调，自动带签名所需的
全部头。等于绕过 UI 直接操作服务端接口。

## 6. 服务端可能的对抗点（工程事实，影响成功率）

- `advert_id` 需为 config/list 返回的真实条目，伪造 id 大概率 code!=0
- 频控：客户端正常节奏是 每条广告 expose→click→complete，间隔数十秒；脚本里做了随机抖动
- `withRetry` 最多 6 次 / 2s 间隔，说明服务端偶发 408/超时是常态
- Bearer token 过期需重取；`device-id`/`peerid` 与硬件绑定（AccHelper.node）
- 任务进度由服务端结算，本地一切操作只是"模拟上报"，金币到账与否以 `get_user_wallet` 为准

## 7. 文件清单

| 文件 | 说明 |
|---|---|
| `log.ini` | 放到 `C:\ETW_LOG\log.ini`，开启后门 + 指向本地广告页 |
| `fake_ad_server.js` | 方案 A：本地 3000 端口假广告页（含自动上报循环） |
| `advert_stats_bot.js` | 方案 B：脱离客户端直打 API 的 Node 脚本 |
| `glacc_asar_tool.js` / `asar_tool.js` | asar 解包工具（复现分析用） |
| `glacc_extract\` | out.asar 全量解包产物 |
| `glacc_src_main\` | 主进程 TS 源码还原 |
