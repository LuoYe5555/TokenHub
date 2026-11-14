// =================================================================
// @description: 渲染进程与主进程发送消息的channel定义集合
//               渲染进程与主进程通信命名：channle{$RM_MODULENAME_ChANNELID} = '{$RM_MODULENAME_ChANNELID}'
//               主进程与渲染进程通信命名：channel{$MR_MODULENAME_ChANNELID} = '{$MR_MODULENAME_ChANNELID}'
//               特别注意：后续如果业务改动导致不再需要某个消息id，请注意第一时间从本文件中剔除
// @author:      maolanghua
// @date:        2018.03.23
// =================================================================
export namespace ThunderChannelList {
  // 主渲染进程发送给主进程更新日志环境
  export const channelRMUpdateLogEnviroment = 'RM_UPDATE_LOG_ENVIRONMENT';
  // 主进程发送给主渲染进程更新日志环境
  export const channelMRUpdateLogEnviroment = 'MR_UPDATE_LOG_ENVIRONMENT';

  // 渲染进程发送消息给主进程，监控渲染进程异常退出时主进程也退出
  export const channelRMProcessSend = 'RM_RENDER_PROCESS_INFO';

  // 渲染进程发送文件操作消息给主进程
  export const channelRMFileCopy = 'RM_FILE_COPY';
  export const channelRMFileMove = 'RM_FILE_MOVE';
  // 主进程向渲染进程发送文件操作结果的消息
  export const channelMRFileCopyResult = 'MR_FILE_COPY_RESULT';
  export const channelMRFileMoveResult = 'MR_FILE_MOVE_RESULT';

  // 渲染进程发消息给主进程，把窗口拉到最前，bringWindowToTop只能在主进程调用
  export const channelMRBringWindowToTop = 'MR_RENDER_BRING_WINDOW_TO_TOP';

  // 渲染进程发消息给主进程获取主渲染进行webContentId
  export const channelGetMRWinWebContentId = 'GET_MAIN_RENDER_WIN_WEBCONTENTS_ID';
  // 渲染进程发消息给主进程获取主进程xlstat是否初始化成功
  export const channelGetBrowserIsStatInitOk = 'GET_BROWSER_IS_STAT_INIT_OK';
  // 渲染进程发消息给主进程启动升级
  export const channelStartUpgrade = 'START_UPGRADE';
  // 渲染进程发消息给主进程检查升级
  export const channelCheckUpgrade = 'CHECK_UPGRADE';
  export const channelMRLocalGameReady = 'MR_LOCAL_GAME_READY';
  // 置顶主渲染窗口
  export const channelMoveTopMRWin = 'MOVE_TOP_MR_WIN';
  // 隐藏主渲染窗口
  export const channelHideMRWin = 'HIDE_MR_WIN';
  // 关闭主渲染窗口
  export const channelCloseMRWin = 'CLOSE_MR_WIN';
  // 最小化主渲染窗口
  export const channelMinizeMRWin = 'MINIZE_MR_WIN';
  // 获取屏幕信息
  export const channelGetScreenInfo = 'GET_SCREEN_INFO';
  // 创建窗口
  export const channelWindowCreate = 'window:create';
  // 移动窗口
  export const channelWindowMove = 'window:move';
  // 置顶窗口
  export const channelWindowMoveTop = 'window:movetop';
  // 关闭窗口
  export const channelWindowClose = 'window:close';
  // 插件最小化
  export const channelWindowMinimize = 'window:minimize';
  // 卸载扩展
  export const channelExtensionUnload = 'extension:unload';
  // 日志模块
  export const channeElog = 'Elog';
  export const channeSetLogLevel = 'SetLogLevel';
  // 标记记录开始阶段耗时
  export const channeMarkPhaseStart = 'markPhaseStart';
  // 标记记录结束阶段耗时
  export const channeMarkPhaseEnd = 'markPhaseEnd';
  // 获取阶段耗时记录
  export const channeGetRecordMarkPhase = 'getRecordMarkPhase';
  // 获取APP内存占用情况
  export const channeGetTotalMemory = 'getTotalMemory';
  // 获取启动协议参数信息
  export const channelGetAssistStartupData = 'GET_ASSIST_STARTUP_DATA';
  // 主渲染进程已注册 scheme 协议监听
  export const channelMRRendererReady = 'MR_RENDERER_READY';
  // 主进程向主渲染进程发送 scheme 协议行为
  export const channelMRSchemeAction = 'MR_SCHEME_ACTION';
}
