import * as path from 'path';
import { BrowserWindow, ipcMain, BrowserWindowConstructorOptions } from 'electron';
// import { roundRectWindow } from '@/main/round-rect-window';
import { MessageBox } from '@/common/message-box/index';
import { MessageBoxNS } from '@/common/message-box/message-box-helper';
import { client as clientModule } from '@xunlei/node-net-ipc/dist/ipc-client';
import { mainRendererContext } from '@xunlei/node-net-ipc/dist/ipc-base';
import { pathToFileURL } from 'url';
import { Elog } from 'xlacc/packages/elog/dist';

const modalMap: { [popType: string]: BrowserWindow } = {};
const singletonMap: { [popType: string]: BrowserWindow } = {};
const pendingResults = new WeakMap<BrowserWindow, Promise<MessageBoxNS.IResolvePayload>>();
const windowStates = new WeakMap<
  BrowserWindow,
  { boxId: number; popType: string; reason?: string; completed?: boolean; settleFailure?: (reason: string) => void }
>();
const initializationTimeoutMs = 15000;

function logMessageBox(window: BrowserWindow, event: string, details: Record<string, any> = {}): void {
  const state = windowStates.get(window);
  Elog.info(`MessageBox:${JSON.stringify({ boxId: state?.boxId, popType: state?.popType, event, ...details })}`);
}

// Initialization timeout is limited to confirm dialogs; custom windows keep their own startup lifecycle.
function monitorMessageBox(window: BrowserWindow): (reason: string, details?: Record<string, any>) => void {
  const state = windowStates.get(window);
  const contents = window.webContents;
  const monitorInitialization = state.popType === 'confirm';
  const readyChannel = `message-box-ready-${state.boxId}`;
  const failedChannel = `message-box-init-failed-${state.boxId}`;
  let componentReady = false;
  let initialized = false;
  const fail = (reason: string, details: Record<string, any> = {}): void => {
    if (window.isDestroyed() || state.reason) return;
    state.reason = reason;
    logMessageBox(window, 'failure', { reason, ...details });
    // 上报失败不能阻断窗口清理和等待结果的释放。
    Promise.resolve().then(() => clientModule.callRemoteClientFunction(
      mainRendererContext, 'TrackEvent', 'core_event', 'renderer_process_crashed', '', 0, 0, 0, 0,
      { type: 'message-box-failure', boxId: state.boxId, popType: state.popType, reason, ...details }
    )).catch(() => {});
    // Commit the failure before destroy/closed can interleave with an in-flight result IPC.
    state.settleFailure?.(reason);
    window.destroy();
  };
  const timer = monitorInitialization
    ? setTimeout(() => fail('initialization-timeout', { componentReady }), initializationTimeoutMs)
    : undefined;
  const finishInitialization = (): void => {
    if (!monitorInitialization || initialized || !componentReady || window.isDestroyed()) return;
    if (!window.isVisible()) return;
    initialized = true;
    clearTimeout(timer);
    logMessageBox(window, 'initialized');
    ipcMain.removeListener(readyChannel, onReady);
    ipcMain.removeListener(failedChannel, onInitFailure);
  };
  const onReady = (event: Electron.IpcMainEvent): void => {
    if (event.sender !== contents) return;
    componentReady = true;
    logMessageBox(window, 'component-ready');
    finishInitialization();
  };
  const onInitFailure = (event: Electron.IpcMainEvent): void => {
    if (event.sender === contents) fail('component-init-failed');
  };
  const onShow = (): void => {
    logMessageBox(window, 'shown');
    finishInitialization();
  };
  const onGone = (_event: Electron.Event, details: Electron.RenderProcessGoneDetails): void => {
    fail('renderer-gone', { processReason: details.reason, exitCode: details.exitCode });
  };
  const onLoadFailure = (
    _event: Electron.Event,
    errorCode: number,
    _description: string,
    _url: string,
    isMainFrame: boolean
  ): void => {
    if (isMainFrame && errorCode !== -3) fail('load-failed', { errorCode });
  };
  if (monitorInitialization) {
    ipcMain.on(readyChannel, onReady);
    ipcMain.on(failedChannel, onInitFailure);
  }
  window.on('show', onShow);
  contents.on('render-process-gone', onGone);
  contents.on('did-fail-load', onLoadFailure);
  window.once('closed', () => {
    clearTimeout(timer);
    ipcMain.removeListener(readyChannel, onReady);
    ipcMain.removeListener(failedChannel, onInitFailure);
    window.removeListener('show', onShow);
    contents.removeListener('render-process-gone', onGone);
    contents.removeListener('did-fail-load', onLoadFailure);
    logMessageBox(window, 'closed', { reason: state.reason ?? 'closed' });
  });
  return fail;
}

const defaultWidth = 472;
const defaultHeight = 224;

