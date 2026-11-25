// zmodem.test.ts 门面测试：真实 zmodem.js 状态机驱动（dist bundle 在
// node 可加载，但须先垫 window=globalThis 再动态 import——静态 import
// 被提升到赋值前会让 bundle 在 node 崩，浏览器无此问题）。
import { beforeAll, describe, expect, it, vi } from "vitest";

let Zmodem: any;
let createZmodem: (typeof import("./zmodem"))["createZmodem"];
let fmtXfer: (typeof import("./zmodem"))["fmtXfer"];

beforeAll(async () => {
  (globalThis as any).window = globalThis; // dist bundle 挂 window.Zmodem
  const m = await import("./zmodem");
  createZmodem = m.createZmodem;
  fmtXfer = m.fmtXfer;
  Zmodem = (globalThis as any).Zmodem;
});

const hooks = () => ({
  writeTerm: vi.fn(),
  sendPty: vi.fn(),
  onState: vi.fn(),
  pickFiles: vi.fn(async () => null as File[] | null),
});

describe("createZmodem", () => {
  it("普通字节透传到终端，无状态变化", () => {
    const h = hooks();
    const ctl = createZmodem(h);
    ctl.consume(new Uint8Array([0x61, 0x62, 0x63]));
    expect(h.writeTerm).toHaveBeenCalledWith(new Uint8Array([0x61, 0x62, 0x63]));
    expect(h.onState).not.toHaveBeenCalled();
    expect(ctl.isBusy()).toBe(false);
  });

  it("ZRQINIT（远端 sz）→ 接收态；取消后复位", () => {
    const h = hooks();
    const ctl = createZmodem(h);
    ctl.consume(Uint8Array.from(Zmodem.Header.build("ZRQINIT").to_hex()));
    expect(h.onState).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "receiving" }));
    expect(ctl.isBusy()).toBe(true);
    ctl.cancel();
    expect(h.onState).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "error" }));
    expect(ctl.isBusy()).toBe(false);
  });

  it("ZRINIT（远端 rz）→ 弹文件选择器；空选择则中止不传输", async () => {
    const h = hooks();
    const ctl = createZmodem(h);
    ctl.consume(Uint8Array.from(Zmodem.Header.build("ZRINIT", ["CANFDX", "CANOVIO"], 0, 0).to_hex()));
    expect(h.pickFiles).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => expect(ctl.isBusy()).toBe(false));
    expect(h.onState).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "idle" }));
  });

  it("sendFiles 注入 rz 命令字节", () => {
    const h = hooks();
    const ctl = createZmodem(h);
    ctl.sendFiles([{ name: "a.txt", size: 3 } as unknown as File]);
    expect(h.sendPty).toHaveBeenCalledWith(new TextEncoder().encode("rz\r"));
  });
});

describe("fmtXfer", () => {
  const base = { phase: "sending", name: "f", startedAt: 1000, message: "" } as const;

  it("零总量无百分比；idle 无速率", () => {
    expect(fmtXfer({ ...base, phase: "idle", bytes: 0, total: 0 }, 2000)).toEqual({ pct: "", rate: "" });
  });

  it("百分比与速率（含 KB 换算）", () => {
    expect(fmtXfer({ ...base, bytes: 50, total: 200 }, 3000)).toEqual({ pct: "25%", rate: "25 B/s" });
    // startedAt=1000（base），nowMs=2000 → elapsed 1s → 20480 B/s
    expect(fmtXfer({ ...base, phase: "receiving", bytes: 20480, total: 204800 }, 2000)).toEqual({
      pct: "10%",
      rate: "20.0 KB/s",
    });
  });

  it("百分比封顶 100", () => {
    expect(fmtXfer({ ...base, bytes: 300, total: 200 }, 1000).pct).toBe("100%");
  });
});
