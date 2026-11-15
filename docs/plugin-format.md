# TokenHub 插件格式

插件是一个目录，包含清单、入口脚本和可选的卡片页。安装后面板「插件」页可启停/卸载，
启用时入口脚本在 goja JS 沙箱中运行，卡片页以 iframe 沙箱嵌在面板下方。

## 目录结构

```
my-plugin/
├── plugin.json        # 清单（必需）
├── main.js            # 入口脚本（默认；可用 manifest 的 main 字段改路径）
└── ui/                # 卡片页（可选）
    ├── index.html
    └── style.css / app.js ...   # 静态资源（扩展名白名单内）
```

安装到 `data/plugins/installed/<id>/`，插件键值存储落盘在
`data/plugins/data/<id>.json`，安装/启停/卸载记录在 `data/plugins/audit.jsonl`。

## plugin.json 清单

```json
{
  "id": "my-plugin",
  "name": "我的插件",
  "version": "1.0.0",
  "description": "一句话说明",
  "author": "你的名字",
  "main": "main.js",
  "permissions": {
    "network": ["https://api.example.com"],
    "storage": true
  },
  "ui": {
    "entry": "ui/index.html",
    "title": "卡片标题"
  }
}
```

| 字段 | 约束 |
|---|---|
| `id` | 小写字母开头，`[a-z0-9_-]`，2–48 字符，全程序唯一 |
| `version` | `x.y.z` 三段数字 |
| `main` | 相对路径 `.js` 文件，缺省 `main.js`；禁止绝对路径 / `\` / `..` |
| `permissions.network` | 允许出站的**精确 origin** 列表（如 `https://api.example.com`），最多 8 个；**不写 = 禁网** |
| `permissions.storage` | `true` 才能用 `th.storage` |
| `ui.entry` | 必须在 `ui/` 目录下的 `.html` 文件；不写则插件不提供卡片 |

## main.js：入口脚本

在 goja 沙箱里执行（ES2020 子集，无 require/import，无 Node API）。
定义全局函数 `onRpc(method, params)` 即可被卡片页调用：

```js
function onRpc(method, params) {
  if (method === 'visit') {
    var n = Number(th.storage.get('visits') || 0) + 1;
    th.storage.set('visits', n);
    return { visits: n };
  }
  return { error: '未知方法: ' + method };   // 约定：{error} 表示业务错误
}
```

规则：
- 必须同步返回 JSON 可序列化的值（无 Promise/异步）
- 每次调用限时 **5 秒**，超时被强制中断
- 返回 `{ error: "..." }` 会以错误形式呈现给调用方

## SDK（全局对象 `th`）

| API | 说明 |
|---|---|
| `th.log(...)` | 写运行日志（tag `plugin/<id>`，面板「运行日志」可见） |
| `th.storage.get(key)` | 读键值（需 `permissions.storage`），缺省返回 `undefined` |
| `th.storage.set(key, value)` | 写键值，值须 JSON 可序列化，全量落盘 |
| `th.data.models()` | 模型目录快照 `{ provider: [{id, name}] }`（同步，随渠道刷新） |
| `th.http.fetch(url, opts)` | **同步** HTTP。`opts: {method, headers, body}`，返回 `{status, body}`；body 为对象时自动 JSON 序列化；origin 必须在白名单里；单次 8 秒超时，响应上限 4MB |

## ui/ 卡片页

清单声明 `ui.entry` 后，卡片以 `<iframe sandbox="allow-scripts">` 渲染：
**没有同源权限**，不能直接 fetch 面板 API，调 onRpc 走 postMessage 桥：

```html
<script>
let seq = 0;
function rpc(method, params) {
  return new Promise((resolve, reject) => {
    const reqId = 'r' + (++seq);
    const onMsg = (ev) => {
      const d = ev.data || {};
      if (d.plugRpcReply && d.reqId === reqId) {
        window.removeEventListener('message', onMsg);
        d.ok ? resolve(d.result) : reject(new Error(d.error || 'rpc 失败'));
      }
    };
    window.addEventListener('message', onMsg);
    parent.postMessage({ plugRpc: { reqId, method, params: params || null } }, '*');
    setTimeout(() => { window.removeEventListener('message', onMsg); reject(new Error('rpc 超时')); }, 8000);
  });
}
// 用法：const r = await rpc('visit');  // r 即 onRpc 的返回值
</script>
```

消息协议（插件作者一般不需要直接用）：
- 卡片 → 面板：`parent.postMessage({ plugRpc: { reqId, method, params } }, '*')`
- 面板 → 卡片：`{ plugRpcReply: { reqId, ok, result?, error? } }`

静态资源按相对路径引用即可（`<img src="logo.png">`），可用扩展名：
html/css/js/png/jpg/gif/svg/webp/ico/woff/woff2/ttf/json/txt。

## 安装方式（面板 → 插件 → 添加插件）

1. **货架**：内置示例，点装即用
2. **GitHub 仓库**：填 `owner/repo` 或完整 URL，拉 zipball（自动尝试直连 + ghproxy 镜像 × main/master）；`plugin.json` 在仓库根目录或唯一一层子目录均可
3. **本地目录**：直接填磁盘路径（开发调试用）

## 最小示例

```jsonc
// plugin.json
{ "id": "hello", "name": "你好", "version": "1.0.0", "main": "main.js" }
```

```js
// main.js
function onRpc(method) { return { msg: 'hello!' }; }
```

无 ui 的插件也可以：安装后仅作为启停项存在，供后续钩子扩展使用。
