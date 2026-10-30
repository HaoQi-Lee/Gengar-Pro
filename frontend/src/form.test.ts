// form.test.ts 表单纯函数测试。显式 import vitest API（不依赖全局类型，
// tsc 构建期也能过类型检查）。
import { describe, expect, it } from "vitest";
import { parseTags } from "./form";

describe("parseTags", () => {
  it("英文逗号分隔并去空白", () => {
    expect(parseTags("web, prod , db")).toEqual(["web", "prod", "db"]);
  });
  it("中文逗号同样分隔", () => {
    expect(parseTags("web，prod")).toEqual(["web", "prod"]);
  });
  it("去重保序", () => {
    expect(parseTags("web,prod,web")).toEqual(["web", "prod"]);
  });
  it("去空白后去重", () => {
    expect(parseTags("web ,　web")).toEqual(["web"]);
  });
  it("空串与纯分隔符返回空数组", () => {
    expect(parseTags("")).toEqual([]);
    expect(parseTags(" , ， ")).toEqual([]);
  });
});
