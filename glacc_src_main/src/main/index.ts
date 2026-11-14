'use strict';
import requireNodeFile from '@/common/require-node-file';
import * as path from 'path';
import { LOG_TYPE, Elog } from 'xlacc/packages/elog/dist';
const accHelper: any = requireNodeFile(path.join(__rootDir, '../bin/AccHelper.node'));
const pkg = requireNodeFile(path.join(__rootDir, '../app/package.json')) as { version: string };
// 引入ipc模块在其它模块之前
import { client } from '@xunlei/node-net-ipc/dist/ipc-client';
import { mainProcessContext, mainRendererContext } from '@xunlei/node-net-ipc/dist/ipc-base';
import { ThunderChannelList } from '@/common/channel';
import {
  app,
  BrowserWindow,
  ipcMain as ipc,
  Menu,
  nativeImage,
  NativeImage,
  Tray,
  crashReporter,
  IpcMainEvent,
  screen
} from 'electron';
import { ThunderHelper } from '@/common/thunder-helper';
import { createTrayMenu } from '@/main/common/tray';
import { initMessageBox, unregisterMessageBox } from '@/main/init-message-box';
import { MainProcessWindowNS } from '@/main/window-handler';
import * as Sentry from '@sentry/electron/main';
import { createExtensionWindow } from './init-extension';
import { packageName } from '@/common/appconfig';
import { XACC_API_QUERY_CLIENT_UPDATE_URL } from '@/common/xacc-api-config';
import Notifier from '@/main/common/notifier';
import * as ch from 'child_process';
import { PerformanceMonitor, PHASE, IMarkPhaseMetrics } from 'xlacc/packages/performance-monitor/dist';
import { pathToFileURL } from 'url';

app.commandLine.appendSwitch('in-process-gpu');
app.commandLine.appendSwitch('disable-software-rasterizer');
app.commandLine.appendSwitch('disable-site-isolation-trials');
app.commandLine.appendSwitch('no-sandbox');
app.commandLine.appendSwitch('ignore-certificate-errors');
app.commandLine.appendSwitch('allow-insecure-localhost');

const performance = new PerformanceMonitor({
  enableMemoryMonitoring: true,
  monitoringInterval: 10000 // 10秒采集一次
});

performance.markPhaseStart(PHASE.CLIENT_REAY);

const logger = (logText: string) => {
  Elog.info(`Main:${logText}`);
};

logger(`startupBeginTime:${Date.now()}`);
performance.markPhaseStart(PHASE.APP_READY);
const install_channel: string = ThunderHelper.getInstallChannel();
let loadingWindow: BrowserWindow = null;
let mainWindow: BrowserWindow = null;
let appTray: Tray = null; // 放全局变量，不然该对象会被系统回收
let gotTheLock = false;
let assistCheckStartUpData: { p2pRoomId?: string;[key: string]: any } = {};
const APP_SCHEME = 'glacc';
interface ISchemeAction {
  url: string;
  host: string;
  path: string;
  params: Record<string, string>;
}
let isMainRendererReady = false;
const pendingSchemeActions: ISchemeAction[] = [];
/** Loading 硬超时：主窗未 ready-to-show 则兜底退出/重启，避免永久卡在启动动画 */
const LOADING_HARD_TIMEOUT_MS = 30 * 1000;
const LOADING_TIMEOUT_RELAUNCH_FLAG = '--loading-timeout-relaunch';
let loadingTimeoutTimer: ReturnType<typeof setTimeout> | null = null;
let loadingTimedOut = false;

function uninit(): void {
  appTray?.destroy();
  if (gotTheLock) {
    app.releaseSingleInstanceLock();
  }
}

/**
 * 获取需要清理的相关进程名。
 * 实际产物主程序为 lfacc.exe（非 packageName glacc.exe），另有 lfaccservice.exe。
 * 写死 glacc.exe 会导致僵尸/退出清理完全无效，二次启动卡在 Loading。
 */
function getRelatedProcessNames(): string[] {
  const mainExe = path.basename(process.execPath);
  const baseName = mainExe.replace(/\.exe$/i, '');
  const names = new Set<string>([mainExe, `${baseName}service.exe`, `${packageName}.exe`, `${packageName}service.exe`]);
  return Array.from(names);
}

