// 是否是测试环境
export const isTest = process.env.XACC_API_ORIGIN === 'Test';
// 业务名
export const productName = '给梨加速器';
// 包名
export const packageName = 'glacc';
// xbase配置
export const appId = '22081';
export const appKey = isTest ? '89906a7cf526f4b5d3ee48dc6cfe532f' : 'd76047ea36c329665954c48729ce7de1';
export const clientId = 'aF42mN5OP0hUa0nK';
export const clientSecret = '4TgNf1MdEfC0KeZoFAJ3vQ';
export const noHostAppName = 'com.xunlei.glacc';
export const xbaseApiOrigin = isTest ? ['https://34qhwely564uz2boqrd.xbase.xyz'] : ['https://user.geilijiasu.net'];
export const projectID = isTest ? '34qhwely564uz2boqrd' : '34qii0d5qwne9r08bt4';
//  用户协议和隐私协议
export const agreementUserUrl = 'https://www.geilijiasu.com/agreement-user.html';
export const privacyUrl = 'https://www.geilijiasu.com/privacy.html';
// 哈勃配置
export const hubAppId = 30699;
export const hubAppkey = 'a24236e6a8ac79410d5ae1126b68c59e';
// 显示币种名
export const currencyName = '时长';
// 官网网页
export const webUrl = isTest ? 'https://test.geilijiasu.net' : 'https://www.geilijiasu.com';
// 币种单位
export const currencyUnit = '个';