function isWindowUsable(window?: BrowserWindow): boolean {
  return (
    !!window &&
    !window.isDestroyed() &&
    !!window.webContents &&
    !window.webContents.isDestroyed() &&
    !window.webContents.isCrashed()
  );
}

function getCachedWindow(windows: { [popType: string]: BrowserWindow }, popType: string): BrowserWindow | undefined {
  const window = windows[popType];
  if (isWindowUsable(window) && !windowStates.get(window)?.completed) {
    logMessageBox(window, 'reuse');
    return window;
  }
  if (window) {
    logMessageBox(window, 'discard-cache');
    delete windows[popType];
    if (!window.isDestroyed()) {
      window.destroy();
    }
  }
  return undefined;
}

function waitForMessageBox(
  window: BrowserWindow,
  makePayload: (action: MessageBoxNS.Action, args: any) => MessageBoxNS.IResolvePayload
): Promise<MessageBoxNS.IResolvePayload> {
  const state = windowStates.get(window);
  if (state.reason || window.isDestroyed()) {
    const reason = state.reason ?? 'closed';
    logMessageBox(window, 'result', { action: MessageBoxNS.Action.Close, reason });
    return Promise.resolve({ action: MessageBoxNS.Action.Close, reason });
  }
  const pending = pendingResults.get(window);
  if (pending) {
    window.focus();
    logMessageBox(window, 'result', { action: MessageBoxNS.Action.Close, reason: 'duplicate' });
    return Promise.resolve({ action: MessageBoxNS.Action.Close, reason: 'duplicate' });
  }

  const boxId = window.id;
  const channel = `message-box-resolve-${boxId}`;
  const contents = window.webContents;
  const result = new Promise<MessageBoxNS.IResolvePayload>((resolve) => {
    let settled = false;
    const settle = (payload: MessageBoxNS.IResolvePayload): void => {
      if (settled) return;
      settled = true;
      ipcMain.removeListener(channel, onResolve);
      window.removeListener('closed', onClose);
      pendingResults.delete(window);
      state.completed = true;
      delete state.settleFailure;
      logMessageBox(window, 'result', { action: payload.action, reason: payload.reason ?? 'user-action' });
      resolve(payload);
    };
    const onResolve = (event: Electron.IpcMainEvent, action: MessageBoxNS.Action, args: any): void => {
      if (event.sender !== contents) return;
      if (state.reason) {
        settle({ action: MessageBoxNS.Action.Close, reason: state.reason });
        return;
      }
      settle(makePayload(action, args));
    };
    const onClose = (): void => {
      // The renderer sends its choice immediately before closing; let that IPC arrive first.
      setImmediate(() =>
        settle({ action: MessageBoxNS.Action.Close, reason: windowStates.get(window)?.reason ?? 'closed' })
      );
    };

    state.settleFailure = (reason) => settle({ action: MessageBoxNS.Action.Close, reason });
    ipcMain.on(channel, onResolve);
    window.once('closed', onClose);
  });
  pendingResults.set(window, result);
  return result;
}