function taskkillProcess(name: string, excludePid?: number): Promise<void> {
  return new Promise((resolve) => {
    const args = ['/F', '/IM', name];
    if (excludePid != null && excludePid > 0) {
      args.push('/FI', `PID ne ${excludePid}`);
    }
    ch.execFile('taskkill.exe', args, { windowsHide: true }, (err, stdout, stderr) => {
      // 进程不存在时 taskkill 返回非 0，属正常情况
      if (err) {
        logger(`taskkill ${name} err: ${err.message}`);
      }
      if (stderr) {
        logger(`taskkill ${name} stderr: ${stderr}`);
      }
      if (stdout) {
        logger(`taskkill ${name} stdout: ${stdout}`);
      }
      resolve();
    });
  });
}

// 清理僵尸进程（排除当前进程，避免自杀）
async function cleanOrphanProcesses() {
  const processNames = getRelatedProcessNames();
  const mainExe = path.basename(process.execPath).toLowerCase();
  logger(`cleanOrphanProcesses: ${processNames.join(',')}, selfPid=${process.pid}`);
  await Promise.all(
    processNames.map((name) => taskkillProcess(name, name.toLowerCase() === mainExe ? process.pid : undefined))
  );
}

// 应用关闭后清理残余进程
async function clearProcessOnAppExit(processNames: string[]) {
  if (!processNames || processNames.length === 0) return;
  // 直接调用 taskkill.exe，不再通过 cmd.exe 中转，减少窗口弹出的几率
  const cmd = 'taskkill.exe';
  const args = ['/F'];
  processNames.forEach((name) => {
    args.push('/IM', name);
  });

  const child = ch.spawn(cmd, args, {
    detached: true,
    stdio: 'ignore',
    windowsHide: true,
    // eslint-disable-next-line @typescript-eslint/ban-ts-comment
    // @ts-ignore
    creationFlags: 0x08000000 | 0x00000008
  });
  child.unref();
}

async function isAppHealth() {
  // hack 应用锁存在，但主窗口异常，则退出应用并杀掉残余进程
  client.start({ name: mainProcessContext });
  const isExitMainRendererContext = (
    await Promise.race([
      new Promise((r) => setTimeout(() => r([false]), 1000)),
      client.isRemoteClientExist(mainRendererContext)
    ])
  )?.[0];
  return isExitMainRendererContext;
}

function clearLoadingTimeout(): void {
  if (loadingTimeoutTimer) {
    clearTimeout(loadingTimeoutTimer);
    loadingTimeoutTimer = null;
  }
}

function hideLoadingWindow(): void {
  clearLoadingTimeout();
  if (loadingWindow && !loadingWindow.isDestroyed()) {
    loadingWindow.hide();
    try {
      performance.markPhaseEnd(PHASE.SHOW_LOADING);
    } catch {
      // phase 可能未开始或已结束
    }
  }
}

function getSchemeUrl(argv: string[]): string | undefined {
  return argv
    .map((arg: string) => arg.replace(/^"|"$/g, ''))
    .find((arg: string) => arg.toLowerCase().startsWith(`${APP_SCHEME}://`));
}

function showMainWindow(): void {
  if (!mainWindow || mainWindow?.isDestroyed()) {
    if (app.isReady()) {
      createMainWindow();
    }
    return;
  }
  if (mainWindow.isMinimized()) {
    mainWindow.restore();
  }
  mainWindow.show();
  mainWindow.focus();
}

function sendSchemeAction(action: ISchemeAction, pending = false): void {
  logger(`sendSchemeAction ${JSON.stringify(action)} ${JSON.stringify(pending)}`);
  if (!mainWindow || mainWindow?.isDestroyed() || pending) {
    pendingSchemeActions.push(action);
    logger(`sendSchemeAction ${JSON.stringify(pendingSchemeActions)}`);
    return;
  }
  mainWindow.webContents.send(ThunderChannelList.channelMRSchemeAction, action);
}

