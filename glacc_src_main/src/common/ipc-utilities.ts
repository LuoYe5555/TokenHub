import { client as clientModule } from '@xunlei/node-net-ipc/dist/ipc-client';
export const messageboxRendererContextPrefix = 'message-box-renderer';
export const captchaRendererContext = 'captcha-dlg-renderer';
export const embededViewBoxRenderContext = 'embeded-view-box-render';

export async function callServerFunction<T>(funcName: string, ...args: any[]): Promise<T> {
  const resultArr = await clientModule.callServerFunctionEx(funcName, ...args);
  if (Array.isArray(resultArr)) {
    if (resultArr[1]) {
      return Promise.reject(resultArr[1]);
    } else {
      return Promise.resolve(resultArr[0]);
    }
  } else {
    return Promise.reject();
  }
}

export async function callClientFunction<T>(remoteId: string, funcName: string, ...args: any[]): Promise<T> {
  const resultArr = await clientModule.callRemoteClientFunction(remoteId, funcName, ...args);
  if (Array.isArray(resultArr)) {
    if (resultArr[1]) {
      return Promise.reject(resultArr[1]);
    } else {
      return Promise.resolve(resultArr[0]);
    }
  } else {
    return Promise.reject();
  }
}
