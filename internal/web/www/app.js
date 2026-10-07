/* TokenHub 面板（简体中文 / English） */
'use strict';

/* ───────────────────────── i18n ───────────────────────── */
const I18N = {
  zh: {
    nav_dashboard: '总览', nav_accounts: '账号', nav_api: 'API 接口', nav_usage: '用量', nav_logs: '运行日志', nav_settings: '设置',
    usage_title: '用量与占比', usage_sub: '账号额度余量与最近调用分布（本地环形日志，最多 500 条）',
    usage_today: '今日 tokens', usage_week: '近 7 天 tokens', usage_requests: '总请求数', usage_tracked: '统计自',
    usage_cache_hit: '缓存命中', usage_cache_read: '缓存读', usage_cache_write: '缓存写', usage_cache_short: '缓存', usage_in: '输入', usage_out: '输出',
    usage_quota_title: '账号额度与占比',
    usage_by_caller: '按调用方', usage_by_provider: '按提供商', usage_by_model: '按模型（Top 8）',
    usage_owner: '主人（主 Key）', usage_no_quota: '暂无额度快照，点账号「查额度」后展示', usage_total: '共',
    usage_until: '到期', usage_req_unit: ' 次', usage_empty: '暂无调用记录',
    usage_reload: '刷新', usage_reloading: '刷新中…',
    dash_title: '总览', dash_sub: '账号额度与接口状态一览',
    dash_refresh_quotas: '刷新全部额度', dash_checkin_all: '立即签到/领取',
    dash_refresh_tokens: '刷新全部token', refresh_tokens_busy: '正在刷新全部token…',
    refresh_tokens_ok: '全部刷新成功（{x} 个）', refresh_tokens_part: '刷新完成：成功 {x} 个，失败 {y} 个',
    local_sync_badge: '本机同步', local_sync_tip: '该账号无 refreshToken：TokenHub 会在 token 临期时自动重读本机客户端会话续期；若本机客户端也未登录该账号，token 过期后需重新登录',
    dash_quick_api: '接口速览', api_base_url: '接口地址（Base URL）',
    dash_api_hint: '在任意支持 OpenAI / Anthropic 协议的客户端中填入上面的 Base URL 与 API Key 即可使用。模型可用 "provider:model" 前缀指定提供商，如 trae:glm-5.2。',
    acct_title: '账号管理', acct_sub: '多账号登录、额度查看与自动领取',
    api_title: 'API 接口', api_sub: 'OpenAI / Anthropic 兼容端点与接入示例',
    api_compat: '兼容端点：POST /v1/chat/completions（OpenAI）、POST /v1/messages（Anthropic）、GET /v1/models。鉴权方式：Authorization: Bearer <API Key>',
    api_models: '可用模型', reload: '刷新',
    lan_label: '局域网分享地址（同一 WiFi 下的人可直接调用）',
    lan_hint_on: '把这个地址 + 下面的 API Key 发给对方，在他的客户端里填上即可调用。⚠️ 对方消耗的是你的账号额度，请只分享给信任的人。',
    lan_hint_off: '当前监听地址为 127.0.0.1，仅本机可用。想给别人调用：到「设置」把监听地址改为 0.0.0.0 保存并重启程序，Windows 防火墙首次弹窗时点「允许访问」。',
    /* 公网分享 */
    pub_label: '公网分享地址（互联网上任何地方都能调用）',
    pub_btn_on: '开启公网分享', pub_btn_off: '关闭公网分享',
    pub_st_off: '未开启。开启后会生成一个 bore.pub 公网地址（每次开启端口随机变化）。',
    pub_st_starting: '正在连接公网中继…（首次连接可能需要 10~30 秒，请稍候）',
    pub_st_up: '已开启 ✅ 任何人都可以用这个地址调用（需配合下方分享钥匙）。',
    pub_st_error: '开启失败：',
    pub_hint: '🔒 安全说明：公网只暴露 API 接口，管理面板与账号凭据不会公开；隧道仅做出站连接，不在本机开放任何端口。对方必须带分享钥匙的 Key 才能调用。',
    fmt_oai_base: 'OpenAI · 基础地址（自动拼 /v1）',
    fmt_oai_v1: 'OpenAI · 以 /v1 结尾',
    fmt_oai_full: 'OpenAI · 完整端点 /v1/chat/completions',
    fmt_ant_full: 'Anthropic · 完整端点 /v1/messages',
    fmt_models: '模型列表 /v1/models',
    /* 用量日志 */
    usage_title: '用量日志',
    usage_hint: '每次 API 调用的 token 消耗记录（保留最近 500 条，含失败请求）。分享钥匙的调用会显示钥匙名称。',
    usage_time: '时间', usage_caller: '来源', usage_prov: '提供商', usage_model: '模型',
    usage_tokens: 'tokens（问+答）', usage_dur: '耗时', usage_status: '状态',
    usage_owner: '主人（主密钥）', usage_ok: '成功', usage_fail: '失败',
    usage_empty: '还没有调用记录，去发一条消息试试',
    /* 分享钥匙 */
    share_title: '分享钥匙（发给别人的专用 Key）',
    share_hint: '每把钥匙可按提供商开关并设用量上限（按 token 计量；上限 0 = 不限）。WorkBuddy / Trae 的「积分」无法逐次读取，用 token 用量近似控制，建议设保守值。主 API Key 请勿外发，发这把专用钥匙即可。',
    share_create: '创建分享钥匙', share_name_ph: '名称（比如：给小明的）',
    share_limit_wb: '积分上限', share_limit_trae: '积分上限', share_limit_zc: 'tokens 上限',
    share_models_ph: '允许模型（逗号分隔，留空=全部）',
    share_none: '还没有分享钥匙，在下方创建一把',
    share_used: '已用', share_times: '次',
    share_del_confirm: '删除这把分享钥匙？用它的对方会立即无法调用。',
    share_created: '分享钥匙已创建',
    logs_title: '运行日志', logs_sub: '实时日志（最近 600 条）', logs_clear: '清空显示',
    set_title: '设置', set_sub: '修改后点击保存；监听地址与端口需重启生效',
    save: '保存', cancel: '取消', close: '关闭', confirm: '确认',
    prov_workbuddy: 'WorkBuddy / CodeBuddy', prov_trae: 'Trae', prov_zcode: 'ZCode（智谱 GLM）',
    prov_off: '已关闭',
    accounts: '账号', active: '可用', remain: '剩余额度',
    acct_add: '添加账号', acct_login: '登录添加', acct_manual: '手动导入', acct_import_local: '从本机 ZCode 导入',
    acct_none: '还没有账号，点击右上角「添加账号」开始',
    st_on: '正常', st_off: '已停用', st_dead: '凭据失效', st_cooling: '冷却中', st_expiring: 'token 将过期',
    token_expiry: 'token 到期', last_checkin: '最近签到', never: '—',
    act_quota: '查额度', act_checkin: '签到/领取', act_refresh: '刷新token', act_enable: '启用', act_disable: '停用', act_delete: '删除',
    act_copy_rt: '复制RT', no_rt: '该账号没有 refreshToken（可能来自本机导入）',
    act_switch_ide: '切入Trae', act_switch_ide_tip: '关闭并重启本机 Trae 客户端，切换为该账号登录；切换前当前登录会话会自动存回账号池',
    act_capture: '抓会话', act_capture_tip: '把本机 Trae 当前的登录会话（登录信封+最新token）抓取进该账号',
    confirm_switch_ide: '将关闭并重启本机 Trae 客户端，切换为该账号登录。\n\n· 切换前当前登录会话会自动保存回账号池\n· Trae 内未保存的工作请先手动保存\n\n继续？',
    act_raw: '原始数据', raw_title: '额度原始数据（上游各接口返回）',
    confirm_delete: '确定删除该账号？其凭据将从本机移除。',
    checkin_ok: '签到/领取完成', quota_ok: '额度已刷新', saved_ok: '已保存', copied: '已复制',
    quota_busy: '正在刷新额度…', checkin_busy: '正在签到/领取…',
    no_quota: '未查询', op_fail: '操作失败',
    /* 登录流程 */
    login_wb_title: '添加 WorkBuddy / CodeBuddy 账号', login_wb_realm: '选择站点',
    realm_cn: '国内版（CodeBuddy）', realm_global: '国际版（WorkBuddy）',
    login_wb_s1: '1. 点击下方按钮打开授权页面并登录；', login_wb_s2: '2. 授权后本页面会自动完成添加（最多等 5 分钟）。',
    open_auth: '打开授权页面', waiting: '等待授权中…', login_ok: '添加成功', login_fail: '失败',
    login_trae_title: '添加 Trae 账号',
    login_trae_s1: '1. 点击下方按钮打开 TRAE 授权页（手机扫码或手机号登录）；',
    login_trae_s2: '2. 登录成功后浏览器会跳转到 127.0.0.1 页面，稍候本面板会自动完成添加；',
    login_trae_s3: '3. 若浏览器提示“无法访问”，把地址栏完整链接粘贴到下方输入框，点「提交回调链接」即可。',
    paste_callback: '粘贴回调链接（可选）', submit_callback: '提交回调链接',
    login_zc_title: '添加 ZCode 账号', login_zc_provider: '选择渠道',
    zc_bigmodel: '智谱 BigModel（中国）', zc_zai: 'Z.ai（国际）',
    login_zc_s1: '1. 选择渠道并点击「打开授权页面」，在浏览器中登录智谱 / Z.ai 账号；',
    login_zc_s2: '2. 授权完成后本面板会自动获取凭据（最多等 10 分钟）。',
    import_local_desc: '读取本机 ZCode 客户端（~/.zcode/v2/credentials.json）当前登录的账号，解密后导入。适合快速导入已在 ZCode 登录的账号。',
    import_local_ok: '本机导入成功', import_local_none: '未检测到本机登录',
    import_wb_btn: '从本机 CodeBuddy 导入', import_trae_btn: '从本机 Trae 导入',
    manual_title: '手动导入凭据', manual_provider: '提供商', manual_realm: '渠道（workbuddy: cn/global；zcode: bigmodel/zai）',
    manual_access: 'AccessToken（ZCode 填 zcode JWT）', manual_refresh: 'RefreshToken（可选，trae/workbuddy 可自动续期）',
    manual_uid: 'UID（可选）', manual_nick: '备注名（可选）',
    /* ZCode 领取 */
    act_switch_ide: '切入Trae', act_capture: '抓会话', act_migrate: '迁会话',
    act_switch_ide_tip: '把本机 Trae 切换为该账号登录（当前登录会自动入库，不丢号）',
    act_capture_tip: '把本机 Trae 当前登录抓取到该账号',
    act_migrate_tip: '把本机 Trae 当前账号的 AI 会话迁移给该账号（需数据库密钥）',
    mig_title: 'Trae AI 会话迁移',
    mig_desc: '把本机 Trae 当前登录账号的全部 AI 对话会话迁移给目标账号（迁移后自动切到目标账号并重启 Trae）。',
    mig_key_label: 'AI 库 SQLCipher 密钥（64 位 hex）',
    mig_key_saved: '已保存',
    mig_key_missing: '未配置',
    mig_key_ph: '粘贴 64 位 hex 密钥（留空使用已保存的密钥）',
    mig_warn: '⚠️ 迁移会改写 Trae 数据库（自动备份 .bak-时间戳）；请先正常退出 Trae 再执行；目标即当前登录时无需迁移。',
    mig_start: '开始迁移',
    mig_done: '迁移完成：迁入 {migrated} 个会话（目标原生 {native} 个），已切换登录。',
    mig_fail: '迁移失败',
    zc_plans: '可领取活动', zc_claim: '领取', zc_no_plans: '当前没有可领取的活动', zc_claiming: '领取中…',
    zc_claim_ok: '领取成功', zc_claim_already: '之前已领取，无需重复', zc_captcha_title: '安全验证', zc_captcha_hint: '该活动需要滑块验证，完成验证后将自动重试领取', zc_captcha_load_fail: '验证码组件加载失败，请检查网络后重试', zc_captcha_retry: '重试',
    zc_auto_claim: '🤖 一键自动领取', zc_auto_running: '自动滑块验证中，请勿移动鼠标…', zc_auto_fail: '自动滑块未通过，请手动完成验证',
    zc_pending_title: '有 {n} 个活动待验证领取（自动领取遇到滑块验证）', set_zcode_shape_hint: 'overwrite=用客户端请求形状整体替换 system（实测最稳）；prepend=前置注入；off=不注入',
    /* 设置项 */
    set_listen_host: '监听地址', set_listen_host_d: '127.0.0.1 仅本机可用；0.0.0.0 允许局域网（需重启）',
    set_listen_port: '端口', set_api_key: 'API Key', set_regen_key: '重新生成',
    set_default_provider: '默认提供商', set_auto_checkin: '每日自动签到',
    set_auto_checkin_d: 'WorkBuddy 国内版每日签到、Trae 每日签到（每天 200 积分）',
    set_wb_keepalive: 'WorkBuddy 国际版保活', set_wb_keepalive_d: '国际版无签到接口，每天用一条极小对话保持活跃',
    set_zcode_claim: 'ZCode 活动自动领取', set_zcode_claim_d: '轮询可领取活动套餐并自动领取（免费额度每日自动发放，无需领取）',
    set_zcode_interval: 'ZCode 领取轮询周期（分钟）',
    set_zcode_shape: 'ZCode 请求形状注入', set_zcode_shape_d: 'overwrite=整体替换 system（实测最稳）；prepend=前置注入；off=关闭',
    set_quota_refresh: '额度定时刷新（秒，可填 0.5，0=关闭）',
    set_trae_switch: 'Trae 积分耗尽自动换号',
    set_trae_switch_d: '需开启额度定时刷新。刷新时发现某 Trae 账号积分耗尽，自动切换到其他账号（无需等请求报错）；积分恢复后自动回归',
    set_refresh_skew: 'token 提前刷新（分钟）',
    set_expose_prefix: '模型列表附带 provider:model 变体',
    set_refresh_skew: 'token 提前刷新（分钟）',
    /* API 示例 */
    ex_curl: '# OpenAI 格式\ncurl {{BASE}}/v1/chat/completions \\\n  -H "Authorization: Bearer {{KEY}}" \\\n  -H "Content-Type: application/json" \\\n  -d \'{\n    "model": "{{MODEL}}",\n    "stream": true,\n    "messages": [{"role": "user", "content": "你好"}]\n  }\'',
    ex_cherry: 'Cherry Studio → 设置 → 模型服务商 → 添加 OpenAI 兼容服务商：\n\n  API 地址:  {{BASE}}\n  API 密钥:  {{KEY}}\n  模型:      手动添加（见下方模型列表）\n\n或 Anthropic 服务商：\n\n  API 地址:  {{BASE}}\n  API 密钥:  {{KEY}}',
    ex_claude: '# Claude Code（环境变量方式）\nset ANTHROPIC_BASE_URL={{BASE}}\nset ANTHROPIC_AUTH_TOKEN={{KEY}}\nclaude\n\n# 或写入 ~/.claude/settings.json：\n{\n  "env": {\n    "ANTHROPIC_BASE_URL": "{{BASE}}",\n    "ANTHROPIC_AUTH_TOKEN": "{{KEY}}"\n  }\n}',
    ex_cline: 'Cline / Roo Code → Provider 选 "OpenAI Compatible"：\n\n  Base URL:  {{BASE}}/v1\n  API Key:   {{KEY}}\n  Model:     {{MODEL}}',
    ex_sdk: 'from openai import OpenAI\n\nclient = OpenAI(\n    base_url="{{BASE}}/v1",\n    api_key="{{KEY}}",\n)\nresp = client.chat.completions.create(\n    model="{{MODEL}}",\n    messages=[{"role": "user", "content": "Hello"}],\n)\nprint(resp.choices[0].message.content)',
    ex_anthropic: 'from anthropic import Anthropic\n\nclient = Anthropic(\n    base_url="{{BASE}}",\n    api_key="{{KEY}}",\n)\nmsg = client.messages.create(\n    model="{{MODEL}}",\n    max_tokens=4096,\n    messages=[{"role": "user", "content": "Hello"}],\n)\nprint(msg.content)',
    logs_empty: '暂无日志',
  },
  en: {
    nav_dashboard: 'Dashboard', nav_accounts: 'Accounts', nav_api: 'API', nav_usage: 'Usage', nav_logs: 'Logs', nav_settings: 'Settings',
    usage_title: 'Usage & Quotas', usage_sub: 'Account quota remainder and recent call distribution (local ring buffer, max 500 entries)',
    usage_today: 'Today tokens', usage_week: '7-day tokens', usage_requests: 'Total requests', usage_tracked: 'from',
    usage_cache_hit: 'Cache hit', usage_cache_read: 'cache read', usage_cache_write: 'cache write', usage_cache_short: 'cache', usage_in: 'in', usage_out: 'out',
    usage_quota_title: 'Account quota & ratio',
    usage_by_caller: 'By caller', usage_by_provider: 'By provider', usage_by_model: 'By model (Top 8)',
    usage_owner: 'Owner (master key)', usage_no_quota: 'No quota snapshot yet; click "Quota" on an account first', usage_total: 'total',
    usage_until: 'until', usage_req_unit: ' req', usage_empty: 'No calls recorded',
    usage_reload: 'Refresh', usage_reloading: 'Refreshing…',
    dash_title: 'Dashboard', dash_sub: 'Account quotas and API status at a glance',
    dash_refresh_quotas: 'Refresh quotas', dash_checkin_all: 'Check in / claim now',
    dash_refresh_tokens: 'Refresh all tokens', refresh_tokens_busy: 'Refreshing all tokens…',
    refresh_tokens_ok: 'All refreshed ({x})', refresh_tokens_part: 'Done: {x} ok, {y} failed',
    local_sync_badge: 'local sync', local_sync_tip: 'No refreshToken for this account: TokenHub re-reads the local client session to renew before expiry; if the local client is not logged in either, re-login is required after the token expires',
    dash_quick_api: 'Quick API', api_base_url: 'Base URL',
    dash_api_hint: 'Fill the Base URL and API Key above into any OpenAI / Anthropic compatible client. Use "provider:model" prefixes to pick a provider, e.g. trae:glm-5.2.',
    acct_title: 'Accounts', acct_sub: 'Multi-account login, quota view and auto claiming',
    api_title: 'API', api_sub: 'OpenAI / Anthropic compatible endpoints & examples',
    api_compat: 'Endpoints: POST /v1/chat/completions (OpenAI), POST /v1/messages (Anthropic), GET /v1/models. Auth: Authorization: Bearer <API Key>',
    api_models: 'Models', reload: 'Reload',
    lan_label: 'LAN share address (reachable by others on the same network)',
    lan_hint_on: 'Send this address plus the API Key below to the other person. Warning: they will consume YOUR account quota — share only with people you trust.',
    lan_hint_off: 'Currently listening on 127.0.0.1 (local only). To let others call the API: set Listen host to 0.0.0.0 in Settings, save and restart, then allow it through the Windows firewall prompt.',
    /* Public sharing */
    pub_label: 'Public share URL (callable from anywhere on the internet)',
    pub_btn_on: 'Start public share', pub_btn_off: 'Stop public share',
    pub_st_off: 'Off. When started, a bore.pub URL is generated (random port each time).',
    pub_st_starting: 'Connecting to the public relay… (first connect may take 10-30 s)',
    pub_st_up: 'Active ✅ Anyone can call this URL (must use a share key below).',
    pub_st_error: 'Failed: ',
    pub_hint: '🔒 Security: only the API is exposed — the panel and credentials stay private; the tunnel makes outbound connections only, no inbound ports opened. Others must present a share key to call.',
    fmt_oai_base: 'OpenAI · Base URL (auto /v1)',
    fmt_oai_v1: 'OpenAI · ends with /v1',
    fmt_oai_full: 'OpenAI · full endpoint /v1/chat/completions',
    fmt_ant_full: 'Anthropic · full endpoint /v1/messages',
    fmt_models: 'Model list /v1/models',
    /* usage log */
    usage_title: 'Usage Log',
    usage_hint: 'Token consumption per API call (last 500 kept, including failed ones). Calls via a share key show its name.',
    usage_time: 'Time', usage_caller: 'Caller', usage_prov: 'Provider', usage_model: 'Model',
    usage_tokens: 'Tokens (in+out)', usage_dur: 'Took', usage_status: 'Status',
    usage_owner: 'Owner (main key)', usage_ok: 'OK', usage_fail: 'Failed',
    usage_empty: 'No calls yet — send a message to try it',
    /* Share keys */
    share_title: 'Share keys (dedicated keys for others)',
    share_hint: 'Each key can toggle providers and cap usage (measured in tokens; 0 = unlimited). WorkBuddy / Trae points cannot be read per-request, so token usage is used as an approximation — set conservative limits. Never send out your main API Key; send a share key instead.',
    share_create: 'Create share key', share_name_ph: 'Name (e.g. for Alice)',
    share_limit_wb: 'points cap', share_limit_trae: 'points cap', share_limit_zc: 'tokens cap',
    share_models_ph: 'Allowed models (comma-separated, empty = all)',
    share_none: 'No share keys yet — create one below',
    share_used: 'Used', share_times: ' reqs',
    share_del_confirm: 'Delete this share key? The holder will lose access immediately.',
    share_created: 'Share key created',
    logs_title: 'Logs', logs_sub: 'Live logs (last 600 entries)', logs_clear: 'Clear view',
    set_title: 'Settings', set_sub: 'Click Save after changes; host/port need a restart',
    save: 'Save', cancel: 'Cancel', close: 'Close', confirm: 'OK',
    prov_workbuddy: 'WorkBuddy / CodeBuddy', prov_trae: 'Trae', prov_zcode: 'ZCode (GLM)',
    prov_off: 'disabled',
    accounts: 'Accounts', active: 'Active', remain: 'Remaining',
    acct_add: 'Add account', acct_login: 'Login', acct_manual: 'Manual import', acct_import_local: 'Import from local ZCode',
    acct_none: 'No accounts yet. Click "Add account" to start.',
    st_on: 'OK', st_off: 'Disabled', st_dead: 'Credential dead', st_cooling: 'Cooling down', st_expiring: 'Token expiring',
    token_expiry: 'Token expiry', last_checkin: 'Last check-in', never: '—',
    act_quota: 'Quota', act_checkin: 'Check-in', act_refresh: 'Refresh token', act_enable: 'Enable', act_disable: 'Disable', act_delete: 'Delete',
    act_copy_rt: 'Copy RT', no_rt: 'No refreshToken for this account (may come from local import)',
    act_switch_ide: 'Use in Trae', act_switch_ide_tip: 'Restart the local Trae client logged in as this account; the current login session is saved back to the pool first',
    act_capture: 'Capture session', act_capture_tip: 'Capture the current local Trae login session (auth envelope + fresh token) into this account',
    confirm_switch_ide: 'The local Trae client will be closed and restarted as this account.\n\n· The current login session is saved back to the pool first\n· Save your unsaved work in Trae first\n\nContinue?',
    act_raw: 'Raw data', raw_title: 'Raw quota sources (upstream responses)',
    confirm_delete: 'Delete this account? Its credentials will be removed from this machine.',
    checkin_ok: 'Check-in / claim finished', quota_ok: 'Quota refreshed', saved_ok: 'Saved', copied: 'Copied',
    quota_busy: 'Refreshing quotas…', checkin_busy: 'Running check-in…',
    no_quota: 'Not queried', op_fail: 'Operation failed',
    login_wb_title: 'Add WorkBuddy / CodeBuddy account', login_wb_realm: 'Site',
    realm_cn: 'China (CodeBuddy)', realm_global: 'International (WorkBuddy)',
    login_wb_s1: '1. Click the button below to open the authorization page and sign in;',
    login_wb_s2: '2. The account will be added automatically after authorization (up to 5 min).',
    open_auth: 'Open authorization page', waiting: 'Waiting for authorization…', login_ok: 'Added', login_fail: 'Failed',
    login_trae_title: 'Add Trae account',
    login_trae_s1: '1. Click the button below to open the TRAE authorization page (QR or phone login);',
    login_trae_s2: '2. After login the browser redirects to a 127.0.0.1 page and the panel completes automatically;',
    login_trae_s3: '3. If the browser shows "site unreachable", paste the full address-bar URL below and submit.',
    paste_callback: 'Paste callback URL (optional)', submit_callback: 'Submit callback URL',
    login_zc_title: 'Add ZCode account', login_zc_provider: 'Channel',
    zc_bigmodel: 'Zhipu BigModel (China)', zc_zai: 'Z.ai (International)',
    login_zc_s1: '1. Pick a channel and click "Open authorization page", then sign in to Zhipu / Z.ai;',
    login_zc_s2: '2. Credentials are fetched automatically once authorized (up to 10 min).',
    import_local_desc: 'Reads the currently logged-in account of the local ZCode client (~/.zcode/v2/credentials.json), decrypts and imports it.',
    import_local_ok: 'Imported from local client', import_local_none: 'No local login detected',
    import_wb_btn: 'Import from local CodeBuddy', import_trae_btn: 'Import from local Trae',
    manual_title: 'Manual import', manual_provider: 'Provider', manual_realm: 'Realm (workbuddy: cn/global; zcode: bigmodel/zai)',
    manual_access: 'AccessToken (ZCode: zcode JWT)', manual_refresh: 'RefreshToken (optional; trae/workbuddy auto-renew)',
    manual_uid: 'UID (optional)', manual_nick: 'Display name (optional)',
    act_switch_ide: 'Switch IDE', act_capture: 'Capture', act_migrate: 'Migrate',
    act_switch_ide_tip: 'Switch local Trae to this account (current login is saved back first)',
    act_capture_tip: 'Capture current local Trae login into this account',
    act_migrate_tip: 'Migrate local Trae AI sessions to this account (requires DB key)',
    mig_title: 'Trae AI Session Migration',
    mig_desc: 'Migrate all AI chat sessions of the current local Trae login to the target account (auto-switches login and restarts Trae afterwards).',
    mig_key_label: 'AI DB SQLCipher key (64-hex)',
    mig_key_saved: 'saved',
    mig_key_missing: 'not set',
    mig_key_ph: 'Paste 64-hex key (leave empty to use saved key)',
    mig_warn: '⚠️ This rewrites the Trae database (auto backup .bak-*); quit Trae normally first; no-op if target is current login.',
    mig_start: 'Start migration',
    mig_done: 'Done: migrated {migrated} sessions (target native {native}), login switched.',
    mig_fail: 'Migration failed',
    zc_plans: 'Claimable campaigns', zc_claim: 'Claim', zc_no_plans: 'No claimable campaign right now', zc_claiming: 'Claiming…',
    zc_claim_ok: 'Claimed', zc_claim_already: 'Already claimed earlier, no need to repeat', zc_captcha_title: 'Security check', zc_captcha_hint: 'This campaign requires a slider captcha; claiming retries automatically after verification', zc_captcha_load_fail: 'Failed to load captcha component, check your network and retry', zc_captcha_retry: 'Retry',
    zc_auto_claim: '🤖 Auto claim', zc_auto_running: 'Auto sliding, please do not move the mouse…', zc_auto_fail: 'Auto slide failed; please finish it manually',
    zc_pending_title: '{n} campaign(s) awaiting slider verification (auto-claim hit captcha)', set_zcode_shape_hint: 'overwrite=replace system with client request shape (most reliable); prepend=prepend; off=disable',
    set_listen_host: 'Listen host', set_listen_host_d: '127.0.0.1 for local only; 0.0.0.0 for LAN (restart required)',
    set_listen_port: 'Port', set_api_key: 'API Key', set_regen_key: 'Regenerate',
    set_default_provider: 'Default provider', set_auto_checkin: 'Daily auto check-in',
    set_auto_checkin_d: 'WorkBuddy CN daily check-in and Trae daily check-in (+200 credits/day)',
    set_wb_keepalive: 'WorkBuddy intl keep-alive', set_wb_keepalive_d: 'Intl has no check-in API; keep active with one tiny chat per day',
    set_zcode_claim: 'ZCode campaign auto-claim', set_zcode_claim_d: 'Poll claimable campaign plans and claim automatically (daily free quota is granted automatically)',
    set_zcode_interval: 'ZCode claim poll interval (minutes)',
    set_zcode_shape: 'ZCode request-shape injection', set_zcode_shape_d: 'overwrite=replace system with client shape (most reliable); prepend=prepend; off=disable',
    set_quota_refresh: 'Quota auto refresh (seconds, 0.5 allowed, 0=off)',
    set_trae_switch: 'Auto-switch when Trae credits exhausted',
    set_trae_switch_d: 'Requires quota auto refresh. When a Trae account runs out of credits, it is switched out automatically (no need to wait for a failed request); it returns once credits recover',
    set_refresh_skew: 'Refresh token skew (minutes)',
    set_expose_prefix: 'Expose provider:model variants in model list',
    set_refresh_skew: 'Refresh token skew (minutes)',
    ex_curl: '# OpenAI style\ncurl {{BASE}}/v1/chat/completions \\\n  -H "Authorization: Bearer {{KEY}}" \\\n  -H "Content-Type: application/json" \\\n  -d \'{\n    "model": "{{MODEL}}",\n    "stream": true,\n    "messages": [{"role": "user", "content": "Hi"}]\n  }\'',
    ex_cherry: 'Cherry Studio → Settings → Provider → Add OpenAI-compatible provider:\n\n  API Host:  {{BASE}}\n  API Key:   {{KEY}}\n  Models:    add manually (see model list below)\n\nOr as Anthropic provider:\n\n  API Host:  {{BASE}}\n  API Key:   {{KEY}}',
    ex_claude: '# Claude Code (env vars)\nset ANTHROPIC_BASE_URL={{BASE}}\nset ANTHROPIC_AUTH_TOKEN={{KEY}}\nclaude\n\n# or ~/.claude/settings.json:\n{\n  "env": {\n    "ANTHROPIC_BASE_URL": "{{BASE}}",\n    "ANTHROPIC_AUTH_TOKEN": "{{KEY}}"\n  }\n}',
    ex_cline: 'Cline / Roo Code → Provider "OpenAI Compatible":\n\n  Base URL:  {{BASE}}/v1\n  API Key:   {{KEY}}\n  Model:     {{MODEL}}',
    ex_sdk: 'from openai import OpenAI\n\nclient = OpenAI(\n    base_url="{{BASE}}/v1",\n    api_key="{{KEY}}",\n)\nresp = client.chat.completions.create(\n    model="{{MODEL}}",\n    messages=[{"role": "user", "content": "Hello"}],\n)\nprint(resp.choices[0].message.content)',
    ex_anthropic: 'from anthropic import Anthropic\n\nclient = Anthropic(\n    base_url="{{BASE}}",\n    api_key="{{KEY}}",\n)\nmsg = client.messages.create(\n    model="{{MODEL}}",\n    max_tokens=4096,\n    messages=[{"role": "user", "content": "Hello"}],\n)\nprint(msg.content)',
    logs_empty: 'No logs yet',
  }
};

