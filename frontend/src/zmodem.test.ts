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

  it("完整上传回路：会话结束 busy 解除，字节完整送达对端", async () => {
    // send_files 内部用 FileReader（node 无）：垫 Chrome 式「一次 partial 返回
    // 全量」——数据经 onprogress 发送、onload 收尾（zmodem_browser.js 语义）。
    class FakeFileReader {
      onerror: unknown;
      onprogress: ((e: { target: { result: ArrayBuffer } }) => void) | null = null;
      onload: ((e: { target: { result: ArrayBuffer } }) => void) | null = null;
      readAsArrayBuffer(f: Blob): void {
        void f.arrayBuffer().then((ab) => {
          this.onprogress?.({ target: { result: ab } });
          this.onload?.({ target: { result: ab } });
        });
      }
    }
    (globalThis as any).FileReader = FakeFileReader;

    const content = [0x61, 0x62, 0x00, 0xff, 0x0a];
    const received: number[] = [];
    let peer: any = null;
    const h = {
      writeTerm: vi.fn(),
      sendPty: vi.fn((b: Uint8Array) => {
        peer?.consume(Array.from(b)); // 我方协议输出 → 对端 rz 会话
      }),
      onState: vi.fn(),
      pickFiles: vi.fn(async () => null),
    };
    const ctl = createZmodem(h);

    // 拖拽路径：预存文件（rz\r 注入时对端未建，仅丢弃——真实场景进 PTY）
    const file = new File([new Uint8Array(content)], "up.txt");
    ctl.sendFiles([file]);

    // 对端（模拟远端 rz）：裸 Receive 会话，start() 即发 ZRINIT
    peer = new Zmodem.Session.Receive();
    peer.set_sender((octets: number[]) => ctl.consume(Uint8Array.from(octets)));
    peer.on("offer", (offer: any) => {
      offer.on("input", (octets: number[]) => {
        received.push(...octets);
      });
      offer.accept();
    });
    peer.start(); // ZRINIT → 我方检测出 send 会话 → 发送预存文件

    await vi.waitFor(() => expect(received).toEqual(content)); // 文件完整送达
    // 关键回归断言：会话结束后门禁解除（不需要 Ctrl+C）
    await vi.waitFor(() => expect(ctl.isBusy()).toBe(false));
    expect(h.onState).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "done" }));
  });

  it("完整接收回路：accept 决义值即载荷 → saveFile 收到文件字节", async () => {
    const saved: { name: string; payloads: Uint8Array[] }[] = [];
    let peerSession: any = null;
    let peerSentry: any = null;
    const h = {
      writeTerm: vi.fn(),
      sendPty: vi.fn((b: Uint8Array) => {
        peerSentry?.consume(Uint8Array.from(b)); // 我方协议响应 → 对端
      }),
      onState: vi.fn(),
      pickFiles: vi.fn(async () => null),
      saveFile: (name: string, payloads: Uint8Array[]) => saved.push({ name, payloads }),
    };
    const ctl = createZmodem(h);

    // 对端（模拟远端 sz）也是一台 Sentry：见我方 ZRINIT 应答 → 确认出 Send 会话
    peerSentry = new Zmodem.Sentry({
      to_terminal: () => {},
      on_detect: (d: any) => {
        peerSession = d.confirm();
      },
      on_retract: () => {},
      sender: (octets: number[]) => ctl.consume(Uint8Array.from(octets)),
    });

    // 开局：对端先发 ZRQINIT（真 lsz 的第一步）→ 我方检测出接收会话并回 ZRINIT，
    // 该应答经 sendPty 喂对端 Sentry 建立其 Send 会话
    ctl.consume(Uint8Array.from(Zmodem.Header.build("ZRQINIT").to_hex()));
    await vi.waitFor(() => expect(peerSession).not.toBeNull());

    // 驱动完整传输：offer → 数据 → 结束 → 关会话
    const content = [0x41, 0x42, 0x43, 0x0d, 0x0a, 0xff, 0x00];
    const xfer = await peerSession.send_offer({
      name: "loop.txt",
      size: content.length,
      mtime: new Date(0),
      files_remaining: 1,
      bytes_remaining: content.length,
    });
    xfer.send(content.slice());
    await xfer.end([]);
    await peerSession.close();

    expect(saved).toHaveLength(1);
    expect(saved[0]!.name).toBe("loop.txt");
    const got = (saved[0]!.payloads as unknown as Uint8Array[]).reduce<number[]>(
      (acc, p) => [...acc, ...Array.from(p)],
      []
    );
    expect(got).toEqual(content);
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
