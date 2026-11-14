import * as path from 'path';
import requireNodeFile from '@/common/require-node-file';
// TODO thunderhelper起来的时候，会需要zlib1.dll,目前发现有些用户系统目录有这个低版本的dll，所以被加载了低版本的zlib1.dll，导致了崩溃
// 这里再AccHelper里面主动从加速器自己的目录加载，所以要保证AccHelper在thunderhelper之前加载
const thunderHelper: any = requireNodeFile(path.join(__rootDir, '../bin/AccHelper.node'));
import { inspect } from 'util';
import { ThunderChannelList } from '@/common/channel';
import TinyLogger, { LogLevel, IOutputLogger, outputLoggerConsole } from '@xunlei/tiny-logger';
import { DevEnvHelperNS } from './dev-env-helper';
import { ipcMain, ipcRenderer, BrowserWindow } from 'electron';
import { ThunderHelper } from '@/common/thunder-helper';
process.env.TL_ENABLE = process.env.NODE_ENV === 'production' ? '0' : '1';
TinyLogger.enableLogger = process.env.TL_ENABLE === '1';

function setOutputLogger(): void {
  if (process.env.TL_OUTPUT === 'console') {
    TinyLogger.outputLogger = outputLoggerConsole;
  } else {
    TinyLogger.outputLogger = getETWOutputLogger();
  }
}

function updateLogOutputEnv(): void {
  let enable: boolean = DevEnvHelperNS.isLogEnable();
  if ((process.env.TL_ENABLE === '1') !== enable) {
    process.env.TL_ENABLE = enable ? '1' : '0';
    TinyLogger.enableLogger = enable;
    ThunderHelper.enableETWLogger(enable);
  }
  let output: string = DevEnvHelperNS.getLogOutput();
  if (output && output !== process.env.TL_OUTPUT) {
    process.env.TL_OUTPUT = output;
    setOutputLogger();
  }
}

function initLogOutputEnv(): void {
  setOutputLogger();
  updateLogOutputEnv();
}
initLogOutputEnv();

function getETWOutputLogger(): IOutputLogger {
  function stringify(...messages: any[]): string {
    return messages
      .map((message: any): string => inspect(message))
      .join(' ')
      .replace(/%/g, '%%');
  }

  function transformToEtwLog(logLevel: LogLevel): (...messages: any[]) => void {
    return function (...messages: any[]): void {
      ThunderHelper.printEtwLog(logLevel, stringify(...messages));
    };
  }

  return {
    [LogLevel.Critical]: transformToEtwLog(LogLevel.Critical),
    [LogLevel.Error]: transformToEtwLog(LogLevel.Error),
    [LogLevel.Warning]: transformToEtwLog(LogLevel.Warning),
    [LogLevel.Information]: transformToEtwLog(LogLevel.Information),
    [LogLevel.Verbose]: transformToEtwLog(LogLevel.Verbose)
  };
}

if (process.type === 'browser') {
  ipcMain.on(ThunderChannelList.channelRMUpdateLogEnviroment, () => {
    let browserWindows: BrowserWindow[] = BrowserWindow.getAllWindows();
    browserWindows.forEach((browserWindow: BrowserWindow) => {
      if (!browserWindow.isDestroyed()) {
        browserWindow.webContents.send(ThunderChannelList.channelMRUpdateLogEnviroment);
      }
    });
    updateLogOutputEnv();
  });
} else if (process.type === 'renderer') {
  ipcRenderer.on(ThunderChannelList.channelMRUpdateLogEnviroment, () => {
    updateLogOutputEnv();
  });
}