let LANG = localStorage.getItem('th_lang') || 'zh';
function t(key) { return (I18N[LANG] && I18N[LANG][key]) || I18N.zh[key] || key; }

function applyI18n() {
  document.documentElement.lang = LANG === 'zh' ? 'zh-CN' : 'en';
  document.querySelectorAll('[data-i18n]').forEach(el => { el.textContent = t(el.dataset.i18n); });
  document.getElementById('langSel').value = LANG;
  renderProviders();
  renderAccounts();
  renderExamples();
  renderSettings();
  renderModels(modelCache);
  const sc = $('#shareCreate');
  if (sc) sc.dataset.built = '';
  renderShares();
  updatePubUI();
}

/* ───────────────────────── helpers ───────────────────────── */
const $ = sel => document.querySelector(sel);
const $$ = sel => Array.from(document.querySelectorAll(sel));

function toast(msg, isErr) {
  const el = $('#toast');
  el.textContent = msg;
  if (isErr) el.style.color = 'var(--red)'; else el.style.color = '';
  el.classList.add('show');
  clearTimeout(el._t);
  el._t = setTimeout(() => el.classList.remove('show'), 2600);
}

async function api(path, opts) {
  const res = await fetch(path, opts);
  let body = null;
  try { body = await res.json(); } catch {}
  if (!res.ok) {
    const msg = (body && body.error) ? body.error : ('HTTP ' + res.status);
    throw new Error(msg);
  }
  return body;
}

