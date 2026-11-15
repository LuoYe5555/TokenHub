// hello-card 插件后端：面板卡片通过 /api/panel/plugins/hello-card/rpc 调这些方法。
// 可用的全局 SDK：
//   th.log(...)                  写运行日志（tag = plugin/hello-card）
//   th.storage.get(key) / .set(key, value)   键值存储（JSON 落盘）
//   th.data.models()             模型目录快照 {provider: [{id,name}]}
//   th.http.fetch(url, opts)     出站请求（origin 须在 permissions.network 白名单）

function onRpc(method, params) {
  if (method === 'greet') {
    return { msg: '你好，' + (params && params.name || '陌生人') + '！这是来自 goja 沙箱的问候。' };
  }
  if (method === 'visit') {
    var n = th.storage.get('visits') || 0;
    n = Number(n) + 1;
    th.storage.set('visits', n);
    return { visits: n };
  }
  if (method === 'stats') {
    var models = th.data.models() || {};
    var total = 0;
    for (var p in models) total += (models[p] || []).length;
    return { providers: Object.keys(models).length, models: total };
  }
  return { error: '未知方法: ' + method };
}
