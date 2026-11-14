import * as path from 'path';
import { ThunderHelper } from '@/common/thunder-helper';

export namespace DevEnvHelperNS {
  export function isDevToolsEnable(): boolean {
    return isLogEnable();
  }

  export function isLogEnable(): boolean {
    let ret: boolean = true;
    do {
      if (process.env.NODE_ENV !== 'production') {
        break;
      }
      let iniPath: string = path.resolve('C:\\ETW_LOG\\log.ini');
      let enableLog: string = ThunderHelper.readINI(iniPath, 'Log', 'enable');
      ret = enableLog === '1';
    } while (false);
    return ret;
  }

  export function getLogOutput(): string {
    let ret: string = process.env.TL_OUTPUT;
    do {
      if (ret && ret !== '') {
        // 优先用环境变量的
        break;
      }
      let iniPath: string = path.resolve('C:\\ETW_LOG\\log.ini');
      ret = ThunderHelper.readINI(iniPath, 'Log', 'output');
    } while (false);
    return ret;
  }

  export function getADUrl(): string {
    let url = '';
    do {
      if (process.env.NODE_ENV !== 'production') {
        break;
      }
      let iniPath: string = path.resolve('C:\\ETW_LOG\\log.ini');
      url = ThunderHelper.readINI(iniPath, 'AD', 'url');
    } while (false);
    return url;
  }

  export function getBannerUrl(): string {
    let url = '';
    do {
      if (process.env.NODE_ENV !== 'production') {
        break;
      }
      let iniPath: string = path.resolve('C:\\ETW_LOG\\log.ini');
      url = ThunderHelper.readINI(iniPath, 'AD', 'banner');
    } while (false);
    return url;
  }

  export function getADVip(): boolean {
    let ret = false;
    do {
      if (process.env.NODE_ENV !== 'production') {
        break;
      }
      let iniPath: string = path.resolve('C:\\ETW_LOG\\log.ini');
      let vip = ThunderHelper.readINI(iniPath, 'AD', 'vip');
      ret = vip === '1'
    } while (false);
    return ret;
  }
}
