// zmodem.js 0.1.10 无自带类型声明；dist 为 webpack bundle（副作用模块，
// 挂 window.Zmodem）。门面 zmodem.ts 内以 any 消费，本垫片只为 tsc 放行。
declare module "zmodem.js/dist/zmodem.js";