function post(path, data) {
  return api(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data || {}) });
}

// copyText 复制到剪贴板；127.0.0.1 下用 clipboard API，
// 局域网 http 无 secure context，降级为 execCommand。
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    ta.remove();
  }
}

/* ───────────────── ZCode 活动领取（含验证码重试） ───────────────── */

// zcClaim 领取一个套餐；返回 'ok' | 'captcha'（需验证码） | 'fail'（其他错误，已 toast）
async function zcClaim(a, planId, captchaParam, region) {
  try {
    const res = await post('/api/panel/zcode/claim', { id: a.id, planId, captchaParam, region });
    toast((res.planName || planId) + ' · ' + (res.already ? t('zc_claim_already') : t('zc_claim_ok')));
    await loadOverview();
    return 'ok';
  } catch (e) {
    if (/3007|3012|验证码|captcha/i.test(e.message)) return 'captcha';
    toast(e.message, true);
    return 'fail';
  }
}

// loadAliyunCaptcha 加载官方验证码 SDK：优先本机内置版（同源 /captcha.js，随 exe 分发，不依赖外网），
// 失败再依次尝试阿里云 CDN。
function loadAliyunCaptcha() {
  const urls = ['/captcha.js',
    'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js',
    'https://g.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js'];
  return new Promise((resolve, reject) => {
    if (window.initAliyunCaptcha) return resolve();
    let i = 0;
    const tryNext = () => {
      if (i >= urls.length) return reject(new Error('all captcha sources failed'));
      const s = document.createElement('script');
      s.src = urls[i++];
      s.onload = () => {
        if (window.initAliyunCaptcha) resolve();
        else { s.remove(); tryNext(); } // 200 空响应等异常：SDK 未定义则换下一个源
      };
      s.onerror = () => { s.remove(); tryNext(); };
      document.head.appendChild(s);
    };
    tryNext();
  });
}

// zcCaptchaModal 渲染阿里云滑块验证码（官方 SDK，popup 模式），通过后带参重试领取。
// auto=true 时先尝试 SendInput 自动滑块（仅窗口模式），失败回落手动。
// 同账号同活动同时只允许一个弹窗：定时任务派发的弹窗与手动领取弹窗可能撞车，
// 后开的不重复弹（复用同一 Promise），关窗时自动清理。
// 返回 Promise<{ok:boolean}>。
function zcCaptchaModal(a, planId, cfg, auto) {
  const key = ((a && a.id) || '') + '|' + planId;
  window._zcCaptchaModals = window._zcCaptchaModals || {};
  if (window._zcCaptchaModals[key]) return window._zcCaptchaModals[key];
  const pr = zcCaptchaModalRun(a, planId, cfg, auto);
  window._zcCaptchaModals[key] = pr;
  const cleanup = () => { delete window._zcCaptchaModals[key]; };
  pr.then(cleanup, cleanup);
  return pr;
}

