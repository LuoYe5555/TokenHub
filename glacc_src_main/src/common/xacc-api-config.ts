export const XACC_API_ORIGIN_PRODUCT = 'https://api-xacc.xunlei.com';
export const XACC_API_ORIGIN_TEST = 'https://game-test-xacc.xunlei.com';

export const XACC_API_ORIGIN = process.env.XACC_API_ORIGIN === 'Test' ? XACC_API_ORIGIN_TEST : XACC_API_ORIGIN_PRODUCT;

export const XACC_API_GAME_BASE_V1_URL = XACC_API_ORIGIN + '/xlppc.game.acc/game/v1/';
export const XACC_API_GAME_BASE_V2_URL = XACC_API_ORIGIN + '/xlppc.game.acc/game/v2/';

export const XACC_API_STAT_ES_REPORT_URL = XACC_API_ORIGIN + '/xlppc.gacs/api/gxsdn/report/data';
export const XACC_API_UPGRADE_CHECK_URL = XACC_API_ORIGIN + '/xlppc.game.acc/game/v1/upgrade/check';
export const XACC_API_QUERY_CLIENT_UPDATE_URL =
  'http://api-xacc.xunlei.com' + '/xlppc.game.acc/game/v1/query_client_update';
export const XACC_API_SUBMIT_WORK_ORDER_URL = XACC_API_ORIGIN + '/xlppc.gacs/api/gxsdn/work/order';
export const XACC_API_CONFIG_LIST_URL = XACC_API_ORIGIN + '/xlppc.gacs/api/gxsdn/config/list';
export const XACC_API_CONFIG_ERROR_CODES_URL = XACC_API_ORIGIN + '/xlppc.gacs/api/gxsdn/config/errCodeList';
export const XACC_API_GAME_TOOL_LIST_URL = XACC_API_ORIGIN + '/xlppc.game.acc/game/v1/gameToolList';
export const XACC_API_GET_GAME_ACCOUNT = XACC_API_ORIGIN + '/xlppc.gacs/api/gxsdn/game/getGamePlayAccount';
export const XACC_API_GET_USER_REGISTER_TIME = XACC_API_ORIGIN + '/xlppc.backends/user_info/get_user_info';
export const XACC_API_SUBMIT_WORK_CREATE_URL = XACC_API_ORIGIN + '/xlppc.gacs/api/gxsdn/work/create';

export const XBASE_ORIGIN =
  process.env.XACC_API_ORIGIN === 'Test'
    ? ['https://dev-xluser-ssl.xunlei.com']
    : ['https://xluser-ssl.xunlei.com', 'https://xluser2-ssl.xunlei.com', 'https://xluser3-ssl.xunlei.com'];

export const SL_API_ORIGIN =
  process.env.XACC_API_ORIGIN === 'Test'
    ? 'https://test.api-shoulei-ssl.xunlei.com'
    : 'https://api-shoulei-ssl.xunlei.com';
