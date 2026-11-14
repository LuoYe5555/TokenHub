export namespace MessageBoxNS {
  export enum Action {
    OK,
    Cancel,
    Close,
    Unknow // 从右键系统菜单点击关闭
  }

  export interface IResolvePayload {
    action: Action;
    [prop: string]: any;
  }
}