function zcCaptchaModalRun(a, planId, cfg, auto) {
  const pick = (...keys) => { for (const k of keys) { if (cfg[k]) return cfg[k]; } return ''; };
  const scene = pick('sceneId', 'SceneId', 'scene', 'scene_id');
  const prefix = pick('prefix', 'Prefix') || 'aliyunCaptcha';
  const region = pick('region', 'Region', 'captchaRegion');
  return new Promise(async (resolve) => {
    showModal(t('zc_captcha_title'), `
      <div style="margin-bottom:10px" id="captchaHint">${t('zc_captcha_hint')}</div>
      <div id="captcha-element"></div>
      <div class="foot"><button id="captcha-button">${t('zc_claim')}</button></div>
      <div id="captchaErr" style="display:none;text-align:center;padding:6px 0">
        <span id="captchaErrMsg" style="color:var(--danger,#e5615c);font-size:13px"></span>
        <button class="sm" id="captchaRetry" style="margin-left:8px">${t('zc_captcha_retry')}</button>
      </div>`);
    const finish = (ok) => { closeModal(); resolve({ ok }); };
    const hint = (msg) => { const el = $('#captchaHint'); if (el) el.textContent = msg; };
    const sleep = (ms) => new Promise(r => setTimeout(r, ms));
    const findCaptchaIframe = () => {
      for (const f of document.querySelectorAll('iframe')) {
        if (/captcha|aliyun/i.test(f.src || '')) return f;
      }
      return null;
    };
    // autoSlideOnce：点开弹窗 → 定位滑块 iframe → 算屏幕物理坐标 → 后端 SendInput 拟人拖拽
    const autoSlideOnce = async () => {
      let ifr = findCaptchaIframe();
      if (!ifr) {
        const btn = $('#captcha-button');
        if (btn) btn.click();
        for (let k = 0; k < 20 && !ifr; k++) { await sleep(250); ifr = findCaptchaIframe(); }
      }
      if (!ifr) return false;
      const r = ifr.getBoundingClientRect();
      const dpr = window.devicePixelRatio || 1;
      const x1 = Math.round((window.screenX + r.left + Math.max(24, r.width * 0.1)) * dpr);
      const y1 = Math.round((window.screenY + r.top + r.height * 0.5) * dpr);
      const x2 = Math.round((window.screenX + r.left + r.width - Math.max(26, r.width * 0.1)) * dpr);
      window._zcSlideDone = null;
      try { await post('/api/panel/zcode/slider-rect', { x1, y1, x2, y2: y1 }); } catch { return false; }
      for (let k = 0; k < 40; k++) { await sleep(250); if (window._zcSlideDone !== null) break; }
      return window._zcSlideDone === true;
    };
    const tryAutoSlide = async () => {
      hint(t('zc_auto_running'));
      for (let i = 0; i < 3; i++) {
        if (await autoSlideOnce()) return true;
        await sleep(1200); // SDK 会自动刷新出新的滑块
      }
      hint(t('zc_auto_fail'));
      return false;
    };
    const boot = async () => {
      $('#captchaErr').style.display = 'none';
      try {
        await loadAliyunCaptcha();
        window.initAliyunCaptcha({
          SceneId: scene, prefix, region, mode: 'popup',
          element: '#captcha-element', button: '#captcha-button',
          captchaVerifyCallback: async (captchaVerifyParam) => {
            const r = await zcClaim(a, planId, captchaVerifyParam, region);
            const ok = r === 'ok';
            window._zcSlideDone = ok;
            if (ok) finish(true);
            return { captchaResult: ok, bizResult: ok };
          },
          onBizResultCallback: () => {},
          getInstance: (ins) => { window._zcCaptcha = ins; },
        });
        if (auto) { setTimeout(() => { tryAutoSlide(); }, 400); }
      } catch (e) {
        $('#captchaErrMsg').textContent = t('zc_captcha_load_fail') + (e && e.message ? '（' + e.message + '）' : '');
        $('#captchaErr').style.display = 'block';
      }
    };
    $('#captchaRetry').addEventListener('click', boot);
    await boot();
  });
}

// zcAutoClaimPending 全自动领取：遍历待办（自动领取撞验证码的），逐个自动滑块。
// 后端撞到 3007 时会通过 webview 调用这里；横幅上的「🤖 一键自动领取」也走这里。
window.zcAutoClaimPending = async function () {
  if (window._zcAutoRunning) return;
  window._zcAutoRunning = true;
  try {
    const pend = await api('/api/panel/zcode/pending');
    for (const p of (pend.pending || [])) {
      try {
        // 开弹窗前再确认一次仍在待办（用户可能已在活动列表手动领掉），避免无谓弹滑块
        const now = await api('/api/panel/zcode/pending');
        if (!(now.pending || []).some(x => x.accountId === p.accountId && x.planId === p.planId)) continue;
        const cc = await api(`/api/panel/zcode/captcha-config?id=${encodeURIComponent(p.accountId)}`);
        await zcCaptchaModal({ id: p.accountId }, p.planId, cc.captcha || {}, true);
      } catch (e) { /* 单个失败继续下一个 */ }
    }
    await loadOverview();
  } finally { window._zcAutoRunning = false; }
};

// migrateSessionsModal Trae AI 会话迁移：密钥设置 → 启动 → 实时日志。
async function migrateSessionsModal(a) {
  const keyState = await api('/api/panel/trae/dbkey');
  showModal(t('mig_title'), `
    <div style="margin-bottom:8px;opacity:.75;font-size:13px">${t('mig_desc')}</div>
    <div class="field"><label>${t('mig_key_label')} ${keyState.set ? `<span style="opacity:.6">(${t('mig_key_saved')} ${esc(keyState.hint)})</span>` : `<span style="color:var(--danger,#e5615c)">${t('mig_key_missing')}</span>`}</label>
      <input id="migKey" type="password" placeholder="${t('mig_key_ph')}" style="width:100%">
    </div>
    <div style="margin:8px 0;color:var(--danger,#e5615c);font-size:12px">${t('mig_warn')}</div>
    <div class="foot"><button id="migStart" class="primary">${t('mig_start')}</button></div>
    <div id="migLog" class="codebox" style="max-height:40vh;overflow:auto;display:none;margin-top:8px;font-size:12px"></div>`);
  $('#migStart').addEventListener('click', async () => {
    const key = $('#migKey').value.trim();
    try {
      if (key) await post('/api/panel/trae/dbkey', { key });
      await post(`/api/panel/account/${a.id}/migrate-sessions`, {});
    } catch (e) { toast(e.message, true); return; }
    $('#migStart').disabled = true;
    const box = $('#migLog');
    box.style.display = 'block';
    let lastLen = -1;
    const timer = setInterval(async () => {
      try {
        const st = await api('/api/panel/trae/migrate-status');
        if (st.logs.length !== lastLen) {
          lastLen = st.logs.length;
          box.textContent = st.logs.join('\n');
          box.scrollTop = box.scrollHeight;
        }
        if (!st.running) {
          clearInterval(timer);
          if (st.result && st.result.ok) {
            box.textContent += '\n✅ ' + t('mig_done')
              .replace('{migrated}', st.result.migrated).replace('{native}', st.result.native);
            await loadOverview();
          } else {
            box.textContent += '\n❌ ' + (st.result && st.result.error ? st.result.error : t('mig_fail'));
          }
          $('#migStart').disabled = false;
        }
      } catch (e) { clearInterval(timer); toast(e.message, true); }
    }, 1000);
  });
}

// zcPlansModal 列出可领取活动并逐个领取；触发验证码时自动进入滑块流程。
async function zcPlansModal(a) {
  let plans = [];
  try {
    const data = await api(`/api/panel/zcode/plans?id=${encodeURIComponent(a.id)}`);
    plans = data.plans || [];
  } catch (e) {
    toast(e.message, true);
    return;
  }
  const rows = plans.length ? plans.map(pl => `
    <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;padding:8px 0;border-bottom:1px solid var(--border, #333)">
      <div>
        <div>${esc(pl.name || pl.planId)}</div>
        ${(pl.grants && pl.grants.length) ? `<div style="opacity:.6;font-size:12px">${esc(pl.grants.join('；'))}</div>` : ''}
      </div>
      <button class="sm" data-plan="${esc(pl.planId)}">${t('zc_claim')}</button>
    </div>`).join('') : `<div style="opacity:.6">${t('zc_no_plans')}</div>`;
  showModal(t('zc_plans'), `<div>${rows}</div>`);
  const resetBtn = (btn) => { btn.disabled = false; btn.textContent = t('zc_claim'); };
  $$('#addBody [data-plan]').forEach(btn => {
    btn.addEventListener('click', async () => {
      const planId = btn.dataset.plan;
      btn.disabled = true;
      btn.textContent = t('zc_claiming');
      const r = await zcClaim(a, planId, '', '');
      if (r === 'captcha') {
        resetBtn(btn);
        try {
          const cc = await api(`/api/panel/zcode/captcha-config?id=${encodeURIComponent(a.id)}`);
          await zcCaptchaModal(a, planId, cc.captcha || {}, !!(overview && overview.webviewWindow));
        } catch (e) {
          toast(t('zc_captcha_load_fail'), true);
        }
      } else if (r === 'ok') {
        closeModal();
      } else {
        resetBtn(btn);
      }
    });
  });
}
function patch(path, data) {
  return api(path, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data || {}) });
}
function del(path) { return api(path, { method: 'DELETE' }); }

