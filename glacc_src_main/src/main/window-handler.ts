/**
 * @description: 主后台进程操作窗口的一些函数
 */

import { BrowserWindow, Point, Display, screen } from 'electron';
import { ThunderHelper } from '@/common/thunder-helper';
import { client as clientModule } from '@xunlei/node-net-ipc/dist/ipc-client';

let inited: boolean = false;
export namespace MainProcessWindowNS {
  export function centerWnd(selfWindow: BrowserWindow, parent: BrowserWindow): void {
    do {
      if (!selfWindow || !parent) {
        break;
      }

      let selfHwnd: number = selfWindow.getNativeWindowHandle().readUIntLE(0, 4);
      let parentHwnd: number = parent.getNativeWindowHandle().readUIntLE(0, 4);

      let parentRect: any = ThunderHelper.getWindowRect(parentHwnd);
      let pos: number[] = parent.getPosition();
      let selfSize: number[] = selfWindow.getSize();
      // 二级面板显示在父窗口左上角所在的屏幕
      let p1: Point = { x: pos[0], y: pos[1] };
      const disp: Display = screen.getDisplayNearestPoint(p1);

      // 转换成屏幕上真实要显示的大小
      selfSize[0] = selfSize[0] * disp.scaleFactor;
      selfSize[1] = selfSize[1] * disp.scaleFactor;

      const dpi: number = disp.scaleFactor;
      const screenWidth: number = disp.workArea.width * dpi;
      const screenHeight: number = disp.workArea.height * dpi;
      let p3 = screen.dipToScreenPoint({ x: disp.workArea.x, y: disp.workArea.y });

      // 父窗口左边超出了屏幕，那么只用右边部分进行居中
      if (parentRect.x < p3.x) {
        parentRect.width = parentRect.width - (p3.x - parentRect.x);
        parentRect.x = p3.x;
      }
      if (parentRect.y < p3.y) {
        parentRect.height = parentRect.height - (p3.y - parentRect.y);
        parentRect.y = p3.y;
      }

      // 计算父窗口在屏幕内的部分可显示的区域
      let visibleWidth: number = parentRect.width;
      let visibleHeight: number = parentRect.height;
      if (parentRect.x + visibleWidth > p3.x + screenWidth) {
        visibleWidth = p3.x + screenWidth - parentRect.x;
      }
      if (parentRect.y + visibleHeight > p3.y + screenHeight) {
        visibleHeight = p3.y + screenHeight - parentRect.y;
      }

      // 二级面板的预想位置
      let x: number = parentRect.x + (visibleWidth - selfSize[0]) / 2;
      let y: number = parentRect.y + (visibleHeight - selfSize[1]) / 2;

      // 矫正位置，二级面板不能超出屏幕,优先保证面板的左上角在屏幕内
      if (x < p3.x) {
        x = p3.x;
      } else if (x > p3.x + screenWidth - selfSize[0]) {
        x = p3.x + screenWidth - selfSize[0];
      }
      if (y < p3.y) {
        y = p3.y;
      } else if (y > p3.y + screenHeight - selfSize[1]) {
        y = p3.y + screenHeight - selfSize[1];
      }

      ThunderHelper.setWindowPos(selfHwnd, 0, x, y, 0, 0, 5);
    } while (0);
  }

  export function escapeCloseFlicker(win: BrowserWindow): void {
    do {
      if (!win) {
        break;
      }

      const parentWindow: BrowserWindow = win.getParentWindow();
      if (!parentWindow) {
        break;
      }

      win.once('close', () => {
        if (!parentWindow.isDestroyed()) {
          parentWindow.focus();
        }
      });
    } while (0);
  }

  export function registerFunctions(): void {
    if (!inited) {
      inited = true;

      clientModule.registerFunctions({
        SetPosition: (remoteId: string, id: number, parentId: number): void => {
          do {
            if (!id || !parentId) {
              break;
            }

            const selfWindow = BrowserWindow.fromId(id);
            const parent = BrowserWindow.fromId(id);
            if (!selfWindow || !parent) {
              break;
            }

            centerWnd(selfWindow, parent);
          } while (0);
        },
        FitWindowPosInParent: (remoteId: string, id: number): void => {
          do {
            if (!id) {
              break;
            }

            const selfWindow = BrowserWindow.fromId(id);
            if (!selfWindow) {
              break;
            }
            const parent = selfWindow.getParentWindow();
            if (!parent) {
              break;
            }

            centerWnd(selfWindow, parent);
          } while (0);
        },
        EscapeCloseFlicker: (remoteId: string, ...args: any[]): void => {
          do {
            const id: number = args[0];
            if (id === undefined || id === null) {
              break;
            }

            const win: BrowserWindow = BrowserWindow.fromId(id);
            escapeCloseFlicker(win);
          } while (0);
        }
      });
    }
  }
}
