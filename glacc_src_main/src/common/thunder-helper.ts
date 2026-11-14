import * as path from 'path';
import requireNodeFile from '@/common/require-node-file';
import { SetWindowType, WindowStyle, VirtualKeyCode, OptionOfHWNDInAfter } from '@/common/window-define';
// 修改工作路径，执行在最前，避免后续文件加载c++ node插件找不到文件（比如cmd，直接启动）
// 本来想把这段代码防到index.ts的最前面，但是因为tiny-logger（依赖thunder-helper.ts）打包到vendors
// 先加载vendors，再执行index.ts的代码。
// 加载到thunder - helper.ts会提示找不到文件。所以在此处设置工作路径
if (process.type === 'browser') {
  // 在主进程内修改工作路径
  const workingDirectory = path.resolve(process.execPath, '..').replace(/\\/g, '/');
  process.chdir(workingDirectory);
}
const thunderHelper: any = requireNodeFile(path.join(__rootDir, '../bin/ThunderHelper.node'));

export namespace ThunderHelper {
  export enum RegistryHKey {
    HKEY_CLASSES_ROOT = 'HKEY_CLASSES_ROOT',
    HKEY_CURRENT_USER = 'HKEY_CURRENT_USER',
    HKEY_LOCAL_MACHINE = 'HKEY_LOCAL_MACHINE',
    HKEY_USERS = 'HKEY_USERS'
  }

  export enum RegistryDataType {
    REG_NONE = 0,
    REG_SZ = 1,
    REG_EXPAND_SZ = 2,
    REG_BINARY = 3,
    REG_DWORD = 4,
    REG_DWORD_LITTLE_ENDIAN = 4,
    REG_DWORD_BIG_ENDIAN = 5,
    REG_LINK = 6,
    REG_MULTI_SZ = 7,
    REG_RESOURCE_LIST = 8,
    REG_FULL_RESOURCE_DESCRIPTOR = 9,
    REG_RESOURCE_REQUIREMENTS_LIST = 10,
    REG_QWORD = 11,
    REG_QWORD_LITTLE_ENDIAN = 11
  }

  export function readINI(filename: string, section: string, key: string): string {
    return thunderHelper.readINI(filename, section, key);
  }

  export function enableETWLogger(enbale: boolean): void {
    thunderHelper.enableETWLogger(enbale);
  }

  export function printEtwLog(logLevel: number, message: string): void {
    thunderHelper.printEtwLog(logLevel, message);
  }

  export function disableWindowAnimation(hwndHandle: number): void {
    thunderHelper.disableWindowAnimation(hwndHandle);
  }

  export function findChildWindow(hwndHandle: number, windowClassName: string): number {
    return thunderHelper.findChildWindow(hwndHandle, windowClassName);
  }

  export function setWindowLong(hwndHandle: number, windowType: SetWindowType, windowStyle: WindowStyle): void {
    thunderHelper.setWindowLong(hwndHandle, windowType, windowStyle);
  }

  export async function sleep(time: number): Promise<void> {
    await new Promise<void>((res: any, rej: any): void => {
      setTimeout(res, time);
    });
  }

  export function getTickCount(): number {
    return thunderHelper.getTickCount();
  }

  export function getInstallChannel(): string {
    return thunderHelper.getInstallChannel();
  }

  export function getFileVersion(filePath: string): string {
    return thunderHelper.getFileVersion(filePath);
  }

  // 返回short型数据，要查询的键被按下，返回值最高位被置1
  export function getKeyState(code: VirtualKeyCode): number {
    return thunderHelper.getKeyState(code);
  }

  export function setWindowPos(
    hwnd: number,
    options: OptionOfHWNDInAfter,
    x: number,
    y: number,
    width: number,
    height: number,
    flag: number
  ): void {
    return thunderHelper.setWindowPos(hwnd, options, x, y, width, height, flag);
  }

  export function getWindowRect(hwnd: number): any {
    if (!hwnd) {
      return { x: 0, y: 0, width: 0, height: 0 };
    }
    return thunderHelper.getWindowRect(hwnd);
  }

  export function getPeerID(): string {
    return thunderHelper.getPeerID();
  }

  export function shellExecute(
    hwnd: number,
    operation: string,
    filePath: string,
    params: string,
    workDir: string,
    showCmd: string
  ): number {
    return thunderHelper.shellExecute(hwnd, operation, filePath, params, workDir, showCmd);
  }

  export function createRegKey(hkey: RegistryHKey, subkey: string): number {
    return thunderHelper.createRegKey(hkey, subkey);
  }

  export function deleteRegKey(hkey: RegistryHKey, subkey: string): number {
    return thunderHelper.deleteRegKey(hkey, subkey);
  }

  export function readRegString(hkey: RegistryHKey, subkey: string, value: string): number | string {
    return thunderHelper.readRegString(hkey, subkey, value);
  }

  export function queryRegValue(hkey: RegistryHKey, subkey: string, value: string): number | string {
    return thunderHelper.queryRegValue(hkey, subkey, value);
  }

  export function writeRegValue(
    hkey: RegistryHKey,
    subkey: string,
    value: string,
    data: string,
    dataType: RegistryDataType
  ): number {
    return thunderHelper.writeRegValue(hkey, subkey, value, data, dataType);
  }

  export function deleteRegValue(hkey: RegistryHKey, subkey: string, value: string): number {
    return thunderHelper.deleteRegValue(hkey, subkey, value);
  }

  export function handleAutoRun(preValue: boolean, newValue: boolean): void {
    console.log('handleAutoRun preValue:', preValue, '; newValue:', newValue);
    // logger.verbose('handleAutoRun preValue:', preValue, '; newValue:', newValue);
    if (newValue) {
      const thunderPath: string = path.join(__rootDir, `../../${__packageName}.exe`);
      const param: string = thunderPath + ' -silent -StartType:AutoRun';
      writeRegValue(
        RegistryHKey.HKEY_LOCAL_MACHINE,
        'Software\\Microsoft\\Windows\\CurrentVersion\\Run',
        __packageName,
        param,
        RegistryDataType.REG_SZ
      );
    } else {
      deleteRegValue(RegistryHKey.HKEY_LOCAL_MACHINE, 'Software\\Microsoft\\Windows\\CurrentVersion\\Run', __packageName);
    }
  }

  export function getChannelID (): string {
    return thunderHelper.getChannelID();
  }
}