function fmtNum(n) {
  if (n === undefined || n === null) return '0';
  n = Number(n);
  if (LANG === 'en') {
    if (Math.abs(n) >= 1e9) return (n / 1e9).toFixed(1) + 'B';
    if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (Math.abs(n) >= 1e3) return (n / 1e3).toFixed(1) + 'K';
    return String(Math.round(n * 100) / 100);
  }
  if (Math.abs(n) >= 1e8) return (n / 1e8).toFixed(1) + '亿';
  if (Math.abs(n) >= 1e4) return (n / 1e4).toFixed(1) + 'w';
  return String(Math.round(n * 100) / 100);
}
function fmtTime(unix) {
  if (!unix) return t('never');
  const d = new Date(unix * 1000);
  const p = x => String(x).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function copyText(txt) {
  navigator.clipboard.writeText(txt).then(() => toast(t('copied'))).catch(() => {});
}

/* ───────────────────────── tabs ───────────────────────── */
$$('.nav-item').forEach(item => {
  item.addEventListener('click', () => {
    $$('.nav-item').forEach(i => i.classList.remove('active'));
    item.classList.add('active');
    $$('.page').forEach(p => p.classList.remove('active'));
    $('#page-' + item.dataset.page).classList.add('active');
    if (item.dataset.page === 'logs') startLogStream();
    if (item.dataset.page === 'usage') loadUsage();
  });
});

/* ───────────────────────── 用量页 ───────────────────────── */
function fmtTokens(n) {
  n = Number(n) || 0;
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}
function usageBar(used, total, cls) {
  const pct = total > 0 ? Math.min(100, used / total * 100) : 0;
  const color = pct >= 90 ? 'var(--danger,#e5615c)' : pct >= 70 ? '#e6a23c' : 'var(--ok,#39d98a)';
  return `<div style="background:var(--card2,#222);border-radius:6px;height:8px;overflow:hidden;margin-top:6px">
    <div style="width:${pct.toFixed(1)}%;height:100%;background:${cls || color}"></div></div>`;
}
async function loadUsage() {
  let d;
  try { d = await api('/api/panel/usage-summary'); } catch (e) { toast(e.message, true); return; }
  const tks = d.tokens || {};
  $('#usageCards').innerHTML = `
    <div class="card" style="flex:1;min-width:150px"><div style="opacity:.6;font-size:12px">${t('usage_today')}</div>
      <div style="font-size:22px;font-weight:600">${fmtTokens(tks.today)}</div>
      <div style="opacity:.5;font-size:11px">${tks.todayReqs || 0} ${t('usage_req_unit').trim()}${tks.cacheHit ? ' · ' + t('usage_cache_hit') + ' ' + Math.round(tks.cacheHit * 100) + '%' : ''}</div></div>
    <div class="card" style="flex:1;min-width:150px"><div style="opacity:.6;font-size:12px">${t('usage_week')}</div>
      <div style="font-size:22px;font-weight:600">${fmtTokens(tks.week)}</div></div>
    <div class="card" style="flex:1;min-width:150px"><div style="opacity:.6;font-size:12px">${t('usage_requests')}</div>
      <div style="font-size:22px;font-weight:600">${tks.requests || 0}</div>
      <div style="opacity:.5;font-size:11px">${t('usage_tracked')} ${tks.tracked || 0}</div></div>`;
  // 各账号额度与占比
  const accs = d.accounts || [];
  $('#usageQuotaList').innerHTML = accs.length ? accs.map(x => {
    const pct = x.total > 0 ? (x.used / x.total * 100).toFixed(1) + '%' : '—';
    const unit = x.unit || '';
    return `<div style="padding:10px 0;border-bottom:1px solid var(--border,#333)">
      <div style="display:flex;justify-content:space-between;align-items:center;gap:8px">
        <div><b>${esc(x.nickname)}</b> <span class="badge">${esc(x.provider)}</span>
          ${x.plan ? `<span class="badge acc">${esc(x.plan)}</span>` : ''}</div>
        <div style="font-variant-numeric:tabular-nums">${fmtNum(x.used)} / ${fmtNum(x.total)} ${esc(unit)}
          <span style="opacity:.55;margin-left:6px">${pct}</span></div>
      </div>${usageBar(x.used, x.total)}
      ${(x.parts && x.parts.length > 1) ? x.parts.map(pt => `
        <div style="margin-top:6px;font-size:12px;opacity:.8">
          ${esc(pt.name)}：${fmtNum(pt.used)} / ${fmtNum(pt.total)} ${esc(pt.unit || '')}${pt.expiresAt ? ` · ${t('usage_until')} ${new Date(pt.expiresAt * 1000).toLocaleString()}` : ''}
        </div>${usageBar(pt.used, pt.total)}`).join('') : ''}
    </div>`;
  }).join('') : `<div style="opacity:.55;padding:12px 0">${t('usage_no_quota')}</div>`;
  // 分布
  const rowsHtml = (rows, labelFn) => {
    const max = Math.max(1, ...(rows || []).map(r => r.tokens));
    if (!rows || !rows.length) return `<div style="opacity:.55;padding:8px 0">${t('usage_empty')}</div>`;
    return rows.map(r => {
      const label = labelFn ? labelFn(r.key) : r.key;
      return `<div style="padding:6px 0">
        <div style="display:flex;justify-content:space-between;font-size:13px">
          <span>${esc(label)}</span><span style="opacity:.7">${fmtTokens(r.tokens)} · ${r.requests}${t('usage_req_unit')}</span></div>
        ${usageBar(r.tokens, max, 'var(--acc,#4f8cff)')}</div>`;
    }).join('');
  };
  $('#usageByCaller').innerHTML = rowsHtml(d.byCaller, k => k === 'owner' ? t('usage_owner') : k);
  $('#usageByProvider').innerHTML = rowsHtml(d.byProvider);
  $('#usageByModel').innerHTML = rowsHtml((d.byModel || []).slice(0, 8));
}

/* ───────────────────────── state ───────────────────────── */
let overview = null;
let modelCache = null;
let cfgData = null;

async function loadOverview() {
  try {
    overview = await api('/api/panel/overview');
    $('#verLine').textContent = 'TokenHub v' + overview.version;
    $('#dashBase').value = `http://127.0.0.1:${overview.port}/v1`;
    apiBaseURL = `http://127.0.0.1:${overview.port}`;
    $('#apiBase').value = apiBaseURL;
    $('#dashKey').value = overview.apiKey;
    $('#apiKeyInput').value = overview.apiKey;
    const lanEl = $('#lanBase');
    lanBaseURL = `http://${overview.localIP || '127.0.0.1'}:${overview.port}`;
    if (lanEl) lanEl.value = lanBaseURL + '/v1';
    const lanHint = $('#lanHint');
    if (lanHint) {
      const open = overview.host === '0.0.0.0' || overview.host === '::';
      lanHint.textContent = t(open ? 'lan_hint_on' : 'lan_hint_off');
    }
    renderProviders();
    renderAccounts();
    syncAllFmt();
  } catch (e) { toast(e.message, true); }
}

/* ───────────────────────── dashboard ───────────────────────── */
function providerMeta(name) {
  const map = {
    workbuddy: { label: t('prov_workbuddy'), icon: '/icons/workbuddy.png', color: '#3ad98a' },
    trae: { label: t('prov_trae'), icon: '/icons/trae.png', color: '#4f8cff' },
    zcode: { label: t('prov_zcode'), icon: '/icons/zcode.png', color: '#f2b544' },
  };
  return map[name] || { label: name, icon: '/favicon.svg', color: '#8b93a5' };
}

function picoImg(name, cls) {
  const meta = providerMeta(name);
  return `<img class="pico ${cls || ''}" src="${meta.icon}" alt="${esc(meta.label)}">`;
}

function renderProviders() {
  if (!overview) return;
  const box = $('#provCards');
  box.innerHTML = '';
  for (const name of ['workbuddy', 'trae', 'zcode']) {
    const s = (overview.summary && overview.summary[name]) || { accounts: 0, active: 0, remain: 0, total: 0, unit: '' };
    const meta = providerMeta(name);
    const card = document.createElement('div');
    card.className = 'card stat';
    card.innerHTML = `
      <div class="label"><span>${picoImg(name)} ${esc(meta.label)}</span><span class="dot ${s.active > 0 ? 'on' : 'off'}"></span></div>
      <div class="num">${fmtNum(s.remain)}<small>${esc(s.unit || '')}</small></div>
      <div class="label" style="margin-top:6px"><span>${t('accounts')} ${s.accounts} · ${t('active')} ${s.active}</span></div>`;
    box.appendChild(card);
  }
}

async function withBusy(btn, fn, busyText) {
  if (btn.disabled) return;
  const old = btn.textContent;
  btn.disabled = true;
  btn.textContent = busyText;
  try {
    await fn();
  } finally {
    btn.disabled = false;
    btn.textContent = old;
  }
}
$('#btnRefreshQuotas').addEventListener('click', e => withBusy(e.currentTarget, async () => {
  await post('/api/panel/refresh-quotas');
  toast(t('quota_ok'));
  await loadOverview();
}, t('quota_busy')));
$('#btnReloadUsage').addEventListener('click', async () => {
  const btn = $('#btnReloadUsage');
  if (btn.dataset.busy) return;
  btn.dataset.busy = '1';
  const old = btn.textContent;
  btn.textContent = t('usage_reloading');
  try {
    // 先从上游拉一遍全部额度快照（与总览页「刷新全部额度」同源），再渲染用量页
    try { await post('/api/panel/refresh-quotas'); } catch {}
    await loadUsage();
    await loadUsageLog();
  } finally {
    btn.textContent = old;
    delete btn.dataset.busy;
  }
});
$('#btnCheckinAll').addEventListener('click', e => withBusy(e.currentTarget, async () => {
  await post('/api/panel/checkin-pass');
  toast(t('checkin_ok'));
  await loadOverview();
}, t('checkin_busy')));
$('#btnRefreshTokens').addEventListener('click', e => withBusy(e.currentTarget, async () => {
  const res = await post('/api/panel/refresh-tokens');
  const okN = res.ok || 0, failN = res.failed || 0;
  if (failN === 0) {
    toast(t('refresh_tokens_ok').replace('{x}', okN));
  } else {
    const fails = (res.results || []).filter(r => !r.ok).map(r => `${r.name}: ${r.error}`).join('\n');
    toast(t('refresh_tokens_part').replace('{x}', okN).replace('{y}', failN));
    showModal(t('dash_refresh_tokens'), `<div class="codebox" style="max-height:50vh;overflow:auto;white-space:pre-wrap">${esc(fails)}</div>
      <div class="foot"><button id="rtClose">${t('close')}</button></div>`);
    $('#rtClose').addEventListener('click', closeModal);
  }
  await loadOverview();
}, t('refresh_tokens_busy')));

/* ───────────────────────── accounts ───────────────────────── */
function acctStatus(a) {
  const now = Math.floor(Date.now() / 1000);
  if (!a.enabled) return { cls: '', label: t('st_off') };
  if (a.dead) return { cls: 'err', label: t('st_dead') };
  if (a.cooldownUntil > now) return { cls: 'warn', label: t('st_cooling') };
  if (a.expiresAt && a.expiresAt > 0 && a.expiresAt - now < 86400) return { cls: 'warn', label: t('st_expiring') };
  return { cls: 'ok', label: t('st_on') };
}

// renderZcodePending 自动领取撞到验证码的活动待办横幅（一键进入滑块领取）。
async function renderZcodePending(box) {
  let pend;
  try { pend = await api('/api/panel/zcode/pending'); } catch { return; }
  const list = pend.pending || [];
  if (!list.length) return;
  const el = document.createElement('div');
  el.className = 'card';
  el.style.cssText = 'border:1px solid var(--danger,#e5615c);margin-bottom:12px';
  el.innerHTML = `
    <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:6px">
      <b style="color:var(--danger,#e5615c)">🔔 ${t('zc_pending_title').replace('{n}', list.length)}</b>
      <button class="sm primary" id="zcAutoAll">${t('zc_auto_claim')}</button>
    </div>` + list.map((p, i) => `
    <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;padding:6px 0;border-top:1px solid var(--border,#333)">
      <div style="font-size:13px">${esc(p.planName || p.planId)} <span style="opacity:.55">· ${esc(p.accountName)}</span></div>
      <button class="sm primary" data-pend-claim="${i}">${t('zc_claim')}</button>
    </div>`).join('');
  box.appendChild(el);
  $('#zcAutoAll').addEventListener('click', async (ev) => {
    const b = ev.currentTarget;
    b.disabled = true;
    b.textContent = t('zc_auto_running');
    try { await window.zcAutoClaimPending(); } finally {
      b.disabled = false;
      b.textContent = t('zc_auto_claim');
    }
  });
  el.querySelectorAll('[data-pend-claim]').forEach((btn, i) => {
    btn.addEventListener('click', async () => {
      const p = list[i];
      btn.disabled = true;
      btn.textContent = t('zc_claiming');
      try {
        const cc = await api(`/api/panel/zcode/captcha-config?id=${encodeURIComponent(p.accountId)}`);
        await zcCaptchaModal({ id: p.accountId }, p.planId, cc.captcha || {}, !!(overview && overview.webviewWindow));
      } catch (e) { toast(t('zc_captcha_load_fail'), true); }
      btn.disabled = false;
      btn.textContent = t('zc_claim');
      await loadOverview();
    });
  });
}

function renderAccounts() {
  if (!overview) return;
  const box = $('#acctSections');
  box.innerHTML = '';
  renderZcodePending(box);
  for (const name of ['workbuddy', 'trae', 'zcode']) {
    const meta = providerMeta(name);
    const list = (overview.accounts || []).filter(a => a.provider === name);
    const sec = document.createElement('div');
    sec.className = 'sec';
    sec.innerHTML = `
      <div class="sec-head">
        <h2>${picoImg(name)} ${esc(meta.label)} <span class="badge">${list.length}</span></h2>
        <div class="row">
          <button class="sm" data-add="${name}">＋ ${t('acct_add')}</button>
        </div>
      </div>
      <div data-list="${name}"></div>`;
    const listBox = sec.querySelector(`[data-list="${name}"]`);
    if (!list.length) {
      listBox.innerHTML = `<div class="empty">${t('acct_none')}</div>`;
    } else {
      for (const a of list) listBox.appendChild(acctCard(a));
    }
    box.appendChild(sec);
  }
  $$('#acctSections [data-add]').forEach(btn => {
    btn.addEventListener('click', () => openAddModal(btn.dataset.add));
  });
}

function acctCard(a) {
  const el = document.createElement('div');
  el.className = 'acct';
  const st = acctStatus(a);
  const q = a.lastQuota || a.quota;
  let quotaHtml = '';
  if (q && (q.total > 0 || q.remain > 0)) {
    const pct = q.total > 0 ? Math.max(2, Math.min(100, Math.round(q.remain / q.total * 100))) : 100;
    quotaHtml = `
      <div class="quota">
        <div class="txt"><span>${t('remain')}</span><span>${fmtNum(q.remain)} / ${fmtNum(q.total)} ${esc(q.unit || '')}</span></div>
        <div class="bar"><i style="width:${pct}%"></i></div>
      </div>`;
  } else {
    quotaHtml = `<div class="quota"><div class="txt"><span>${t('remain')}</span><span>${t('no_quota')}</span></div></div>`;
  }
  const initial = (a.nickname || a.uid || '?').trim().charAt(0).toUpperCase();
  el.innerHTML = `
    <div class="avatar">${esc(initial)}</div>
    <div class="info">
      <div class="name">${esc(a.nickname || a.uid || a.id.slice(0, 8))}
        ${a.realm ? `<span class="badge acc">${esc(a.realm)}</span>` : ''}
        <span class="badge ${st.cls}">${st.label}</span>
        ${a.hasRefresh === false ? `<span class="badge" title="${esc(t('local_sync_tip'))}">${t('local_sync_badge')}</span>` : ''}
        ${q && q.plan ? `<span class="badge">${esc(q.plan)}</span>` : ''}
      </div>
      <div class="meta">
        ${a.uid ? `<span>UID ${esc(String(a.uid))}</span>` : ''}
        <span>${t('token_expiry')}: ${a.expiresAt ? fmtTime(a.expiresAt) : t('never')}</span>
        <span>${t('last_checkin')}: ${a.lastCheckinMsg ? esc(a.lastCheckinMsg) + ' · ' + fmtTime(a.lastCheckinAt) : t('never')}</span>
      </div>
      ${quotaHtml}
    </div>
    <div class="acts">
      <button class="sm" data-act="quota">🔄 ${t('act_quota')}</button>
      <button class="sm" data-act="checkin">🎁 ${t('act_checkin')}</button>
      <button class="sm" data-act="refresh">🔑 ${t('act_refresh')}</button>
      ${a.hasRefresh ? `<button class="sm" data-act="copyrt">📋 ${t('act_copy_rt')}</button>` : ''}
      ${a.provider === 'trae' ? `<button class="sm" data-act="switchide" title="${esc(t('act_switch_ide_tip'))}">⚡ ${t('act_switch_ide')}</button>
      <button class="sm" data-act="captureide" title="${esc(t('act_capture_tip'))}">📥 ${t('act_capture')}</button>
      <button class="sm" data-act="migratesess" title="${esc(t('act_migrate_tip'))}">🧬 ${t('act_migrate')}</button>` : ''}
      ${a.provider === 'zcode' ? `<button class="sm" data-act="plans">🎪 ${t('zc_plans')}</button>
      <button class="sm" data-act="raw">🧾 ${t('act_raw')}</button>` : ''}
      <button class="sm" data-act="toggle">${a.enabled ? '⏸' : '▶'} ${a.enabled ? t('act_disable') : t('act_enable')}</button>
      <button class="sm danger" data-act="delete">🗑</button>
    </div>`;
  el.querySelectorAll('[data-act]').forEach(btn => {
    btn.addEventListener('click', () => acctAction(btn.dataset.act, a, btn));
  });
  return el;
}

async function acctAction(act, a, btn) {
  const old = btn.textContent;
  try {
    if (act === 'quota') {
      btn.disabled = true;
      await post(`/api/panel/account/${a.id}/quota`);
      toast(t('quota_ok'));
    } else if (act === 'checkin') {
      btn.disabled = true;
      const res = await post(`/api/panel/account/${a.id}/checkin`);
      toast(res.msg || t('checkin_ok'));
    } else if (act === 'refresh') {
      btn.disabled = true;
      await post(`/api/panel/account/${a.id}/refresh`);
      toast('OK');
    } else if (act === 'copyrt') {
      const res = await api(`/api/panel/account/${a.id}/refresh-token`);
      if (!res.refreshToken) { toast(t('no_rt'), true); return; }
      await copyText(res.refreshToken);
      toast(t('copied'));
      return;
    } else if (act === 'switchide') {
      if (!confirm(t('confirm_switch_ide'))) return;
      btn.disabled = true;
      await post(`/api/panel/account/${a.id}/switch-trae`);
      toast('OK');
    } else if (act === 'captureide') {
      btn.disabled = true;
      await post(`/api/panel/account/${a.id}/capture-trae`);
      toast('OK');
    } else if (act === 'migratesess') {
      await migrateSessionsModal(a);
      return;
    } else if (act === 'plans') {
      btn.disabled = true;
      await zcPlansModal(a);
    } else if (act === 'raw') {
      const data = await api(`/api/panel/account/${a.id}/quota-raw`);
      showModal(t('raw_title'), `<div class="codebox" style="max-height:60vh;overflow:auto">${esc(JSON.stringify(data, null, 2))}</div>
        <div class="foot"><button id="rawClose">${t('close')}</button></div>`);
      $('#rawClose').addEventListener('click', closeModal);
      return;
    } else if (act === 'toggle') {
      await patch(`/api/panel/account/${a.id}`, { enabled: !a.enabled });
    } else if (act === 'delete') {
      if (!confirm(t('confirm_delete'))) return;
      await del(`/api/panel/account/${a.id}`);
    }
    await loadOverview();
  } catch (e) {
    toast(e.message, true);
  } finally {
    btn.disabled = false;
    btn.textContent = old;
  }
}

/* ───────────────────────── add-account modal ───────────────────────── */
function showModal(title, bodyHtml) {
  $('#addTitle').textContent = title;
  $('#addBody').innerHTML = bodyHtml;
  $('#addModal').classList.add('show');
}
function closeModal() { $('#addModal').classList.remove('show'); }
$('#addClose').addEventListener('click', closeModal);
$('#addModal').addEventListener('click', e => { if (e.target === $('#addModal')) closeModal(); });

function openAddModal(provider) {
  if (provider === 'workbuddy') return addModalWB();
  if (provider === 'trae') return addModalTrae();
  if (provider === 'zcode') return addModalZcode();
}

function steps(items) {
  return `<div class="pill-steps">${items.map(s => `<div class="step">${s}</div>`).join('')}</div>`;
}

/* WorkBuddy */
function addModalWB() {
  showModal(t('login_wb_title'), `
    <div class="field"><label>${t('login_wb_realm')}</label>
      <select id="wbRealm">
        <option value="cn">${t('realm_cn')}</option>
        <option value="global">${t('realm_global')}</option>
      </select>
    </div>
    ${steps([t('login_wb_s1'), t('login_wb_s2')])}
    <button class="primary" id="wbGo">${t('open_auth')}</button>
    <div class="hint" id="wbStatus"></div>
    <button class="sm" id="wbLocal" style="margin-top:10px">📥 ${t('import_wb_btn')}</button>
    <button class="ghost sm" id="wbManual" style="margin-left:8px">${t('acct_manual')}</button>
  `);
  $('#wbLocal').addEventListener('click', async () => {
    try {
      const r = await post('/api/panel/import-local/workbuddy');
      $('#wbStatus').textContent = `${t('import_local_ok')} +${r.added}（发现 ${r.found}）`;
      toast(t('import_local_ok'));
      loadOverview();
    } catch (e) { $('#wbStatus').textContent = e.message; }
  });
  $('#wbGo').addEventListener('click', async () => {
    const realm = $('#wbRealm').value;
    try {
      const res = await post('/api/panel/login/workbuddy', { realm });
      window.open(res.authUrl, '_blank');
      $('#wbStatus').textContent = t('waiting');
      const poll = setInterval(async () => {
        try {
          const r = await api(`/api/panel/login/workbuddy/poll?state=${encodeURIComponent(res.state)}`);
          if (r.status === 'ok') {
            clearInterval(poll);
            $('#wbStatus').textContent = `${t('login_ok')}: ${r.nickname}`;
            toast(t('login_ok'));
            loadOverview();
            setTimeout(closeModal, 1200);
          }
        } catch (e) { clearInterval(poll); $('#wbStatus').textContent = t('login_fail') + ': ' + e.message; }
      }, 3000);
    } catch (e) { $('#wbStatus').textContent = t('login_fail') + ': ' + e.message; }
  });
  $('#wbManual').addEventListener('click', () => manualModal('workbuddy'));
}

/* Trae */
function addModalTrae() {
  showModal(t('login_trae_title'), `
    ${steps([t('login_trae_s1'), t('login_trae_s2'), t('login_trae_s3')])}
    <button class="primary" id="trGo">${t('open_auth')}</button>
    <div class="hint" id="trStatus"></div>
    <div class="field" style="margin-top:12px"><label>${t('paste_callback')}</label>
      <input type="text" id="trCallback" placeholder="http://127.0.0.1:18080/authorize?refreshToken=...">
    </div>
    <button class="sm" id="trSubmit">${t('submit_callback')}</button>
    <button class="sm" id="trLocal" style="margin-left:8px">📥 ${t('import_trae_btn')}</button>
    <button class="ghost sm" id="trManual" style="margin-left:8px">${t('acct_manual')}</button>
  `);
  $('#trLocal').addEventListener('click', async () => {
    try {
      const r = await post('/api/panel/import-local/trae');
      $('#trStatus').textContent = `${t('import_local_ok')} +${r.added}（发现 ${r.found}）`;
      toast(t('import_local_ok'));
      loadOverview();
    } catch (e) { $('#trStatus').textContent = e.message; }
  });
  $('#trGo').addEventListener('click', async () => {
    try {
      const res = await post('/api/panel/login/trae');
      window.open(res.loginUrl, '_blank');
      $('#trStatus').textContent = t('waiting');
      const poll = setInterval(async () => {
        try {
          const r = await api(`/api/panel/login/trae/poll?pendingId=${encodeURIComponent(res.pendingId)}`);
          if (r.status === 'success') {
            clearInterval(poll);
            $('#trStatus').textContent = `${t('login_ok')}: ${r.nickname}`;
            toast(t('login_ok'));
            loadOverview();
            setTimeout(closeModal, 1200);
          } else if (r.status === 'failed') {
            clearInterval(poll);
            $('#trStatus').textContent = t('login_fail') + ': ' + (r.error || '');
          }
        } catch (e) { clearInterval(poll); $('#trStatus').textContent = t('login_fail') + ': ' + e.message; }
      }, 2500);
    } catch (e) { $('#trStatus').textContent = t('login_fail') + ': ' + e.message; }
  });
  $('#trSubmit').addEventListener('click', async () => {
    const url = $('#trCallback').value.trim();
    if (!url) return;
    try {
      const r = await post('/api/panel/login/trae/callback', { url });
      $('#trStatus').textContent = `${t('login_ok')}: ${r.nickname}`;
      toast(t('login_ok'));
      loadOverview();
      setTimeout(closeModal, 1200);
    } catch (e) { $('#trStatus').textContent = t('login_fail') + ': ' + e.message; }
  });
  $('#trManual').addEventListener('click', () => manualModal('trae'));
}

/* ZCode */
function addModalZcode() {
  showModal(t('login_zc_title'), `
    <div class="field"><label>${t('login_zc_provider')}</label>
      <select id="zcProvider">
        <option value="bigmodel">${t('zc_bigmodel')}</option>
        <option value="zai">${t('zc_zai')}</option>
      </select>
    </div>
    ${steps([t('login_zc_s1'), t('login_zc_s2')])}
    <button class="primary" id="zcGo">${t('open_auth')}</button>
    <div class="hint" id="zcStatus"></div>
    <hr style="border-color:var(--border);margin:14px 0">
    <div class="hint">${t('import_local_desc')}</div>
    <button class="sm" id="zcLocal">📥 ${t('acct_import_local')}</button>
    <button class="ghost sm" id="zcManual" style="margin-left:8px">${t('acct_manual')}</button>
  `);
  $('#zcGo').addEventListener('click', async () => {
    const providerKind = $('#zcProvider').value;
    try {
      const res = await post('/api/panel/login/zcode', { provider: providerKind });
      window.open(res.authorizeUrl, '_blank');
      $('#zcStatus').textContent = t('waiting');
      const poll = setInterval(async () => {
        try {
          const r = await api(`/api/panel/login/zcode/poll?flowId=${encodeURIComponent(res.flowId)}`);
          if (r.status === 'ok') {
            clearInterval(poll);
            $('#zcStatus').textContent = `${t('login_ok')}: ${r.nickname}`;
            toast(t('login_ok'));
            loadOverview();
            setTimeout(closeModal, 1200);
          }
        } catch (e) { clearInterval(poll); $('#zcStatus').textContent = t('login_fail') + ': ' + e.message; }
      }, 3000);
    } catch (e) { $('#zcStatus').textContent = t('login_fail') + ': ' + e.message; }
  });
  $('#zcLocal').addEventListener('click', async () => {
    try {
      const r = await post('/api/panel/zcode/import-local');
      $('#zcStatus').textContent = `${t('import_local_ok')}: ${r.nickname}`;
      toast(t('import_local_ok'));
      loadOverview();
    } catch (e) { $('#zcStatus').textContent = e.message; }
  });
  $('#zcManual').addEventListener('click', () => manualModal('zcode'));
}

/* Manual import */
function manualModal(provider) {
  showModal(t('manual_title'), `
    <div class="field"><label>${t('manual_provider')}</label>
      <select id="mProvider">
        ${['workbuddy', 'trae', 'zcode'].map(pv => `<option value="${pv}" ${pv === provider ? 'selected' : ''}>${providerMeta(pv).label}</option>`).join('')}
      </select>
    </div>
    <div class="field"><label>${t('manual_realm')}</label><input type="text" id="mRealm" placeholder="cn / global / bigmodel / zai"></div>
    <div class="field"><label>${t('manual_access')}</label><input type="text" id="mAccess"></div>
    <div class="field"><label>${t('manual_refresh')}</label><input type="text" id="mRefresh"></div>
    <div class="field"><label>${t('manual_uid')}</label><input type="text" id="mUid"></div>
    <div class="field"><label>${t('manual_nick')}</label><input type="text" id="mNick"></div>
    <button class="primary" id="mSave">${t('save')}</button>
  `);
  $('#mSave').addEventListener('click', async () => {
    try {
      await post('/api/panel/account', {
        provider: $('#mProvider').value,
        realm: $('#mRealm').value.trim(),
        accessToken: $('#mAccess').value.trim(),
        refreshToken: $('#mRefresh').value.trim(),
        uid: $('#mUid').value.trim(),
        nickname: $('#mNick').value.trim(),
      });
      toast(t('saved_ok'));
      closeModal();
      loadOverview();
    } catch (e) { toast(e.message, true); }
  });
}

/* ───────────────────────── API tab ───────────────────────── */
function renderExamples() {
  const base = $('#apiBase').value || 'http://127.0.0.1:8687';
  const key = $('#apiKeyInput').value || 'sk-...';
  const model = (modelCache && modelCache.workbuddy && modelCache.workbuddy[0] && modelCache.workbuddy[0].id) || 'glm-5.2';
  const tpl = I18N[LANG][activeEx] || I18N.zh[activeEx];
  $('#exBox').textContent = tpl.replace(/{{BASE}}/g, base).replace(/{{KEY}}/g, key).replace(/{{MODEL}}/g, model);
}
let activeEx = 'ex_curl';
$$('#exTabs button').forEach(b => {
  b.addEventListener('click', () => {
    $$('#exTabs button').forEach(x => x.classList.remove('on'));
    b.classList.add('on');
    activeEx = b.dataset.ex;
    renderExamples();
  });
});

async function renderModels(cached) {
  if (!cached) {
    try {
      const r = await api('/api/panel/models');
      modelCache = r.models;
    } catch { return; }
  }
  const chips = $('#modelChips');
  chips.innerHTML = '';
  const all = modelCache || {};
  for (const name of ['workbuddy', 'trae', 'zcode']) {
    const meta = providerMeta(name);
    for (const m of (all[name] || [])) {
      const chip = document.createElement('span');
      chip.className = 'chip';
      chip.innerHTML = `<b>${esc(m.id)}</b>`;
      chip.title = meta.label + (m.name ? ' · ' + m.name : '');
      chips.appendChild(chip);
    }
  }
  if (!chips.children.length) chips.innerHTML = `<span class="empty">${t('no_quota')}</span>`;
  renderExamples();
}
$('#btnReloadModels').addEventListener('click', () => { modelCache = null; renderModels(); });

$$('[data-copy]').forEach(b => b.addEventListener('click', () => copyText($('#' + b.dataset.copy).value)));
$('#dashKeyEye').addEventListener('click', () => { $('#dashKey').type = $('#dashKey').type === 'password' ? 'text' : 'password'; });
$('#apiKeyEye').addEventListener('click', () => { $('#apiKeyInput').type = $('#apiKeyInput').type === 'password' ? 'text' : 'password'; });

/* ───────────────────────── logs ───────────────────────── */
let logES = null;
const logBuf = [];
function appendLog(e) {
  logBuf.push(e);
  if (logBuf.length > 600) logBuf.shift();
  const box = $('#logsBox');
  const line = document.createElement('div');
  line.className = 'l';
  const d = new Date(e.time * 1000);
  const p = x => String(x).padStart(2, '0');
  line.innerHTML = `<span class="t">${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}</span><span class="tag">${esc(e.tag)}</span><span class="m">${esc(e.msg)}</span>`;
  line.querySelector('.tag').classList.add('lv-' + (e.level || 'info'));
  box.appendChild(line);
  while (box.children.length > 600) box.removeChild(box.firstChild);
  if (box._auto !== false) box.scrollTop = box.scrollHeight;
}
function startLogStream() {
  if (logES) return;
  fetch('/api/panel/logs?n=200').then(r => r.json()).then(r => {
    (r.entries || []).forEach(appendLog);
  }).catch(() => {});
  logES = new EventSource('/api/panel/logs/stream');
  logES.onmessage = ev => {
    try { appendLog(JSON.parse(ev.data)); } catch {}
  };
}
$('#btnClearLogsView').addEventListener('click', () => { logBuf.length = 0; $('#logsBox').innerHTML = ''; });

/* ───────────────────────── settings ───────────────────────── */
function settingRow(label, desc, ctl) {
  return `<div class="setting-row"><div><div>${label}</div><div class="d">${desc || ''}</div></div><div class="ctl">${ctl}</div></div>`;
}
function renderSettings() {
  if (!cfgData) return;
  const c = cfgData;
  $('#settingsCard').innerHTML = [
    settingRow(t('set_listen_host'), t('set_listen_host_d'), `<input type="text" id="sHost" value="${esc(c.host)}">`),
    settingRow(t('set_listen_port'), '', `<input type="number" id="sPort" value="${c.port}">`),
    settingRow(t('set_api_key'), '', `<div class="keyline"><input type="text" id="sKey" value="${esc(c.apiKey)}"><button class="sm" id="sRegen">${t('set_regen_key')}</button></div>`),
    settingRow(t('set_default_provider'), '', `<select id="sDefProv">${['workbuddy', 'trae', 'zcode'].map(pv => `<option value="${pv}" ${c.defaultProvider === pv ? 'selected' : ''}>${providerMeta(pv).label}</option>`).join('')}</select>`),
    settingRow(t('set_auto_checkin'), t('set_auto_checkin_d'), `<label class="switch"><input type="checkbox" id="sAutoCheckin" ${c.autoCheckin ? 'checked' : ''}><i></i></label>`),
    settingRow(t('set_wb_keepalive'), t('set_wb_keepalive_d'), `<label class="switch"><input type="checkbox" id="sWbKeep" ${c.wbIntlKeepalive ? 'checked' : ''}><i></i></label>`),
    settingRow(t('set_zcode_claim'), t('set_zcode_claim_d'), `<label class="switch"><input type="checkbox" id="sZcClaim" ${c.zcodeAutoClaim ? 'checked' : ''}><i></i></label>`),
    settingRow(t('set_zcode_interval'), '', `<input type="number" id="sZcInterval" value="${c.zcodeClaimIntervalMin}">`),
    settingRow(t('set_zcode_shape'), t('set_zcode_shape_d'), `<select id="sZcShape">${['overwrite', 'prepend', 'off'].map(v => `<option value="${v}" ${c.zcodeInjectShape === v ? 'selected' : ''}>${v}</option>`).join('')}</select>`),
    settingRow(t('set_quota_refresh'), '', `<input type="number" id="sQuotaSec" step="0.1" min="0" value="${c.quotaRefreshSec ?? 300}">`),
    settingRow(t('set_trae_switch'), t('set_trae_switch_d'), `<label class="switch"><input type="checkbox" id="sTraeSwitch" ${c.traeAutoSwitch ? 'checked' : ''}><i></i></label>`),
    settingRow(t('set_refresh_skew'), '', `<input type="number" id="sSkew" value="${c.refreshSkewMin}">`),
    settingRow(t('set_expose_prefix'), '', `<label class="switch"><input type="checkbox" id="sExpose" ${c.exposePrefixModels ? 'checked' : ''}><i></i></label>`),
  ].join('');
  $('#sRegen').addEventListener('click', () => {
    const bytes = new Uint8Array(12);
    crypto.getRandomValues(bytes);
    $('#sKey').value = 'sk-th-' + Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
  });
}

$('#btnSaveSettings').addEventListener('click', async () => {
  const body = {
    host: $('#sHost').value.trim(),
    port: parseInt($('#sPort').value, 10) || 8687,
    apiKey: $('#sKey').value.trim(),
    defaultProvider: $('#sDefProv').value,
    autoCheckin: $('#sAutoCheckin').checked,
    wbIntlKeepalive: $('#sWbKeep').checked,
    zcodeAutoClaim: $('#sZcClaim').checked,
    zcodeClaimIntervalMin: parseInt($('#sZcInterval').value, 10) || 30,
    zcodeInjectShape: $('#sZcShape').value,
    quotaRefreshSec: Math.max(0, parseFloat($('#sQuotaSec').value) || 0),
    traeAutoSwitch: $('#sTraeSwitch').checked,
    refreshSkewMin: parseInt($('#sSkew').value, 10) || 1440,
    exposePrefixModels: $('#sExpose').checked,
  };
  try {
    await post('/api/panel/config', body);
    cfgData = Object.assign(cfgData, body);
    toast(t('saved_ok'));
  } catch (e) { toast(e.message, true); }
});

/* ───────────────────── 公网分享 & 分享钥匙 ───────────────────── */
let tunnelState = { state: 'off', url: '', error: '' };
let shareCache = [];
let lanBaseURL = '';
let apiBaseURL = '';

/* 地址格式下拉：OpenAI / Anthropic / 模型列表 */
const FMT_KEYS = ['fmt_oai_base', 'fmt_oai_v1', 'fmt_oai_full', 'fmt_ant_full', 'fmt_models'];
function fmtURL(kind, base) {
  const b = (base || '').replace(/\/+$/, '');
  if (kind === 'fmt_oai_v1') return b + '/v1';
  if (kind === 'fmt_oai_full') return b + '/v1/chat/completions';
  if (kind === 'fmt_ant_full') return b + '/v1/messages';
  if (kind === 'fmt_models') return b + '/v1/models';
  return b; // fmt_oai_base
}
function buildFmtOptions() {
  [['#pubFmtSel', '#pubFmtURL'], ['#lanFmtSel', '#lanFmtURL'], ['#apiFmtSel', '#apiFmtURL']].forEach(([sid, uid]) => {
    const sel = $(sid); if (!sel) return;
    const cur = sel.value || FMT_KEYS[0];
    sel.innerHTML = FMT_KEYS.map(k => `<option value="${k}">${t(k)}</option>`).join('');
    sel.value = cur;
    if (!sel.dataset.bound) {
      sel.dataset.bound = '1';
      sel.addEventListener('change', () => syncFmt(sid, uid));
    }
  });
  syncAllFmt();
}
function syncFmt(sid, uid) {
  const sel = $(sid), url = $(uid); if (!sel || !url) return;
  const base = sid === '#pubFmtSel' ? (tunnelState.url || '') : sid === '#apiFmtSel' ? apiBaseURL : lanBaseURL;
  url.value = fmtURL(sel.value, base);
  const row = url.closest('.fmt-row');
  if (row) row.style.display = base ? '' : 'none';
}
function syncAllFmt() { syncFmt('#pubFmtSel', '#pubFmtURL'); syncFmt('#lanFmtSel', '#lanFmtURL'); syncFmt('#apiFmtSel', '#apiFmtURL'); }

async function loadTunnel() {
  try { tunnelState = await api('/api/panel/tunnel/status'); } catch {}
  updatePubUI();
}
function updatePubUI() {
  const urlEl = $('#pubURL'); if (!urlEl) return;
  urlEl.value = tunnelState.url || '';
  const btn = $('#btnPubToggle'), st = $('#pubStatus');
  btn.textContent = (tunnelState.state === 'up' || tunnelState.state === 'starting') ? t('pub_btn_off') : t('pub_btn_on');
  btn.disabled = tunnelState.state === 'starting';
  if (tunnelState.state === 'up') st.textContent = t('pub_st_up');
  else if (tunnelState.state === 'starting') st.textContent = t('pub_st_starting');
  else if (tunnelState.state === 'error') st.textContent = t('pub_st_error') + (tunnelState.error || '');
  else st.textContent = t('pub_st_off');
  syncFmt('#pubFmtSel', '#pubFmtURL');
}
$('#btnPubToggle').addEventListener('click', async () => {
  const on = tunnelState.state === 'up' || tunnelState.state === 'starting';
  try {
    tunnelState = await post(on ? '/api/panel/tunnel/stop' : '/api/panel/tunnel/start');
    updatePubUI();
  } catch (e) { toast(e.message, true); }
});
setInterval(loadTunnel, 5000);

/* 用量轻量刷新：只更新 .used 文本，不重渲染列表（避免打断输入） */
async function pollUsage() {
  try {
    const d = await api('/api/panel/shares'); const list = d.shares || [];
    document.querySelectorAll('.share-item').forEach(item => {
      const s = list.find(x => x.id === item.dataset.id); if (!s) return;
      const P = s.prov || s.providers || {};
      let totR = 0, totT = 0;
      SHARE_PROVS.forEach(pv => {
        const ps = P[pv] || {};
        totR += ps.usedReqs || 0; totT += ps.usedTokens || 0;
        const u = item.querySelector(`[data-usage="${pv}"]`);
        if (u) u.textContent = `${t('share_used')} ${ps.usedReqs || 0}${t('share_times')} / ${fmtNum(ps.usedTokens || 0)} tok`;
      });
      const tu = item.querySelector('[data-usage-total]');
      if (tu) tu.textContent = `${t('share_used')} ${totR}${t('share_times')} / ${fmtNum(totT)} tok`;
    });
  } catch {}
}
setInterval(pollUsage, 10000);

/* ── 用量日志 ── */
let usageLogPage = 1, usageLogPages = 1;
async function loadUsageLog() {
  try {
    const d = await api(`/api/panel/usage?page=${usageLogPage}&page_size=15`);
    const total = d.total || 0, size = d.pageSize || 15;
    usageLogPages = Math.max(1, Math.ceil(total / size));
    if (usageLogPage > usageLogPages) { usageLogPage = usageLogPages; return loadUsageLog(); }
    renderUsageLog(d.entries || []);
    const pager = $('#usagePager');
    if (pager) {
      pager.style.display = total > size ? 'flex' : 'none';
      pager.innerHTML = `
        <button class="sm" id="ulPrev" ${usageLogPage <= 1 ? 'disabled' : ''}>‹</button>
        <span style="opacity:.65;font-size:12px;padding:0 6px">${usageLogPage} / ${usageLogPages} · ${t('usage_total')} ${total}</span>
        <button class="sm" id="ulNext" ${usageLogPage >= usageLogPages ? 'disabled' : ''}>›</button>`;
      $('#ulPrev')?.addEventListener('click', () => { if (usageLogPage > 1) { usageLogPage--; loadUsageLog(); } });
      $('#ulNext')?.addEventListener('click', () => { if (usageLogPage < usageLogPages) { usageLogPage++; loadUsageLog(); } });
    }
  } catch {}
}
function renderUsageLog(list) {
  const tb = $('#usageList'); if (!tb) return;
  const head = $('#usageHead');
  if (head) head.innerHTML = `<tr>
    <th>${t('usage_time')}</th><th>${t('usage_caller')}</th><th>${t('usage_prov')}</th>
    <th>${t('usage_model')}</th><th style="text-align:right">${t('usage_tokens')}</th>
    <th style="text-align:right">${t('usage_dur')}</th><th>${t('usage_status')}</th></tr>`;
  tb.innerHTML = list.length ? list.map(u => {
    const time = new Date(u.time * 1000).toLocaleTimeString('zh-CN', { hour12: false });
    const caller = u.caller ? esc(u.caller) : t('usage_owner');
    const cr = u.cacheReadTokens || 0, cw = u.cacheWriteTokens || 0;
    const hit = (u.promptTokens + cr + cw) > 0 ? Math.round(cr / (u.promptTokens + cr + cw) * 100) : 0;
    const detail = `${t('usage_in')} ${fmtNum(u.promptTokens || 0)} · ${t('usage_cache_read')} ${fmtNum(cr)} · ${t('usage_cache_write')} ${fmtNum(cw)} · ${t('usage_out')} ${fmtNum(u.completionTokens || 0)}${(u.promptTokens + cr + cw) > 0 ? ' · ' + t('usage_cache_hit') + ' ' + hit + '%' : ''}`;
    const tok = u.totalTokens
      ? `<span title="${esc(detail)}" style="cursor:help">${fmtNum(u.totalTokens)} <span style="color:var(--muted)">(${fmtNum(u.promptTokens)}+${fmtNum(u.completionTokens)}${(cr || cw) ? ` +${t('usage_cache_short')} ${fmtNum(cr + cw)}` : ''})</span>${(cr || cw) ? ` <span style="color:var(--ok,#3ad98a)" title="${esc(detail)}">${hit}%</span>` : ''}</span>`
      : '-';
    const st = u.ok
      ? `<span style="color:var(--ok, #3ad98a)">${t('usage_ok')}</span>`
      : `<span style="color:var(--danger, #ff6b6b)" title="${esc(u.errMsg || '')}">${t('usage_fail')}</span>`;
    return `<tr>
      <td style="font-family:var(--mono)">${time}</td>
      <td>${caller}</td>
      <td>${esc(u.provider || '')}</td>
      <td style="font-family:var(--mono)">${esc(u.model || '')}</td>
      <td style="text-align:right;font-family:var(--mono)">${tok}</td>
      <td style="text-align:right;font-family:var(--mono)">${u.durationMs != null ? u.durationMs + 'ms' : '-'}</td>
      <td>${st}</td>
    </tr>`;
  }).join('') : `<tr><td colspan="7" style="color:var(--muted)">${t('usage_empty')}</td></tr>`;
}
setInterval(() => { if (usageLogPage === 1) loadUsageLog(); }, 10000);

const SHARE_PROVS = ['workbuddy', 'trae', 'zcode'];
function shareLimitLabel(pv) {
  return pv === 'workbuddy' ? t('share_limit_wb') : pv === 'trae' ? t('share_limit_trae') : t('share_limit_zc');
}

async function loadShares() {
  try { const d = await api('/api/panel/shares'); shareCache = d.shares || []; } catch { return; }
  renderShares();
}

function shareItemHTML(s) {
  const provs = SHARE_PROVS.map(pv => {
    const ps = (s.prov || s.providers || {})[pv] || { enabled: false, limit: 0, models: [], usedTokens: 0, usedReqs: 0 };
    return `<div class="prow">
      <span class="pvn">${esc(providerMeta(pv).label)}</span>
      <label class="switch"><input type="checkbox" data-prov="${pv}" data-field="enabled" ${ps.enabled ? 'checked' : ''}><i></i></label>
      <span class="lbl">${shareLimitLabel(pv)}</span>
      <input type="number" min="0" data-prov="${pv}" data-field="limit" value="${ps.limit || 0}">
      <input type="text" data-prov="${pv}" data-field="models" value="${esc((ps.models || []).join(','))}" placeholder="${t('share_models_ph')}">
      <span class="used" data-usage="${pv}">${t('share_used')} ${ps.usedReqs || 0}${t('share_times')} / ${fmtNum(ps.usedTokens || 0)} tok</span>
    </div>`;
  }).join('');
  const P = s.prov || s.providers || {};
  const totReqs = SHARE_PROVS.reduce((a, pv) => a + ((P[pv] || {}).usedReqs || 0), 0);
  const totTok = SHARE_PROVS.reduce((a, pv) => a + ((P[pv] || {}).usedTokens || 0), 0);
  return `<div class="share-item" data-id="${s.id}">
    <div class="head">
      <span class="nm">${esc(s.name || s.id)}</span>
      <input type="text" class="mono share-key" readonly value="${esc(s.key)}" style="width:270px">
      <button class="sm" data-act="copy">📋</button>
      <span class="muted" data-usage-total>${t('share_used')} ${totReqs}${t('share_times')} / ${fmtNum(totTok)} tok</span>
      <label class="switch" title="${t('st_on')}"><input type="checkbox" data-act="toggle" ${s.enabled ? 'checked' : ''}><i></i></label>
      <button class="sm danger" data-act="del">${t('act_delete')}</button>
    </div>
    <div class="share-provs">${provs}</div>
  </div>`;
}

function renderShares() {
  const box = $('#shareList'); if (!box) return;
  box.innerHTML = shareCache.length ? shareCache.map(shareItemHTML).join('') : `<div class="empty">${t('share_none')}</div>`;
  box.querySelectorAll('.share-item').forEach(item => {
    const id = item.dataset.id;
    const s = shareCache.find(x => x.id === id);
    if (!s) return;
    item.querySelector('[data-act="copy"]').addEventListener('click', () => copyText(s.key));
    item.querySelector('[data-act="toggle"]').addEventListener('change', async e => {
      try { await patch(`/api/panel/shares/${id}`, { enabled: e.target.checked }); toast(t('saved_ok')); }
      catch (err) { toast(err.message, true); e.target.checked = !e.target.checked; }
    });
    item.querySelector('[data-act="del"]').addEventListener('click', async () => {
      if (!confirm(t('share_del_confirm'))) return;
      try { await del(`/api/panel/shares/${id}`); await loadShares(); toast(t('saved_ok')); }
      catch (err) { toast(err.message, true); }
    });
    item.querySelectorAll('input[data-prov]').forEach(inp => {
      inp.addEventListener('change', async () => {
        const providers = {};
        SHARE_PROVS.forEach(pv => {
          const en = item.querySelector(`input[data-prov="${pv}"][data-field="enabled"]`).checked;
          const li = parseInt(item.querySelector(`input[data-prov="${pv}"][data-field="limit"]`).value, 10) || 0;
          const models = item.querySelector(`input[data-prov="${pv}"][data-field="models"]`).value
            .split(',').map(x => x.trim()).filter(Boolean);
          providers[pv] = { enabled: en, limit: li, models };
        });
        try { await patch(`/api/panel/shares/${id}`, { providers }); toast(t('saved_ok')); }
        catch (err) { toast(err.message, true); }
      });
    });
  });
  renderShareCreate();
}

function renderShareCreate() {
  const box = $('#shareCreate'); if (!box) return;
  if (box.dataset.built === '1') return; // 只构建一次（i18n 文案切换时重建）
  box.dataset.built = '1';
  const provRows = SHARE_PROVS.map(pv => `<div class="prow">
      <span class="pvn">${esc(providerMeta(pv).label)}</span>
      <label class="switch"><input type="checkbox" id="sc_${pv}_en" checked><i></i></label>
      <span class="lbl">${shareLimitLabel(pv)}</span>
      <input type="number" min="0" id="sc_${pv}_limit" value="0">
      <input type="text" id="sc_${pv}_models" placeholder="${t('share_models_ph')}">
    </div>`).join('');
  box.innerHTML = `<div style="margin-top:12px">
    <div class="row" style="gap:10px;margin-bottom:2px">
      <input type="text" id="shareName" placeholder="${t('share_name_ph')}" style="flex:1;max-width:300px">
    </div>
    <div class="share-provs">${provRows}</div>
    <div class="cfoot"><button class="primary" id="btnShareCreate">${t('share_create')}</button></div>
  </div>`;
  $('#btnShareCreate').addEventListener('click', async () => {
    const name = $('#shareName').value.trim();
    const providers = {};
    SHARE_PROVS.forEach(pv => {
      providers[pv] = {
        enabled: $(`#sc_${pv}_en`).checked,
        limit: parseInt($(`#sc_${pv}_limit`).value, 10) || 0,
        models: $(`#sc_${pv}_models`).value.split(',').map(x => x.trim()).filter(Boolean),
      };
    });
    try {
      await post('/api/panel/shares', { name, providers });
      $('#shareName').value = '';
      toast(t('share_created'));
      await loadShares();
    } catch (e) { toast(e.message, true); }
  });
}

/* ───────────────────────── language ───────────────────────── */
$('#langSel').addEventListener('change', e => {
  LANG = e.target.value;
  localStorage.setItem('th_lang', LANG);
  applyI18n();
  buildFmtOptions();
});

/* ───────────────────────── boot ───────────────────────── */
async function boot() {
  applyI18n();
  buildFmtOptions();
  try { cfgData = await api('/api/panel/config'); } catch {}
  await loadOverview();
  renderModels();
  renderExamples();
  renderSettings(); // cfgData 到位后渲染设置页（applyI18n 时配置可能尚未加载）
  loadTunnel();
  loadShares();
  loadUsageLog();
}
boot();
setInterval(loadOverview, 30000);