function flushPendingSchemeActions(): void {
  logger(
    `flushPendingSchemeActions ${JSON.stringify(mainWindow?.isDestroyed())} ${JSON.stringify(pendingSchemeActions)} ${JSON.stringify(isMainRendererReady)}`
  );
  if (!mainWindow || mainWindow?.isDestroyed() || !isMainRendererReady) {
    return;
  }
  while (pendingSchemeActions.length > 0) {
    mainWindow.webContents.send(ThunderChannelList.channelMRSchemeAction, pendingSchemeActions.shift());
  }
}

function handleSchemeUrl(rawUrl: string, shouldShowWindow = true, pending = false): void {
  try {
    const parsedUrl = new URL(rawUrl);
    if (parsedUrl.protocol.toLowerCase() !== `${APP_SCHEME}:`) {
      return;
    }

    const params: Record<string, string> = {};
    parsedUrl.searchParams.forEach((value: string, key: string) => {
      params[key] = value;
    });

    const action: ISchemeAction = {
      url: rawUrl,
      host: parsedUrl.hostname.toLowerCase(),
      path: parsedUrl.pathname,
      params
    };

    if (action.host === 'startup' && (params.p2p_roomid || params.p2p_gameid)) {
      assistCheckStartUpData = {
        p2pRoomId: params.p2p_roomid || '',
        p2pGameId: params.p2p_gameid || ''
      };
    }

    logger(`handleSchemeUrl:${JSON.stringify(action)} ${JSON.stringify(pending)}`);
    sendSchemeAction(action, pending);
    if (shouldShowWindow) {
      showMainWindow();
    }
  } catch (error) {
    logger(`handleSchemeUrl invalid url:${rawUrl}, error:${JSON.stringify(error)}`);
  }
}

function handleSchemeArgv(argv: string[], shouldShowWindow = true, pending = false): void {
  const schemeUrl = getSchemeUrl(argv);
  if (schemeUrl) {
    handleSchemeUrl(schemeUrl, shouldShowWindow, pending);
  }
}

function startLoadingTimeout(): void {
  clearLoadingTimeout();
  loadingTimeoutTimer = setTimeout(() => {
    void handleLoadingTimeout();
  }, LOADING_HARD_TIMEOUT_MS);
}

async function handleLoadingTimeout(): Promise<void> {
  if (loadingTimedOut) {
    return;
  }
  // 主窗已可见则只收尾 Loading，不再强制退出
  if (mainWindow && !mainWindow?.isDestroyed() && mainWindow.isVisible()) {
    hideLoadingWindow();
    return;
  }
  loadingTimedOut = true;
  logger(`loading hard timeout after ${LOADING_HARD_TIMEOUT_MS}ms`);
  hideLoadingWindow();

  try {
    // 只清其他实例/服务，排除当前 PID，避免误杀即将 relaunch 的新进程
    await cleanOrphanProcesses();
  } catch (e) {
    logger('loading timeout cleanOrphan failed:' + e);
  }

  uninit();

  const alreadyRetried = process.argv.includes(LOADING_TIMEOUT_RELAUNCH_FLAG);
  if (!alreadyRetried) {
    logger('loading timeout: relaunch once');
    const args = process.argv.slice(1).filter((a) => a !== LOADING_TIMEOUT_RELAUNCH_FLAG);
    args.push(LOADING_TIMEOUT_RELAUNCH_FLAG);
    app.relaunch({ args });
  } else {
    logger('loading timeout: already relaunched once, exit without relaunch');
  }
  // exit 可立即结束卡死启动态；同名进程的自杀式 taskkill 会与 relaunch 竞态，故这里不做
  app.exit(0);
}

const showLoading = (cb: () => void) => {
  logger('showLoading:' + Date.now());
  performance.markPhaseStart(PHASE.SHOW_LOADING);
  loadingWindow = new BrowserWindow({
    show: false,
    frame: false,
    width: 135,
    height: 135,
    resizable: false,
    transparent: true,
    webPreferences: {
      devTools: false
    }
  });

  loadingWindow.once('show', () => {
    startLoadingTimeout();
    cb();
  });
  loadingWindow.loadURL(pathToFileURL(path.join(__dirname, '../static/launch-animation.html')).href).catch(() => {
    startLoadingTimeout();
    cb();
  });
  loadingWindow.on('ready-to-show', () => {
    loadingWindow.show();
  });
};

