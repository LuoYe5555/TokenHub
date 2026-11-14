export enum NavViewID {
  MyGame = 'myGame', // 我的游戏
  AllGame = 'allGame', // 所有游戏
  Free = 'free',
  TaskCenter = 'taskCenter', // 任务中心
  Home = 'home', // 首页
  Mod = 'mod', // 首页
  P2P = 'p2p' // 组网联机
}

/**
 * 因为要在左侧显示未读数, 所以navs需要承担原先community的显示未读数功能
 */
export interface INav {
  key: NavViewID;
  content: string;
  isDefault?: boolean; // 是否默认的导航
  curPage?: ECurrentPage;
  icon?: string;
}

export enum ECurrentPage {
  Other = '',
  DetailPage = 'DetailPage', // 游戏详情页
  MyGamePage = 'MyGamePage', // 我的游戏页
  TaskCenterPage = 'TaskCenterPage', // 任务中心页
  SearchResults = 'SearchResults', // 搜索结果
  HomePage = 'HomePage' // 首页
}
