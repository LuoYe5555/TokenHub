// glacc advert/stats 直打脚本 (方案 B)
//
// 用法:
//   1) 打开 glacc 主窗 DevTools (F12, 需 C:\ETW_LOG\log.ini [Log] enable=1)
//   2) Network 里随便点一条 xlppc.gacs 请求 -> Copy as cURL / 复制 Request Headers
//   3) 把下面的头原样填进 HEADERS (Authorization / userid / device-id / peerid 必须真实)
//   4) node advert_stats_bot.js
//
// 流程: config/list 拿真实 advert 条目 -> 循环上报 expose/click/complete -> 轮询钱包
// 签名: key="07f21229eab0bb7d7c4", md5(排序后的 k=v&k=v 串, 含 timestamp), 密钥硬编码于客户端

const crypto = require('crypto');
const https = require('https');

const ORIGIN = 'https://api-xacc.xunlei.com';
const EP = {
  configList: '/xlppc.gacs/api/gxsdn/act/advert/config/list',
  stats:      '/xlppc.gacs/api/gxsdn/act/advert/stats',
  wallet:     '/xlppc.gacs/api/gxsdn/gold/get_user_wallet',
};

// ====== 填入你自己的登录态 (DevTools 抓一次) ======
const HEADERS = {
  'package-name': 'glacc',
  'is-login': '1',
  'is-vip': '0',
  'userid': '<USER_ID>',
  'user-id': '<USER_ID>',
  'Authorization': 'Bearer <ACCESS_TOKEN>',
  'device-id': '<DEVICE_ID>',
  'peerid': '<PEER_ID>',
  'app-version': '26.10.2.6',
  'bundle-version': '26.10.2.6',
  'install-channel': '<install_channel>',
  'x-channel-id': '<install_channel>',
  'content-type': 'application/json',
  'user-agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) glacc/26.10.2.6',
};

const SIGN_KEY = '07f21229eab0bb7d7c4';
function guangshanSign(params) {
  const p = Object.assign({}, params);
  if (!p.key) p.key = SIGN_KEY;
  const str = Object.keys(p).sort().map((k, i) => (i === 0 ? '' : '&') + k + '=' + p[k]).join('');
  return crypto.createHash('md5').update(str).digest('hex').toLowerCase();
}

function req(method, path, bodyObj) {
  return new Promise((resolve, reject) => {
    let body = null;
    let pathWithQs = path;
    if (method === 'get' && bodyObj && Object.keys(bodyObj).length) {
      pathWithQs += '?' + Object.entries(bodyObj).map(([k, v]) => encodeURIComponent(k) + '=' + encodeURIComponent(v)).join('&');
    } else if (method === 'post') {
      body = JSON.stringify(bodyObj || {});
    }
    const r = https.request({
      host: 'api-xacc.xunlei.com',
      port: 443,
      path: pathWithQs,
      method: method.toUpperCase(),
      headers: Object.assign({}, HEADERS, body ? {'content-length': Buffer.byteLength(body)} : {}),
    }, (res) => {
      let data = '';
      res.on('data', (c) => (data += c));
      res.on('end', () => {
        let j = null;
        try { j = JSON.parse(data); } catch (e) {}
        resolve({ status: res.statusCode, data: j, raw: data });
      });
    });
    r.on('error', reject);
    r.setTimeout(15000, () => { r.destroy(new Error('timeout')); });
    if (body) r.write(body);
    r.end();
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const rnd = (a, b) => a + Math.floor(Math.random() * (b - a));

async function getConfigList() {
  const r = await req('get', EP.configList, {});
  console.log('[config/list]', r.status, JSON.stringify(r.data).slice(0, 500));
  return r.data && r.data.data;
}

async function postStats(advert_id, advert_type, event_type) {
  const C = {
    advert_id,
    advert_type,
    event_type,
    ts: Math.floor(Date.now() / 1000),
    count: 1,
    timestamp: Math.ceil(Date.now() / 1000),
  };
  C.sign = guangshanSign(C);
  const r = await req('post', EP.stats, C);
  console.log(`[stats ${event_type}]`, advert_id, r.status, JSON.stringify(r.data));
  return r.data && r.data.code;
}

async function getWallet() {
  const r = await req('get', EP.wallet, {});
  const d = r.data && r.data.data;
  if (d) console.log(`[wallet] score=${d.score} (~${(d.score / 80).toFixed(1)} min @80)  remain_acc_second=${d.remain_acc_second}`);
  else console.log('[wallet]', r.status, r.raw.slice(0, 200));
  return d;
}

(async () => {
  if (HEADERS.Authorization.includes('<')) {
    console.error('!! 请先在脚本里填入真实登录态 (DevTools 抓包)');
    process.exit(1);
  }
  const cfg = await getConfigList();
  const list = (cfg && (cfg.task_list || cfg.advert_list || cfg.list)) || [];
  if (!list.length) {
    console.error('config/list 为空 — 确认账号已登录且任务中心可见广告任务');
    process.exit(1);
  }
  const pending = list.filter((t) => Number(t.complete_status) === 1 || t.complete_status === undefined);
  console.log(`pending tasks: ${pending.length}/${list.length}`);

  const rounds = parseInt(process.env.ROUNDS || '3', 10);
  for (let i = 0; i < rounds; i++) {
    console.log(`--- round ${i + 1}/${rounds}`);
    for (const t of pending.slice(0, 3)) {
      const id = t.advert_id || t.id;
      const type = t.advert_type || t.type || '';
      for (const evt of ['expose', 'click', 'complete']) {
        const code = await postStats(id, type, evt);
        if (code === 0) console.log(`   OK  ${evt} ${id}`);
        else console.log(`   ERR ${evt} ${id} code=${code}`);
        await sleep(rnd(1500, 3500));
      }
    }
    await getWallet();
    await sleep(rnd(5000, 9000));
  }
  console.log('done. 轮询 get_user_wallet 确认 score 是否增加');
})();
