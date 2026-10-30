// events.test.ts WS 传输层测试：注入 FakeWebSocket + 假时钟——事件分发与
// 序号去重、断档全量重排、hello 回退重连、心跳保活、终端发送排队与 ping。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connectTerm, subscribeChat } from "./events";

class FakeWS {
  static instances: FakeWS[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  url: string;
  readyState = FakeWS.CONNECTING;
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  sent: string[] = [];

  constructor(url: string) {
    this.url = url;
    FakeWS.instances.push(this);
  }
  send(s: string): void {
    this.sent.push(s);
  }
  close(): void {
    if (this.readyState === FakeWS.CLOSED) return;
    this.readyState = FakeWS.CLOSED;
    this.onclose?.();
  }
  // ---- 测试侧驱动 ----
  open(): void {
    this.readyState = FakeWS.OPEN;
    this.onopen?.();
  }
  deliver(f: unknown): void {
    this.onmessage?.({ data: JSON.stringify(f) });
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  FakeWS.instances = [];
  const g = globalThis as Record<string, unknown>;
  g.WebSocket = FakeWS;
  g.location = { protocol: "http:", host: "localhost" };
  // window 委托到全局定时器：假时钟接管后安装的心跳/退避一并可控
  g.window = {
    setInterval: (cb: () => void, ms?: number) => setInterval(cb, ms),
    clearInterval: (id?: number) => clearInterval(id),
  };
});

afterEach(() => {
  vi.useRealTimers();
  const g = globalThis as Record<string, unknown>;
  delete g.WebSocket;
  delete g.location;
  delete g.window;
});

const makeHandlers = () => ({
  onChat: vi.fn(),
  onConfirm: vi.fn(),
  onAsk: vi.fn(),
  onConfirmDone: vi.fn(),
  onAskDone: vi.fn(),
  onReset: vi.fn(),
  onHello: vi.fn(),
});

describe("subscribeChat", () => {
  it("事件分发到各 handler，序号回退/重复帧去重", () => {
    const h = makeHandlers();
    const unsub = subscribeChat("", h);
    const ws = FakeWS.instances[0]!;
    expect(ws.url).toBe("ws://localhost/api/ws/chat?since=0&session=");
    ws.open();
    ws.deliver({ t: "chat", seq: 2, ev: { type: "info", text: "a" } });
    ws.deliver({ t: "chat", seq: 2, ev: { type: "info", text: "dup" } }); // 重复
    ws.deliver({ t: "chat", seq: 1, ev: { type: "info", text: "old" } }); // 回退
    expect(h.onChat).toHaveBeenCalledTimes(1);
    expect(h.onChat).toHaveBeenCalledWith({ type: "info", text: "a" });
    ws.deliver({ t: "confirm", id: "c1", reqs: [] });
    expect(h.onConfirm).toHaveBeenCalledWith({ id: "c1", reqs: [] });
    ws.deliver({ t: "ask", id: "a1", input: "问题" });
    expect(h.onAsk).toHaveBeenCalledWith({ id: "a1", input: "问题" });
    ws.deliver({ t: "confirm_done", id: "c1" });
    expect(h.onConfirmDone).toHaveBeenCalledWith("c1");
    ws.deliver({ t: "ask_done", id: "a1" });
    expect(h.onAskDone).toHaveBeenCalledWith("a1");
    ws.deliver({ t: "reset" });
    expect(h.onReset).toHaveBeenCalledTimes(1);
    unsub();
  });

  it("reset 帧重置序号：其后低序号重放帧不被当重复丢弃", () => {
    const h = makeHandlers();
    const unsub = subscribeChat("", h);
    const ws = FakeWS.instances[0]!;
    ws.open();
    ws.deliver({ t: "chat", seq: 5, ev: { type: "info", text: "a" } });
    ws.deliver({ t: "reset", seq: 6 }); // 服务端 reset + 全量重放路径
    expect(h.onReset).toHaveBeenCalledTimes(1);
    ws.deliver({ t: "chat", seq: 1, ev: { type: "info", text: "replay" } });
    expect(h.onChat).toHaveBeenCalledTimes(2);
    expect(h.onChat).toHaveBeenLastCalledWith({ type: "info", text: "replay" });
    unsub();
  });

  it("序号断档：onReset 后断开并以 since=0 重连", () => {
    const h = makeHandlers();
    const unsub = subscribeChat("", h);
    const ws = FakeWS.instances[0]!;
    ws.open();
    ws.deliver({ t: "chat", seq: 1, ev: { type: "info" } });
    ws.deliver({ t: "chat", seq: 3, ev: { type: "info" } }); // 1→3 断档
    expect(h.onReset).toHaveBeenCalledTimes(1);
    expect(ws.readyState).toBe(FakeWS.CLOSED);
    vi.advanceTimersByTime(1000); // 退避 500→1000
    expect(FakeWS.instances).toHaveLength(2);
    expect(FakeWS.instances[1]!.url).toBe("ws://localhost/api/ws/chat?since=0&session=");
    unsub();
  });

  it("hello.next 回退（服务重启）：onReset 并重置序号", () => {
    const h = makeHandlers();
    const unsub = subscribeChat("", h);
    const ws = FakeWS.instances[0]!;
    ws.open();
    ws.deliver({ t: "chat", seq: 5, ev: { type: "info" } }); // 首帧序号任意可收
    ws.deliver({ t: "hello", next: 3 }); // 3 < 5：服务重启
    expect(h.onReset).toHaveBeenCalledTimes(1);
    ws.close(); // 模拟断线
    vi.advanceTimersByTime(1000);
    expect(FakeWS.instances[1]!.url).toBe("ws://localhost/api/ws/chat?since=0&session=");
    unsub();
  });

  it("hello.session 不符（首连分配/stale 复活）：onHello(rebound) 重绑，重连用新 id", () => {
    const h = makeHandlers();
    const unsub = subscribeChat("deadbeef", h);
    const ws = FakeWS.instances[0]!;
    ws.open();
    ws.deliver({ t: "hello", next: 0, session: "abc123", scope: "web1、db1" });
    expect(h.onHello).toHaveBeenCalledWith({ session: "abc123", scope: "web1、db1", rebound: true });
    ws.close(); // 模拟断线
    vi.advanceTimersByTime(1000);
    expect(FakeWS.instances[1]!.url).toBe("ws://localhost/api/ws/chat?since=0&session=abc123");
    unsub();
  });

  it("hello.session 一致（重连）：onHello(rebound=false) 通告目标，不触发迁移", () => {
    const h = makeHandlers();
    const unsub = subscribeChat("sid9", h);
    const ws = FakeWS.instances[0]!;
    ws.open();
    ws.deliver({ t: "hello", next: 5, session: "sid9", scope: "" });
    expect(h.onHello).toHaveBeenCalledWith({ session: "sid9", scope: "", rebound: false });
    unsub();
  });

  it("30s 心跳发空帧，断开后停发", () => {
    const h = makeHandlers();
    const unsub = subscribeChat("", h);
    const ws = FakeWS.instances[0]!;
    ws.open();
    vi.advanceTimersByTime(30_000);
    expect(ws.sent).toEqual(["{}"]);
    vi.advanceTimersByTime(30_000);
    expect(ws.sent).toEqual(["{}", "{}"]);
    unsub();
    vi.advanceTimersByTime(120_000);
    expect(ws.sent).toEqual(["{}", "{}"]);
  });

  it("unsubscribe 后断线不重连", () => {
    const h = makeHandlers();
    const unsub = subscribeChat("", h);
    FakeWS.instances[0]!.open();
    unsub();
    vi.advanceTimersByTime(60_000);
    expect(FakeWS.instances).toHaveLength(1);
  });
});

describe("connectTerm", () => {
  it("open 前发送排队，open 后按序冲刷；data/exit 分发", () => {
    const onData = vi.fn();
    const onExit = vi.fn();
    const conn = connectTerm("t1", { onData, onExit });
    const ws = FakeWS.instances[0]!;
    expect(ws.url).toBe("ws://localhost/api/ws/term?id=t1");
    conn.send("aGk=");
    conn.resize(80, 24);
    expect(ws.sent).toEqual([]); // 未 open：排队不丢
    ws.open();
    expect(ws.sent).toEqual([
      JSON.stringify({ t: "write", data: "aGk=" }),
      JSON.stringify({ t: "resize", cols: 80, rows: 24 }),
    ]);
    ws.deliver({ t: "data", data: "aGk=" });
    expect(onData).toHaveBeenCalledWith("aGk=");
    ws.deliver({ t: "exit" });
    expect(onExit).toHaveBeenCalledWith("");
    conn.close();
  });

  it("120s ping 保活，close 后停发", () => {
    const onData = vi.fn();
    const onExit = vi.fn();
    const conn = connectTerm("t1", { onData, onExit });
    const ws = FakeWS.instances[0]!;
    ws.open();
    vi.advanceTimersByTime(120_000);
    expect(ws.sent).toContain('{"t":"ping"}');
    conn.close();
    vi.advanceTimersByTime(240_000);
    expect(ws.sent.filter((s) => s === '{"t":"ping"}')).toHaveLength(1);
  });

  it("服务端异常关闭 → onExit(\"连接断开\")；主动 close / 已收 exit 帧不重复通知", () => {
    const onData = vi.fn();
    const onExit = vi.fn();
    const conn = connectTerm("t1", { onData, onExit });
    const ws = FakeWS.instances[0]!;
    ws.open();
    ws.close(); // 服务端/网络侧关闭（含拨号 404 后的 onerror→onclose）
    expect(onExit).toHaveBeenCalledTimes(1);
    expect(onExit).toHaveBeenCalledWith("连接断开");

    const onExit2 = vi.fn();
    const conn2 = connectTerm("t2", { onData, onExit: onExit2 });
    const ws2 = FakeWS.instances[1]!;
    ws2.open();
    conn2.close(); // 组件主动关闭（卸载）：不触发
    expect(onExit2).not.toHaveBeenCalled();

    const onExit3 = vi.fn();
    const conn3 = connectTerm("t3", { onData, onExit: onExit3 });
    const ws3 = FakeWS.instances[2]!;
    ws3.open();
    ws3.deliver({ t: "exit", error: "ssh: 断开" }); // 正常退出帧
    ws3.close(); // 其后的连接关闭是收尾：不二次通知
    expect(onExit3).toHaveBeenCalledTimes(1);
    expect(onExit3).toHaveBeenCalledWith("ssh: 断开");
    conn.close();
  });
});