function create(
  args: {
    popType: string;
    options: { singleton?: boolean; windowWidth?: number; windowHeight?: number; [prop: string]: any };
    dialogConf: BrowserWindowConstructorOptions;
  },
  parent: BrowserWindow
): BrowserWindow {
  let customWindow: BrowserWindow = undefined;
  do {
    let windowOptions: BrowserWindowConstructorOptions = {
      width: defaultWidth,
      height: defaultHeight,
      frame: false,
      resizable: false,
      maximizable: false,
      minimizable: false,
      parent: parent,
      show: false,
      modal: true,
      alwaysOnTop: false,
      transparent: true, //使用透明窗口
      backgroundColor: '#00000000', //设置背景色为全透明
      webPreferences: {
        preload: `${__outDir}/common-preload.js`,
        spellcheck: false,
        nodeIntegration: true,
        contextIsolation: false,
        webSecurity: process.env.RUN_ENV !== 'development'
      }
    };

    if (args.dialogConf) {
      windowOptions = { ...windowOptions, ...args.dialogConf };
      if (args.dialogConf.webPreferences) {
        windowOptions.webPreferences = {
          spellcheck: false,
          nodeIntegration: true,
          contextIsolation: false,
          webSecurity: process.env.RUN_ENV !== 'development',
          ...args.dialogConf.webPreferences
        };
      }
    }

    const modalWindow = windowOptions.modal && getCachedWindow(modalMap, args.popType);
    if (modalWindow) {
      customWindow = modalWindow;
      break;
    }

    // 是否为单例窗口
    if (args.options.singleton) {
      const singletonWindow = getCachedWindow(singletonMap, args.popType);
      if (singletonWindow) {
        customWindow = singletonWindow;
        break;
      }
    }

    customWindow = new BrowserWindow(windowOptions);
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    require('@electron/remote/main').enable(customWindow.webContents);
    const boxId = customWindow.id;
    windowStates.set(customWindow, { boxId, popType: args.popType });
    logMessageBox(customWindow, 'created', { parentId: parent?.id, modal: !!windowOptions.modal });
    const fail = monitorMessageBox(customWindow);
    if (windowOptions.modal) {
      modalMap[args.popType] = customWindow;
    }
    if (args.options.singleton) {
      singletonMap[args.popType] = customWindow;
    }

    try {
      if (parent) {
        const bounds = parent.getBounds();
        customWindow.setBounds({
          x: Math.round(bounds.x + (bounds.width - windowOptions.width) / 2),
          y: Math.round(bounds.y + (bounds.height - windowOptions.height) / 2)
        });
      }
    } catch (err) {
      console.log('customWindow', err);
    }

    if (process.env.RUN_ENV === 'development') {
      customWindow.loadURL(`http://localhost:9080/message-box-renderer/?boxId=${boxId}`).catch(() => {
        fail('load-rejected');
      });
    } else {
      customWindow
        .loadURL(pathToFileURL(path.join(__dirname, 'message-box-renderer/index.html')).href + `?boxId=${boxId}`)
        .catch(() => {
          fail('load-rejected');
        });
    }
    // customWindow.webContents.executeJavaScript(fs.readFileSync(`${__dirname}/message-box-renderer/renderer.js`, 'utf8')).catch();

    customWindow.on('ready-to-show', () => {
      if (isWindowUsable(customWindow)) {
        logMessageBox(customWindow, 'init-sent');
        customWindow.webContents.send(`message-box-init-reply-${boxId}`, { ...args });
      }
      // customWindow.webContents.openDevTools();
      // roundRectWindow(customWindow, parentSpecified);
    });

    customWindow.on('close', () => {
      if (parent && !parent.isDestroyed()) {
        parent.focus();
      }
      // Preserve the existing system-menu close action and checkbox payload for a healthy renderer.
      // Destruction after failure bypasses this path and is settled by the main process.
      if (!windowStates.get(customWindow)?.completed && isWindowUsable(customWindow)) {
        customWindow.webContents.send('message-box-resolve-systemmenu');
      }
    });

    customWindow.on('closed', () => {
      if (modalMap[args.popType] === customWindow) delete modalMap[args.popType];
      if (singletonMap[args.popType] === customWindow) delete singletonMap[args.popType];
    });
  } while (0);

  return customWindow;
}

async function confirmMessageBox(
  args: {
    popType: string;
    options: { singleton?: boolean; windowWidth?: number; windowHeight?: number; [prop: string]: any };
    dialogConf: BrowserWindowConstructorOptions;
  },
  parent: BrowserWindow
): Promise<MessageBoxNS.IResolvePayload> {
  const confirmWindow = create(args, parent);
  return waitForMessageBox(confirmWindow, (action, checkboxChecked) => ({ action, checkboxChecked }));
}

async function customMessageBox(
  args: {
    popType: string;
    options: { singleton?: boolean; windowWidth?: number; windowHeight?: number; [prop: string]: any };
    dialogConf: BrowserWindowConstructorOptions;
  },
  parentSpecified: BrowserWindow
): Promise<MessageBoxNS.IResolvePayload> {
  const customWindow = create(args, parentSpecified);
  return waitForMessageBox(customWindow, (action, resultArgs) => ({ action, args: resultArgs }));
}

export function newBrowserWindow(dialogConf: BrowserWindowConstructorOptions): BrowserWindow {
  let windowOptions: BrowserWindowConstructorOptions = {
    width: 450,
    height: 174,
    frame: false,
    resizable: false,
    maximizable: false,
    minimizable: false,
    show: false,
    modal: true,
    alwaysOnTop: false,
    webPreferences: {
      spellcheck: false,
      nodeIntegration: true,
      contextIsolation: false,
      webSecurity: process.env.RUN_ENV !== 'development'
    }
  };

  if (dialogConf) {
    windowOptions = { ...windowOptions, ...dialogConf };
    if (dialogConf.webPreferences) {
      windowOptions.webPreferences = {
        spellcheck: false,
        nodeIntegration: true,
        contextIsolation: false,
        webSecurity: process.env.RUN_ENV !== 'development',
        ...dialogConf.webPreferences
      };
    }
  }

  const emptyWnd = new BrowserWindow(windowOptions);
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  require('@electron/remote/main').enable(emptyWnd.webContents);

  return emptyWnd;
}