async function exitRouterSDK() {
  return new Promise((r) => {
    const timer = setTimeout(r, 800);
    client
      .callRemoteClientFunction(mainRendererContext, 'ExitRouterSDK')
      .catch()
      .finally(() => {
        if (timer) clearTimeout(timer);
        r(null);
      });
  });
}

function createMainWindow(): BrowserWindow {
  logger('createMainWindow:' + Date.now());
  isMainRendererReady = false;
  performance.markPhaseStart(PHASE.MAIN_WINDOW_SHOW);
  const width = 1016;
  const height = 674;
  mainWindow = new BrowserWindow({
    width,
    height,
    minWidth: width,
    minHeight: height,
    maxWidth: width,
    maxHeight: height,
    frame: false,
    show: false,
    resizable: false,
    backgroundColor: '#FFF',
    // transparent: true, //使用透明窗口
    webPreferences: {
      devTools: true,
      webSecurity: false,
      spellcheck: false,
      nodeIntegration: true,
      contextIsolation: false,
      webviewTag: true,
      nodeIntegrationInWorker: true,
      preload: `${__outDir}/common-preload.js`
    }
  });

  // 监听渲染进程崩溃
  mainWindow.webContents.on('crashed', (event) => {
    logger('crashed 渲染进程崩溃，尝试恢复...');
    clearLoadingTimeout();
    app.quit();
  });

  // 监听页面加载失败（网络问题等）
  mainWindow.webContents.on('did-fail-load', (event, errorCode, errorDescription) => {
    logger(`did-fail-load 加载失败: ${errorCode}-${errorDescription}，3秒后重试`);
    clearLoadingTimeout();
    app.quit();
  });

  // 监听渲染进程无响应（卡死）
  mainWindow.webContents.on('unresponsive', () => {
    logger('unresponsive 页面无响应，尝试强制重载');
    clearLoadingTimeout();
    app.quit();
  });

  // In electron >= 14.0.0 the remote module is disabled by default for any WebContents instance
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  require('@electron/remote/main').enable(mainWindow.webContents);
  MainProcessWindowNS.registerFunctions();

  // 修改UA
  const userAgent: string = mainWindow.webContents.session.getUserAgent();
  const uaArr: string[] = userAgent.split(' ');
  for (let index = 0; index < uaArr.length; index++) {
    const sub: string = uaArr[index];
    if (sub.toLowerCase().indexOf(`${packageName}/`) === 0) {
      uaArr.splice(index, 1);
      break;
    }
  }
  const appVersion: string = ThunderHelper.getFileVersion(process.execPath);
  uaArr.unshift(`${packageName}/` + appVersion);
  mainWindow.webContents.setUserAgent(uaArr.join(' '));
  mainWindow.loadURL(pathToFileURL(path.join(__dirname, 'main-renderer/index.html')).href).catch((error) => {
    logger('createMainWindow loadURL' + error);
  });
  mainWindow.once('ready-to-show', () => {
    logger('createMainWindow ready-to-show' + Date.now());
    // 主窗已就绪即取消硬超时，避免后续逻辑异常时仍被超时杀掉
    clearLoadingTimeout();
    if (appTray) {
      hideLoadingWindow();
      return;
    }
    try {
      const image: NativeImage = nativeImage.createFromPath(`${__rootDir}/static/${packageName}.ico`);
      mainWindow.setIcon(image);
    } catch (e) {
      logger('createFromPath failed!' + e);
    }

    mainWindow.show();
    // 创建托盘图标
    appTray = createTrayMenu(mainWindow, app.quit);
    setTimeout(() => {
      new Notifier();
    }, 0);
    setTimeout(createExtensionWindow, 500);
    setTimeout(() => {
      initMessageBox(mainWindow);
      hideLoadingWindow();
    }, 1000);
    performance.markPhaseEnd(PHASE.MAIN_WINDOW_SHOW);
    performance.markPhaseEnd(PHASE.CLIENT_REAY);
  });

  mainWindow.on('session-end', () => {
    uninit();
  });

  // eslint-disable-next-line @typescript-eslint/no-misused-promises
  mainWindow.on('close', async (e) => {
    if (global.__exit_block === 'Exit_Default') {
      // 初始化状态，阻止关闭，并询问主渲染窗口
      const isExitMainRendererContext = (await client.isRemoteClientExist(mainRendererContext))?.[0];
      if (isExitMainRendererContext) {
        e.preventDefault();
        client.callRemoteClientFunction(mainRendererContext, 'CloseMainWindow');
      }
      logger('mainWindow close preventDefault：' + global.__exit_block);
    } else if (global.__exit_block === 'Exit_Immediate') {
      e.preventDefault();
      const block = (await client.callRemoteClientFunction(mainRendererContext, 'CloseMainWindowBlockAfterCheck'))?.[0];
      if (block) {
        return;
      }
      global.__exit_block = 'Exit_Padding';
      mainWindow.hide();
      unregisterMessageBox();
      await exitRouterSDK().catch();
      mainWindow.close();
    } else {
      global.__exit_block = 'Exit_Default';
      // 关闭所有窗口
      const windows = BrowserWindow.getAllWindows();
      windows.forEach((window) => {
        window.removeAllListeners();
        window.close();
      });
      logger('mainWindow close allWindows:' + global.__fromExitBlock);
    }
  });
  return mainWindow;
}

