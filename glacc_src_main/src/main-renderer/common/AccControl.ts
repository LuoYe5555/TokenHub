export interface AccDetail {
  proxy_latency: number; // 延迟 ms
  proxy_latency_show: number; // 延迟 ms（界面上显示这个）
  direct_latency: number; //
  times: number; // 加速时常
  loss_rate: number; // 丢包率
  best_proxy_latency?: number; // 当前区服所有线路中延迟最低的线路的延时
  region_name?: string; // 地区名称
}

// 这个顺序是和service返回的一致，可以参考cppsrc\AccService\callback.h
export enum GameAccStatus {
  Stopped = 0,
  Starting,
  Started,
  Error,
  Stopping
}

export interface AccStatusInfo {
  status: GameAccStatus;
  progress: number;
  error_code: number;
  error_message: string;
}

export interface AccControlEventHandler {
  OnAccStatusInfo(info: AccStatusInfo): void;
  // 加速详情变化
  OnDetailChange(detail: AccDetail): void;
  // 加速器奔溃
  OnAccServiceCrash(code: number, signal: string): void;
}

export interface AccControl {
  Init(): Promise<boolean>;
  //	{
  //	  "username": "",
  //	  "password": "",
  //	  "game_id": "",
  //	  "game_area": "",
  //	  "speed_id": ""
  //	}
  // info为上面字段格式化后传递过去
  StartAcc(info: string): Promise<boolean>;
  StopAcc(): Promise<boolean>;
}