export function initMessageBox(parent: BrowserWindow): void {
  clientModule.registerFunctions({
    // 创建 Confirm 对话框
    MessageBoxConfirm: async (
      remoteId: string,
      args: { options: MessageBox.IConfirmOptions; dialogConf: BrowserWindowConstructorOptions }
    ): Promise<MessageBoxNS.IResolvePayload> => {
      console.log('confirm create enter');
      let parentSpecified: BrowserWindow = parent;
      if (args.options.parentId) {
        const parentArg: BrowserWindow = BrowserWindow.fromId(args.options.parentId);
        if (parentArg) {
          parentSpecified = parentArg;
        }
      }

      return confirmMessageBox(
        {
          popType: 'confirm',
          options: args.options,
          dialogConf: Object.assign({}, args.dialogConf, {
            width: args.options.windowWidth,
            height: args.options.windowHeight
          })
        },
        parentSpecified
      );
    },
    // 创建预设的自定义对话框
    MessageBoxCustom: (
      remoteId: string,
      args: {
        popType: string;
        options: {
          parentId?: number;
          singleton?: boolean;
          windowWidth?: number;
          windowHeight?: number;
          [prop: string]: any;
        };
        dialogConf: BrowserWindowConstructorOptions;
      }
    ) => {
      let parentSpecified: BrowserWindow = parent;
      if (typeof args.options.parentId === 'number') {
        if (args.options.parentId === -1) {
          // 支持不设置父窗口
          parentSpecified = null;
        } else {
          const parentArg: BrowserWindow = BrowserWindow.fromId(args.options.parentId);
          if (parentArg) {
            parentSpecified = parentArg;
          }
        }
      }

      args.dialogConf = Object.assign({}, args.dialogConf, {
        width: args.options.windowWidth,
        height: args.options.windowHeight
      });
      return customMessageBox(args, parentSpecified);
    },
    // 创建一个窗口，不关注点击结果，返回窗口id
    MessageBoxCreate: (
      remoteId: string,
      args: {
        popType: string;
        options: {
          parentId?: number;
          singleton?: boolean;
          windowWidth?: number;
          windowHeight?: number;
          [prop: string]: any;
        };
        dialogConf: BrowserWindowConstructorOptions;
      },
      onReadyToShow?: (id: number) => void
    ) => {
      let parentSpecified: BrowserWindow = parent;
      if (typeof args.options.parentId === 'number') {
        if (args.options.parentId === -1) {
          // 支持不设置父窗口
          parentSpecified = null;
        } else {
          const parentArg: BrowserWindow = BrowserWindow.fromId(args.options.parentId);
          if (parentArg) {
            parentSpecified = parentArg;
          }
        }
      }

      args.dialogConf = Object.assign({}, args.dialogConf, {
        width: args.options.windowWidth,
        height: args.options.windowHeight
      });
      const wnd = create(args, parentSpecified);
      if (wnd && onReadyToShow) {
        wnd.once('ready-to-show', () => {
          onReadyToShow(wnd.id);
          // wnd.webContents.openDevTools();
        });
      }

      return wnd?.id;
    },
    CreateBrowserWindow: (
      remoteId: string,
      dialogConf: BrowserWindowConstructorOptions & { parentId?: number }
    ): number => {
      let parentSpecified: BrowserWindow = parent;
      if (typeof dialogConf.parentId === 'number') {
        if (dialogConf.parentId === -1) {
          // 支持不设置父窗口
          parentSpecified = null;
        } else {
          const parentArg: BrowserWindow = BrowserWindow.fromId(dialogConf.parentId);
          if (parentArg) {
            parentSpecified = parentArg;
          }
        }
      }

      dialogConf = dialogConf ?? {};
      dialogConf.parent = parentSpecified;
      const wnd = newBrowserWindow(dialogConf);

      return wnd.id;
    }
  });
}

export function unregisterMessageBox(): void {
  clientModule.unRegisterFunctions([
    'MessageBoxConfirm',
    'MessageBoxCustom',
    'MessageBoxCreate',
    'CreateBrowserWindow'
  ]);
}

/** 弹出一个 confirm 对话框 */
export async function confirm(
  options: MessageBox.IConfirmOptions,
  dialogConf: BrowserWindowConstructorOptions,
  parent: BrowserWindow
): Promise<MessageBoxNS.IResolvePayload> {
  return confirmMessageBox(
    {
      popType: 'confirm',
      options,
      dialogConf: Object.assign({}, dialogConf, { width: options.windowWidth, height: options.windowHeight })
    },
    parent
  );
}

export async function custom(
  popType: string,
  options: { singleton?: boolean; windowWidth?: number; windowHeight?: number; [prop: string]: any },
  dialogConf: BrowserWindowConstructorOptions,
  parent: BrowserWindow
): Promise<MessageBoxNS.IResolvePayload> {
  return customMessageBox({ popType, options, dialogConf }, parent);
}

export function canConfirmNow(modal = true): boolean {
  // This is a query only; stale windows are cleaned up when creating the next dialog.
  return !modal || !isWindowUsable(modalMap.confirm) || !!windowStates.get(modalMap.confirm)?.completed;
}