function openAccUpdate() {
  return accHelper.shellExecute(
    0,
    'open',
    path.join(path.dirname(process.execPath), 'AccUpdate.exe'),
    '',
    path.dirname(process.execPath),
    'SW_HIDE'
  );
}

async function checkAppUpdate(): Promise<boolean> {
  // Scheme 冷启动必须先完成协议行为；更新器重启客户端时不会转发原始 URL。
  if (getSchemeUrl(process.argv || [])) {
    logger('skip app update for scheme startup');
    return false;
  }
  for (let i = 0; i < process.argv.length; i++) {
    if (process.argv[i].indexOf('FromUpdate') > -1) {
      return false;
    }
  }

  let bUpdate = false;
  const appVersion: string = ThunderHelper.getFileVersion(process.execPath);
  const peerid: string = accHelper.getPeerId();
  const url = XACC_API_QUERY_CLIENT_UPDATE_URL + `?X-Client-Version=${pkg.version || appVersion}&peerid=${peerid}`;
  logger('appVersion:' + appVersion + ';peerid:' + peerid);
  const axios: any = await require('axios');
  try {
    const res: any = await axios.get(url, {
      timeout: 5000,
      headers: { 'package-name': packageName, 'install-channel': install_channel }
    });
    console.log('get update info=', res);
    logger('checkAppUpdate:' + JSON.stringify(res?.data));
    if (res?.status !== 200) {
      logger('get update info falied ');
    } else {
      if (res?.data?.code === 0 && res?.data?.data?.link?.length > 0) {
        bUpdate = true;
      }
    }
  } catch (e) {
    console.log('get update info failed, e=', e);
  }
  return bUpdate;
}

function initSentry(): void {
  const sentryUrl = 'https://387b5bf5f4e640739e49b983aa1bcad8@sentry-jsq.xunlei.com/5';
  const appVersion = ThunderHelper.getFileVersion(process.execPath);
  const peerid = accHelper.getPeerId();
  Sentry.init({ dsn: sentryUrl });
  Sentry.setTag('appVersion', appVersion);
  Sentry.setUser({ id: peerid, appVersion });

  crashReporter.start({
    companyName: packageName,
    productName: packageName,
    ignoreSystemCrashHandler: false,
    submitURL: sentryUrl
  });
}

async function shouldQuit() {
  // 是否退出
  let isExit = false;
  // 用导出的，主要是为了安装升级等协同使用(需要以这个锁来实现单例，比如升级中会创建这个锁，防止acc起来)
  const bExist: number = accHelper.checkMutex();
  // 用electron的主要是能拉起前面一个用例
  gotTheLock = app.requestSingleInstanceLock();
  logger('bExist:' + bExist + gotTheLock);

  if (bExist) {
    const isHealth = await isAppHealth();
    logger('app isHealth:' + isHealth);
    if (gotTheLock || !isHealth) {
      await cleanOrphanProcesses();
    } else {
      isExit = true;
    }
  }
  return isExit;
}

