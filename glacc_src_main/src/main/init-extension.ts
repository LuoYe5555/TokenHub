import { BrowserWindow, ipcMain, screen } from 'electron';
import { MRW_WIDTH, MRW_HEIGHT } from '@/common/window-dimension';
import { client } from '@xunlei/node-net-ipc/dist/ipc-client';
import { mainRendererContext } from '@xunlei/node-net-ipc/dist/ipc-base';
import { ThunderChannelList } from '@/common/channel';
import { NavViewID } from '@/main-renderer/store/modules/navs-define';
import { join } from 'path';
import { pathToFileURL } from 'url';
// todo 需要迁移至窗口池管理
export const createExtensionWindow = () => {
  const extensionWindow = new BrowserWindow({
    width: MRW_WIDTH + 20,
    height: MRW_HEIGHT,
    // frame: false,
    transparent: true,
    resizable: false,
    show: false,
    skipTaskbar: true,
    webPreferences: {
      preload: `${__outDir}/common-preload.js`,
      devTools: true,
      webSecurity: false,
      spellcheck: false,
      nodeIntegration: true,
      contextIsolation: false,
      webviewTag: true,
      nodeIntegrationInWorker: true
    }
  });

  extensionWindow.loadURL(pathToFileURL(join(__dirname, 'extensions-renderer/index.html')).href);
  extensionWindow.on('ready-to-show', () => {
    // extensionWindow.show();
    // extensionWindow.webContents.openDevTools();
  });

  // 窗口map
  const winMap = {};
  const winMoveMap = {};
  // 失联间隔
  const ON_BLUR_DURCTION = 30000;
  // 拖动缓冲距离
  const DRAG_BUFFER = 10;
  // 拖动回调距离
  const DRAG_ADJUST = 5;
  // 兼容ai助手老版本插件未透传id问题
  const fallbackId = 'ai-chat';
  ipcMain.on(
    ThunderChannelList.channelWindowCreate,
    (event, options: Electron.BrowserWindowConstructorOptions, url: string, id?: string, uninstall?: boolean) => {
      const mapId = id || fallbackId;
      console.log('channelWindowCreate', mapId);
      if (winMap[mapId]) winMap[mapId].close();

      winMap[mapId] = new BrowserWindow(options);
      winMap[mapId].on('ready-to-show', () => {
        // winMap[url].webContents.openDevTools();
        winMap[mapId].show();
        if (options.alwaysOnTop) winMap[mapId].setAlwaysOnTop(true, 'screen-saver');
      });
      winMap[mapId].loadURL(url);
      winMap[mapId].on('closed', async () => {
        if (mapId === 'p2p-connection') {
          client.callRemoteClientFunction(mainRendererContext, 'HandleTabChange', {
            key: NavViewID.MyGame
          }).catch((error) => console.error('Failed to select home after closing P2P window', error));
        }
        if (typeof uninstall === 'boolean' && uninstall) {
          await client.callRemoteClientFunction('extension-renderer-context', 'UnloadExtension', id);
        }
        winMap[mapId].destroy();
        winMap[mapId] = null;
      });

      // 控制窗口移动
      winMoveMap[mapId] = {
        winStartPosition: { x: 0, y: 0 },
        mouseStartPosition: { x: 0, y: 0 },
        movingInterval: null,
        startTime: 0,
        canMoving: false
      };

      ipcMain.on(
        ThunderChannelList.channelWindowMove,
        (
          event,
          dragging: boolean,
          paddingOptions: Partial<{
            paddingTop: number;
            paddingLeft: number;
            paddingRight: number;
            paddingBottom: number;
          }> = {},
          id?: string
        ) => {
          const targetId = id || fallbackId;
          console.log('channelWindowMove', targetId);
          winMoveMap[targetId].canMoving = dragging;
          winMoveMap[targetId].startTime = Date.now();
          if (winMoveMap[targetId].canMoving) {
            if (winMoveMap[targetId].movingInterval) return;
            if (!winMap[targetId] || winMap[targetId]?.isDestroyed())
              return clearInterval(winMoveMap[targetId].movingInterval);
            // 读取原位置
            const winPosition = winMap[targetId].getPosition();
            winMoveMap[targetId].winStartPosition = { x: winPosition[0], y: winPosition[1] };
            winMoveMap[targetId].mouseStartPosition = screen.getCursorScreenPoint();
            const size = screen.getPrimaryDisplay().workAreaSize;
            const WIN_PADDING_TOP_HEIGHT = paddingOptions.paddingTop || 0;
            const WIN_PADDING_LEFT_HEIGHT = paddingOptions.paddingLeft || 0;
            const WIN_PADDING_RIGHT_HEIGHT = paddingOptions.paddingRight || 0;
            const WIN_PADDING_BOTTOM_HEIGHT = paddingOptions.paddingBottom || 0;

            const move = async () => {
              // 实时更新位置
              const cursorPosition = screen.getCursorScreenPoint();
              let x =
                winMoveMap[targetId].winStartPosition.x + cursorPosition.x - winMoveMap[targetId].mouseStartPosition.x;
              let y =
                winMoveMap[targetId].winStartPosition.y + cursorPosition.y - winMoveMap[targetId].mouseStartPosition.y;

              if (!winMap[targetId] || winMap[targetId]?.isDestroyed())
                return clearInterval(winMoveMap[targetId].movingInterval);
              //获取窗口大小
              const winSize = winMap[targetId].getSize();
              //窗口四个代表性边缘坐标值
              //上边
              const winPointUpY = y;
              //下边
              const winPointDownY = y + winSize[1];
              //左边
              const winPointLeftX = x;
              //右边
              const winPointRightX = x + winSize[0];

              let minimizedFlag = '';

              //窗口上方超出屏幕，重置Y为0
              if (winPointUpY + WIN_PADDING_TOP_HEIGHT <= 0) {
                if (winPointUpY + WIN_PADDING_TOP_HEIGHT < -DRAG_BUFFER) {
                  y = -DRAG_ADJUST - WIN_PADDING_TOP_HEIGHT;
                }

                console.log('上边超出屏幕，重置Y为:', y);
                minimizedFlag = 'minimized-top';
              }

              //窗口下方超出屏幕，重置Y为 屏幕高度最大值 - 窗口高度
              if (winPointDownY - WIN_PADDING_BOTTOM_HEIGHT >= size.height) {
                if (winPointDownY - WIN_PADDING_BOTTOM_HEIGHT >= size.height + DRAG_BUFFER) {
                  y = DRAG_ADJUST + size.height - winSize[1] + WIN_PADDING_BOTTOM_HEIGHT;
                }
                console.log('下方超出屏幕，重置Y为:', y);
                minimizedFlag = 'minimized-bottom';
              }

              //窗口左边超出屏幕，重置X为0
              if (winPointLeftX + WIN_PADDING_LEFT_HEIGHT <= 0) {
                if (winPointLeftX + WIN_PADDING_LEFT_HEIGHT <= -DRAG_BUFFER) {
                  x = -DRAG_ADJUST - WIN_PADDING_LEFT_HEIGHT;
                }
                console.log('左边超出屏幕，重置X为:', x);
                minimizedFlag = 'minimized-left';
              }

              //窗口右边超出屏幕，重置X为 屏幕长度最大值 - 窗口长度
              if (winPointRightX - WIN_PADDING_RIGHT_HEIGHT >= size.width) {
                if (winPointRightX - WIN_PADDING_RIGHT_HEIGHT >= size.width + DRAG_BUFFER) {
                  x = DRAG_ADJUST + size.width - winSize[0] + WIN_PADDING_RIGHT_HEIGHT;
                }
                console.log('右边超出屏幕，重置X为:', x);
                minimizedFlag = 'minimized-right';
              }

              event.sender.send('window:move', minimizedFlag);
              winMap[targetId].setBounds({ x, y, width: options.width, height: options.height });
              // if (minimizedFlag) {
              //   // 移动到边界位置主动停止
              //   clearInterval(winMoveMap[targetId].movingInterval);
              //   winMoveMap[targetId].movingInterval = null;
              // } else {
              //   clearInterval(winMoveMap[targetId].movingInterval);
              //   winMoveMap[targetId].movingInterval = setTimeout(move, 20);
              // }
              clearInterval(winMoveMap[targetId].movingInterval);
              winMoveMap[targetId].movingInterval = null;
              // ipc通知停止移动或ipc拖动事件失联30s主动暂停
              if (winMoveMap[targetId].canMoving && Date.now() - winMoveMap[targetId].startTime < ON_BLUR_DURCTION) {
                winMoveMap[targetId].movingInterval = setTimeout(move, 20);
              }
            };
            move();
          } else {
            clearInterval(winMoveMap[targetId].movingInterval);
            winMoveMap[targetId].movingInterval = null;
          }
        }
      );

      ipcMain.on(ThunderChannelList.channelWindowClose, (event, id) => {
        const targetId = id || fallbackId;
        console.log('channelWindowClose', targetId);
        if (winMap[targetId]) winMap[targetId].close();
      });

      ipcMain.on(ThunderChannelList.channelExtensionUnload, (event, extensionName: string, id?: string) => {
        const targetId = id || fallbackId;
        console.log('channelExtensionUnload', targetId);
        if (mapId !== targetId) return;
        console.log('unload extension:', extensionName);
        client.callRemoteClientFunction('extension-renderer-context', 'UnloadExtension', extensionName);
      });

      ipcMain.on(ThunderChannelList.channelWindowMoveTop, (event, id) => {
        const targetId = id || fallbackId;
        console.log('channelWindowMoveTop', targetId);
        if (winMap[targetId]) {
          if (winMap[targetId]?.isMinimized()) {
            winMap[targetId]?.restore();
          }
          winMap[targetId]?.show();
          winMap[targetId].moveTop();
          winMap[targetId]?.focus();
        }
      });

      ipcMain.on(ThunderChannelList.channelWindowMinimize, (event, id) => {
        const targetId = id || fallbackId;
        console.log('channelWindowMinimize', targetId);
        if (winMap[targetId]) {
          winMap[targetId].minimize();
        }
      });
    }
  );
  // return extensionWindow;
};
