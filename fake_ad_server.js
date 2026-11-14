// glacc 假广告 H5 服务器 v2
// 监听 http://localhost:3000 —— 配合 C:\ETW_LOG\log.ini 的 [AD] url
//
// v2 变更:
//  - ts 字段 = 停留秒数 (服务端校验 click 需要 ts>=20), 不再发时间戳
//  - 事件类型对齐真实 H5: expose / click / complete 分开发
//  - 新增 auto: 自动扫描未完成主任务, 补 expose, 21s 后补 click, 再 complete
//
// 运行: node fake_ad_server.js

const http = require('http');

const PAGE = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>AD</title>
<style>
  body{margin:0;background:#111;color:#0f0;font-family:Consolas,monospace;font-size:13px;padding:12px}
  #log{white-space:pre-wrap;max-height:70vh;overflow:auto}
  button{margin:4px;padding:6px 10px;background:#222;color:#0f0;border:1px solid #0f0;cursor:pointer}
  .dim{color:#888}
</style></head>
<body>
<div>glacc fake-ad bridge v2 · <span id="st" class="dim">checking bridge...</span></div>
<div id="log"></div>
<div>
  <button onclick="autoAll()">auto all tasks</button>
  <button onclick="doClickTask()">click task only</button>
  <button onclick="dumpState()">dump state</button>
  <button onclick="dumpWallet()">wallet</button>
  <button onclick="dumpHeaders()">dump auth headers</button>
</div>
<script>
const logEl = document.getElementById('log');
function log(s){ logEl.textContent += s + "\\n"; }
const jstr = o => { try { return JSON.stringify(o); } catch(e){ return String(o); } };
const sleep = ms => new Promise(r => setTimeout(r, ms));
const rnd = (a,b) => a + Math.random()*(b-a);

function bridgeOK(){ return window.native && typeof window.native.CallNativeFunction === 'function'; }
async function waitBridge(){
  for (let i=0;i<50;i++){ if (bridgeOK()) return true; await sleep(200); }
  return false;
}
function nativeCallLoose(name, ...args){
  return new Promise((resolve) => {
    if (!bridgeOK()) return resolve({err:'no-bridge'});
    window.native.CallNativeFunction(name, ...args, (errCode, ret) => resolve({errCode, ret}));
  });
}
async function transferFetch(url, method='get', body={}){
  const r = await nativeCallLoose('TransferFetch', JSON.stringify({url, method, body}));
  return r.ret;
}
const ORIGIN = 'https://api-xacc.xunlei.com';
const EP = { wallet: ORIGIN + '/xlppc.gacs/api/gxsdn/gold/get_user_wallet' };

// ---- 状态 ----
async function getTaskState(){
  const r = await nativeCallLoose('GetADCompletionInfo');
  const d = r.ret;
  log('[raw GetADCompletionInfo] ' + jstr(d).slice(0, 1200));
  // 兼容多种包裹: {status,data:{code,data:{task_list}}} / {code,data:{task_list}} / {task_list}
  let x = d;
  if (x && x.data && x.data.data) x = x.data.data;      // {status,data:{code,data:{...}}}
  else if (x && x.data) x = x.data;                      // {code,data:{...}}
  if (!x || !x.task_list) {
    // 可能整个就是 {data:{task_list}} 再包一层
    const maybe = d && d.data && d.data.data && d.data.data.task_list ? d.data.data :
                  d && d.data && d.data.task_list ? d.data : null;
    if (maybe) x = maybe;
  }
  return x || {};
}
function masters(state){
  return (state && state.task_list) || [];
}
// 子任务在 special 字段下: {task_id, sub_task_id, task_type(expose/click), task_count, complete_count, finish}
function subOf(m, type){
  return ((m && m.special) || (m && m.task_list) || []).filter(s => s.task_type === type);
}
async function dumpWallet(){
  const r = await transferFetch(EP.wallet, 'get', {});
  const d = r && r.data && r.data.data;
  if (d) log('wallet: score=' + d.score + ' (~' + (d.score/80).toFixed(1) + ' min)  remain=' + d.remain_acc_second + 's');
  else log('wallet: ' + jstr(r && r.data).slice(0,200));
  return d;
}
async function dumpHeaders(){
  const r = await transferFetch('http://localhost:3000/echo', 'get', {});
  const h = (r && (r.data || r)) || {};
  const out = {
    'package-name': h['package-name'] || 'glacc',
    'is-login': h['is-login'] || '1',
    'is-vip': h['is-vip'] || '0',
    'userid': h['userid'] || '', 'user-id': h['user-id'] || '',
    'Authorization': h['authorization'] || '',
    'device-id': h['device-id'] || '', 'peerid': h['peerid'] || '',
    'app-version': h['app-version'] || '', 'bundle-version': h['bundle-version'] || '',
    'install-channel': h['install-channel'] || '', 'x-channel-id': h['x-channel-id'] || '',
  };
  log('== HEADERS (复制进 advert_stats_bot.js) ==\\n' + JSON.stringify(out, null, 2));
  return out;
}

// ---- 事件上报 ----
// 真实 H5 载荷: {event_type, ts(停留秒), count, source, extra, api_extra, adslot, mode, resource_id, advert_id, task_id, progress}
// 客户端只取 advert_id/advert_type/event_type/ts/count 进 advert/stats
async function sendEvent(fn, eventType, staySec, extra){
  const payload = Object.assign({
    event_type: eventType,
    ts: staySec,
    count: 1,
    advert_id: (extra && extra.advert_id) || '',
    advert_type: (extra && extra.advert_type) || '',
    resource_id: (extra && extra.resource_id) || '',
    source: (extra && extra.source) || '',
    extra: (extra && extra.extra) || '',
    api_extra: (extra && extra.api_extra) || '',
    adslot: (extra && extra.adslot) || '',
    mode: (extra && extra.mode) || 0,
  }, extra || {});
  const r = await nativeCallLoose(fn, JSON.stringify(payload));
  const code = r.ret && r.ret.code;
  log('  ' + fn + ' [' + eventType + ' ts=' + staySec + '] -> code=' + code + ' ' + jstr(r.ret && r.ret.data ? (r.ret.data.code!==undefined?{code:r.ret.data.code}:r.ret.data):'').slice(0,120));
  return r.ret;
}

// 找第一个未完成的主任务 (以子任务判定: 任一子任务 finish=false 即未完成)
function firstUnfinished(state){
  const ms = masters(state);
  for (const m of ms){
    const subs = (m && m.special) || (m && m.task_list) || [];
    if (!subs.length) { if (Number(m.complete_status) !== 2) return m; continue; }
    if (subs.some(s => !s.finish)) return m;
  }
  return null;
}

// 补齐某个主任务的 expose 次数
async function fillExpose(m){
  const subs = subOf(m, 'expose');
  for (const s of subs){
    if (s.finish) continue;
    const need = (s.task_count||0) - (s.complete_count||0);
    if (need <= 0) continue;
    log('expose: ' + (s.sub_task_id||s.task_id) + ' need ' + need);
    for (let i=0;i<need;i++){
      await sendEvent('ExposeWebView', 'expose', 5 + Math.floor(rnd(0,4)));
      await sleep(rnd(1800, 3200));
    }
  }
}

// 点击子任务: expose -> 真等 21s -> click(ts=25) -> complete
async function doClickTask(){
  log('--- click task flow');
  let state = await getTaskState();
  let m = firstUnfinished(state);
  if (!m){ log('所有主任务已完成'); return false; }
  log('target master: ' + m.task_id + ' ' + (m.task_name||'') + ' reward=' + (m.reward_desc||m.reward_count));

  await fillExpose(m);

  const clickSub = subOf(m, 'click').find(s => !s.finish);
  if (clickSub){
    log('click sub: ' + (clickSub.sub_task_id||clickSub.task_id) + ' (' + (clickSub.complete_count||0) + '/' + (clickSub.task_count||0) + ')');
    log('  pre-expose (停留铺垫)...');
    await sendEvent('ExposeWebView', 'expose', 25);
    log('  waiting 21s (模拟停留20s以上)...');
    await sleep(21000);
    await sendEvent('ClickWebView', 'click', 25);
    await sleep(rnd(800, 1500));
    await sendEvent('CompleteWebview', 'complete', 25);
  } else {
    log('该任务无未完成 click 子任务, 发 complete 收尾');
    await sendEvent('CompleteWebview', 'complete', 25);
  }

  await sleep(1500);
  state = await getTaskState();
  const m2 = (masters(state).find(x => x.task_id === m.task_id)) || {};
  const subs2 = (m2.special || m2.task_list || []);
  log('after master ' + m.task_id + ': ' + jstr(subs2.map(s => ({id: s.sub_task_id, t: s.task_type, c: s.complete_count + '/' + s.task_count, fin: s.finish}))));
  log('after complete_status=' + m2.complete_status + (subs2.every(s => s.finish) ? '  >>> 回客户端领奖励!' : ''));
  await dumpWallet();
  return true;
}

// 全自动: 循环处理所有主任务直到没有未完成
async function autoAll(){
  log('=== auto all start ===');
  for (let round = 0; round < 12; round++){
    const state = await getTaskState();
    const m = firstUnfinished(state);
    if (!m){ log('=== 全部主任务完成, 去客户端领奖励 ==='); break; }
    log('round ' + (round+1) + ': master ' + m.task_id);
    const ok = await doClickTask();
    if (!ok) break;
    await sleep(rnd(2500, 5000));
  }
  log('=== auto all done ===');
}

// ---- boot ----
window.addEventListener('DOMContentLoaded', async () => {
  const ok = await waitBridge();
  document.getElementById('st').textContent = ok ? 'bridge OK' : 'bridge MISSING';
  log('bridge check: ' + (ok ? 'OK' : 'MISSING'));
  if (!ok) return;
  try {
    const st = await getTaskState();
    const ms = masters(st);
    log('masters: ' + ms.map(m => m.task_id + '(' + (Number(m.complete_status)===2?'done':'pending') + ')').join(', '));
    const m = firstUnfinished(st);
    if (m){
      const subs = (m.special || m.task_list || []);
      log('next: ' + m.task_id + ' ' + jstr(subs.map(s=>s.task_type+':'+(s.complete_count||0)+'/'+s.task_count+(s.finish?'✓':'✗'))));
    } else {
      log('全部主任务子条件已满足, 回客户端领奖励');
    }
    await dumpWallet();
  } catch(e){ log('boot err ' + (e && e.message || e)); }
});
</script></body></html>`;

http.createServer((req, res) => {
  if (req.url.startsWith('/index.html') || req.url === '/' || req.url.startsWith('/?')) {
    res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
    res.end(PAGE);
  } else if (req.url.startsWith('/echo')) {
    res.writeHead(200, {'Content-Type': 'application/json'});
    res.end(JSON.stringify(req.headers));
  } else {
    res.writeHead(200, {'Content-Type': 'application/json'});
    res.end('{}');
  }
}).listen(3000, '127.0.0.1', () => console.log('fake ad server v2 on http://localhost:3000/index.html'));