async function main() {
  process.on('uncaughtException', (err) => {
    Elog.error('uncaughtException: ' + err);
  });

  process.on('unhandledRejection', (reason: Error) => {
    Elog.error('unhandledRejection: ' + reason);
    if (reason?.message && reason?.message?.endsWith('/main-renderer/index.html')) app.quit();
  });

  process.on('SIGINT', (): void => {
    app.quit();
  });

  const isQuit = await shouldQuit();
  if (isQuit) {
    Elog.info('main app exit');
    app.quit();
    return;
  } else {
    Elog.info('main app launch');
  }

  app.on('ready', () => {
    app.setAccessibilitySupportEnabled(false);
    Menu.setApplicationMenu(null);
    // 开始性能监控
    performance.markPhaseEnd(PHASE.APP_READY);
    performance.startMonitoring();
    if (!app.isDefaultProtocolClient(APP_SCHEME)) {
      app.setAsDefaultProtocolClient(APP_SCHEME);
    }
  });

  // Quit when all windows are closed.
  app.on('window-all-closed', (): void => {
    // On OS X it is common for applications and their menu bar
    // to stay active until the user quits explicitly with Cmd + Q
    if (process.platform !== 'darwin') {
      app.quit();
    }
  });

  let ylhProcess: ch.ChildProcess | undefined;

  app.on('before-quit', (): void => {
    logger('before-quit');
    try {
      if (ylhProcess && !ylhProcess.killed) {
        ylhProcess.kill();
      }
    } catch (e) {
      logger('kill ylhProcess failed:' + e);
    }
    uninit();
  });

  app.on('will-quit', () => {
    clearProcessOnAppExit(getRelatedProcessNames());
  });

  app.on('gpu-process-crashed', (event, kill) => {
    Elog.warn('app:gpu-process-crashed' + event + kill);
    client
      .callRemoteClientFunction(
        mainRendererContext,
        'TrackEvent',
        'core_event',
        'renderer_process_crashed',
        '',
        0,
        0,
        0,
        0,
        { type: 'gpu-process-crashed' }
      )
      .catch();
  });

  app.on('renderer-process-crashed', (event, webContents, kill) => {
    Elog.warn('app:renderer-process-crashed' + event + webContents + kill);
    client
      .callRemoteClientFunction(
        mainRendererContext,
        'TrackEvent',
        'core_event',
        'renderer_process_crashed',
        '',
        0,
        0,
        0,
        0,
        { type: 'renderer-process-crashed' }
      )
      .catch();
  });

  app.on('render-process-gone', (event, webContents, details) => {
    Elog.warn('app:render-process-gone' + event + webContents + details);
    client
      .callRemoteClientFunction(
        mainRendererContext,
        'TrackEvent',
        'core_event',
        'renderer_process_crashed',
        '',
        0,
        0,
        0,
        0,
        { type: 'app:render-process-gone' }
      )
      .catch();
  });

  app.on('child-process-gone', (event, details) => {
    Elog.warn('app:child-process-gone' + event + details);
  });

  app.on('second-instance', (event: Event, argv: string[], workingDirectory: string, additionalData: unknown) => {
    logger(`second-instance:${JSON.stringify(argv)}`);
    handleSchemeArgv(argv);
    showMainWindow();
  });

  app.on('browser-window-created', (_, window) => {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    require('@electron/remote/main').enable(window.webContents);
  });

  app.on('browser-window-focus', (_event, window) => {
    client.callRemoteClientFunction(mainRendererContext, 'BrowserWindowFocus', window.id, mainWindow.id);
  });
  // 初始化remote
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  require('@electron/remote/main').initialize();

  const pids: number[] = [];
  // 渲染进程发送消息给主进程，监控渲染进程异常退出时主进程也退出
  ipc.on(ThunderChannelList.channelRMProcessSend, (event: any, pid: number): void => {
    pids.push(pid);
    client.start({ name: mainProcessContext });
  });

  setInterval(() => {
    for (let i: number = pids.length - 1; i >= 0; --i) {
      try {
        process.kill(pids[i], 0);
      } catch (e) {
        app.quit();
      }
    }
  }, 1000);

  // 获取主渲染窗口webContentId
  ipc.handle(ThunderChannelList.channelGetMRWinWebContentId, (): number => {
    return mainWindow.webContents.id;
  });

  // 主进程监听渲染进程传递过来的回调函数
  ipc.handle(ThunderChannelList.channelGetAssistStartupData, (event, args) => {
    return assistCheckStartUpData;
  });

  ipc.on(ThunderChannelList.channelMRRendererReady, (event: IpcMainEvent) => {
    logger('main renderer scheme listener ready');
    isMainRendererReady = true;
    flushPendingSchemeActions();
  });

  try {
    ylhProcess = ch.fork(path.join(__outDir, 'ylh.js'));
    ipc.handle('SDKInit', async (event: any, ...args): Promise<number> => {
      const ret = await new Promise<number>((r) => {
        ylhProcess.once('message', (data) => {
          Elog.info('SDKInit:' + JSON.stringify(data));
          r(data as number);
        });
        ylhProcess.send({ type: 'SDK_INIT', args });
      });
      return ret;
    });

    ipc.handle('GetMainRendererADHwnd', async (event: any, ...args): Promise<number> => {
      const ret = await new Promise<number>((r) => {
        ylhProcess.once('message', (data) => {
          Elog.info('GetMainRendererADHwnd:' + data);
          r(data as number);
        });
        ylhProcess.send({ type: 'GET_HWND', args });
      });
      return ret;
    });
  } catch (err) {
    Elog.info('utilityProcess:' + err);
  }

  ipc.on('hideMainWindow', async (event: unknown, from: string): Promise<void> => {
    // mainWindow.minimize();
    mainWindow.hide();
  });

  ipc.on('closeMainWindow', async (event: unknown, from: string): Promise<void> => {
    global.__fromExitBlock = true;
    global.__exit_block = 'Exit_Immediate';
    mainWindow.close();
  });

  ipc.on('minizeMainWindow', (): void => {
    if (mainWindow !== null) {
      mainWindow.minimize();
    }
  });

  ipc.on('moveTopMainWindow', (): void => {
    if (mainWindow !== null) {
      mainWindow.show();
      mainWindow.moveTop();
    }
  });

  // 日志记录
  ipc.on(ThunderChannelList.channeElog, (event: IpcMainEvent, arg: { type: LOG_TYPE; value: string }) => {
    const { type, value } = arg;
    switch (type) {
      case 'info':
        Elog.info(value);
        break;
      case 'error':
        Elog.error(value);
        break;
      case 'warn':
        Elog.warn(value);
        break;
      case 'debug':
        Elog.debug(value);
        break;
      default:
        console.log('Unknown log type:', type, ...value);
        break;
    }
  });

  // 标记记录开始阶段耗时
  ipc.on(ThunderChannelList.channeMarkPhaseStart, (event: IpcMainEvent, phase: string) => {
    return performance.markPhaseStart(phase);
  });

  // 标记记录结束阶段耗时
  ipc.handle(ThunderChannelList.channeMarkPhaseEnd, (event: IpcMainEvent, phase: string): IMarkPhaseMetrics => {
    performance.markPhaseEnd(phase);
    return performance.getRecordMarkPhase(phase);
  });

  // 向其他进程回传阶段耗时
  ipc.handle(ThunderChannelList.channeGetRecordMarkPhase, (event: IpcMainEvent, phase: string): IMarkPhaseMetrics => {
    return performance.getRecordMarkPhase(phase);
  });

  // 向其他进程回传阶段耗时
  ipc.handle(ThunderChannelList.channeGetTotalMemory, (event: IpcMainEvent): string => {
    return performance.getAppMetrics();
  });

  // 获取屏幕信息
  ipc.handle(ThunderChannelList.channelGetScreenInfo, async () => {
    const { x, y } = screen.getCursorScreenPoint();
    const primaryDisplay = screen.getPrimaryDisplay();
    return { x, y, size: primaryDisplay.workAreaSize, ...primaryDisplay };
  });

  handleSchemeArgv(process.argv || [], false, true);
  checkAppUpdate()
    .then((bUpdate: boolean) => {
      if (bUpdate) {
        const n = openAccUpdate();
        return n > 32;
      } else {
        return false;
      }
    })
    .then((bUpdate: boolean) => {
      if (bUpdate) {
        app.quit();
      } else {
        if (app.isReady()) {
          showLoading(createMainWindow);
        } else {
          app.on('ready', () => {
            showLoading(createMainWindow);
          });
        }
      }
    });
  initSentry();
}

main();
