// 托盘图标
// 引入ipc模块在其它模块之前
import { client } from '@xunlei/node-net-ipc/dist/ipc-client';
import { mainRendererContext } from '@xunlei/node-net-ipc/dist/ipc-base';
import { Menu, Tray, NativeImage, nativeImage, BrowserWindow, MenuItemConstructorOptions } from 'electron';
import { callClientFunction } from '@/common/ipc-utilities';
import '@/common/constants';
import { GameAccStatus } from '@/main-renderer/common/AccControl';
import { packageName, productName } from '@/common/appconfig';

export function createTrayMenu(mainWindow: BrowserWindow, fallback: Function): Tray {
  let appTray: Tray = null;
  let image: NativeImage = null;
  try {
    image = nativeImage.createFromPath(`${__rootDir}/static/${packageName}.ico`);
  } catch (e) {
    console.log('createFromPath failed!', e);
  }
  appTray = new Tray(image);

  if (appTray !== null) {
    console.log('settooltip');
    appTray.setToolTip(productName);

    appTray.on('click', (): void => {
      if (mainWindow.isDestroyed()) return fallback();
      // 左键单击时显示窗口q
      if (mainWindow !== null) {
        const isMinimized: boolean = mainWindow.isMinimized();
        if (!isMinimized && mainWindow.isVisible()) {
          mainWindow.minimize();
          mainWindow.hide();
        } else {
          if (isMinimized) {
            mainWindow.restore();
            mainWindow.show();
          } else {
            mainWindow.show();
          }
        }
      }
    });

    appTray.on('right-click', async (): Promise<void> => {
      if (mainWindow.isDestroyed()) return fallback();
      const contextMenuTemplate: MenuItemConstructorOptions[] = [
        {
          type: 'normal',
          label: '退出(&X)',
          icon: `${__rootDir}/static/icon/tray/exit.png`,
          async click(): Promise<void> {
            global.__exit_block = 'Exit_Immediate';
            const gameStatus = await client.callRemoteClientFunction(
              mainRendererContext,
              'getCurrentAccelerateGameStatus'
            );
            // 设置-退出主程序
            const notHideToTrayAndExit = await callClientFunction(
              mainRendererContext,
              'GetConfigValue',
              'ConfigNormalSession',
              'ConfigNormal_Not_Hide_To_Tray_And_Exit',
              false
            );
            if (gameStatus?.[0] === GameAccStatus.Started) {
              if (mainWindow !== null) {
                const isMinimized: boolean = mainWindow.isMinimized();
                if (!isMinimized && mainWindow.isVisible()) {
                  mainWindow.minimize();
                  mainWindow.hide();
                } else {
                  if (isMinimized) {
                    mainWindow.restore();
                    mainWindow.show();
                  } else {
                    mainWindow.show();
                  }
                }
              }
              if (notHideToTrayAndExit) {
                client.callRemoteClientFunction(mainRendererContext, 'showStopAccModal');
              }
            } else {
              client
                .callRemoteClientFunction(
                  mainRendererContext,
                  'TrackEvent',
                  'mianpage',
                  'tray_menu_click',
                  '',
                  0,
                  0,
                  0,
                  0,
                  { clickid: 'exit' }
                )
                .catch();
              global.__fromExitBlock = true;
              mainWindow.close();
              // app.quit();
            }
          }
        }
      ];
      if (mainWindow.isVisible() && !mainWindow.isMinimized()) {
        contextMenuTemplate.splice(0, 0, {
          label: '隐藏主界面',
          type: 'normal',
          icon: `${__rootDir}/static/icon/tray/main-disvisible.png`,
          click(): void {
            client
              .callRemoteClientFunction(
                mainRendererContext,
                'TrackEvent',
                'mianpage',
                'tray_menu_click',
                '',
                0,
                0,
                0,
                0,
                { clickid: 'hide' }
              )
              .catch();
            mainWindow.minimize();
            mainWindow.hide();
          }
        });
      } else {
        contextMenuTemplate.splice(0, 0, {
          label: '显示主界面',
          type: 'normal',
          icon: `${__rootDir}/static/icon/tray/main-visible.png`,
          click(): void {
            client
              .callRemoteClientFunction(
                mainRendererContext,
                'TrackEvent',
                'mianpage',
                'tray_menu_click',
                '',
                0,
                0,
                0,
                0,
                { clickid: 'show' }
              )
              .catch();
            if (mainWindow.isMinimized()) {
              mainWindow.restore();
            }
            mainWindow.show();
          }
        });
      }

      client
        .callRemoteClientFunction(
          mainRendererContext,
          'TrackEvent',
          'mianpage',
          'tray_menu_show',
          '',
          0,
          0,
          0,
          0
        )
        .catch();
      const contextMenu: Menu = Menu.buildFromTemplate(contextMenuTemplate);
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      appTray.popUpContextMenu(contextMenu);
    });
  }

  return appTray;
}
