import { app } from 'electron';
import path = require('path');
global.__rootDir = app.getAppPath();
global.__profilesDir = path.join(__rootDir, '../../../profiles');
global.__xdasIpcPathNew = true;
global.__game_area__ = 'outland';
global.__fromExitBlock = false;
global.__exit_block = 'Exit_Default';

if (process.env.GUANGSHAN_ENV === 'development') {
  global.isDevelopment = true;
} else if (process.env.GUANGSHAN_ENV === 'test') {
  global.isTest = true;
} else {
  global.isProduction = true;
}
