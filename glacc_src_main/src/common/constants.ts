import * as path from 'path';

global.__rootDir = path.resolve(process.execPath, '../resources/app').replace(/\\/g, '/');
global.__profilesDir = path.resolve(global.__rootDir, '../../../profiles').replace(/\\/g, '/');
global.__xdasIpcPathNew = true;
global.__game_area__ = 'outland';
// const isAsar: string = process.env.IS_ASAR;
// @ts-ignore
global.__outDir = `${__rootDir}/out.asar`;

if (process.env.GUANGSHAN_ENV === 'development') {
  global.isDevelopment = true;
} else if (process.env.GUANGSHAN_ENV === 'test') {
  global.isTest = true;
} else {
  global.isProduction = true;
}
