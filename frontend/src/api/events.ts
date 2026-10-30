// events.ts WS 订阅层：对话事件流（自动重连 + since 续传 + 断档全量重排 + 心跳）
// 与终端流（发送排队 + ping 保活 + 异常断开映射 exit）。心跳契约：服务端读
// 空闲 90s（chat）/ 5min（term）掐断，任意上行帧刷新 deadline。
import type { ChatEvent, ConfirmRequestView } from "../types";

function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return proto + "//" + location.host + path;
}

export interface ChatHandlers {
  onChat(ev: ChatEvent): void;
  onConfirm(p: { id: string; reqs: ConfirmRequestView[] }): void;
  onAsk(p: { id: string; input: unknown }): void;
  onConfirmDone(id: string): void;
  onAskDone(id: string): void;
  onReset(): void;
  /** WS 连接状态变化（评审 ④-1）：断线/重连中 → 顶部横幅。 */
  onStatus?(st: "connecting" | "open"): void;
  /** 每次 hello 通告：服务端绑定的会话 ID + 该会话的对话目标（空=未设定）。
   *  rebound=true 表示与请求不符（首连分配/stale 复活）——回调方需迁移状态。 */
  onHello(p: { session: string; scope: string; rebound: boolean }): void;
}

interface Frame {
  t: string;
  seq?: number;
  next?: number;
  session?: string;
  scope?: string;
  ev?: ChatEvent;
  id?: string;
  reqs?: ConfirmRequestView[];
  input?: unknown;
}

const HEARTBEAT_MS = 30_000; // < 90s 服务端读 deadline，留足余量

// subscribeChat 建立指定会话的对话 WS（session 空 = 服务端新建）。断线自动
// 重连（退避 0.5s→15s），重连带 ?since=lastSeq&session=… 续传；序号断档或
// 服务重启（hello.next 回退）时 onReset 后全量重排（since=0）；hello.session
// 与请求不符（stale 复活）时 onHello(newId) 重绑——回调方负责迁移状态。
// 返回断开函数。
export function subscribeChat(session: string, h: ChatHandlers): () => void {
  let sid = session;
  let lastSeq = 0;
  let ws: WebSocket | null = null;
  let closed = false;
  let backoff = 500;
  let hb: number | undefined;

  const sendHeartbeat = () => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send("{}"); // 服务端忽略上行内容，任意帧刷新读 deadline
    }
  };

  const dispatch = (f: Frame) => {
    if (f.seq !== undefined && f.seq <= lastSeq) {
      return; // subscribe→snapshot 窗口的重复帧：按序号去重
    }
    if (f.seq !== undefined) {
      lastSeq = f.seq;
    }
    switch (f.t) {
      case "chat":
        if (f.ev) {
          h.onChat(f.ev);
        }
        break;
      case "confirm":
        h.onConfirm({ id: f.id ?? "", reqs: f.reqs ?? [] });
        break;
      case "ask":
        h.onAsk({ id: f.id ?? "", input: f.input });
        break;
      case "confirm_done":
        if (f.id) {
          h.onConfirmDone(f.id);
        }
        break;
      case "ask_done":
        if (f.id) {
          h.onAskDone(f.id);
        }
        break;
      case "reset":
        h.onReset();
        lastSeq = 0; // 服务端 reset 常伴随全量重放：清序号防重放帧被当重复帧丢弃
        break;
      case "hello":
        if (f.session !== undefined) {
          const rebound = f.session !== sid;
          if (rebound) {
            // 首连分配 / stale 复活：换绑新会话 id，序号从头（回调方迁移状态）
            sid = f.session;
            lastSeq = 0;
          }
          h.onHello({ session: sid, scope: f.scope ?? "", rebound });
          break;
        }
        // 服务重启（序号回退）：清状态全量重排
        if (f.next !== undefined && f.next < lastSeq) {
          h.onReset();
          lastSeq = 0;
        }
        break;
    }
  };

  const connect = () => {
    if (closed) {
      return;
    }
    h.onStatus?.("connecting");
    ws = new WebSocket(
      wsURL("/api/ws/chat?since=" + lastSeq + "&session=" + encodeURIComponent(sid))
    );
    ws.onopen = () => {
      backoff = 500;
      h.onStatus?.("open");
      window.clearInterval(hb);
      hb = window.setInterval(sendHeartbeat, HEARTBEAT_MS);
    };
    ws.onmessage = (e) => {
      let f: Frame;
      try {
        f = JSON.parse(e.data as string) as Frame;
      } catch {
        return;
      }
      if (f.seq !== undefined && lastSeq > 0 && f.seq > lastSeq + 1) {
        // 断档：缓冲截断/丢帧 → 清状态后全量重连
        h.onReset();
        lastSeq = 0;
        ws?.close(); // onclose 触发重连（since=0）
        return;
      }
      dispatch(f);
    };
    ws.onclose = () => {
      window.clearInterval(hb);
      if (!closed) {
        backoff = Math.min(backoff * 2, 15000);
        setTimeout(connect, backoff);
      }
    };
    // onerror 不单独处理：随后必触发 onclose
  };
  connect();
  return () => {
    closed = true;
    window.clearInterval(hb);
    ws?.close();
  };
}

// ---- 终端 ----
export interface TermConn {
  send(data: string): void;
  resize(cols: number, rows: number): void;
  close(): void;
}

const TERM_PING_MS = 120_000; // < 5min 服务端读 deadline

// connectTerm 终端双向流。断开不自动重连（PTY 保持，重连即恢复——由
// 组件层决定何时重连）；ping 保活防 5min 空闲掐断（观看 tail -f 场景）。
// 异常断开（拨号 404/网络中断，浏览器只报 onclose）统一映射为
// onExit("连接断开")——组件遮罩逻辑复用；主动 close() 或已收 exit 帧不通知。
export function connectTerm(
  id: string,
  h: { onData(b64: string): void; onExit(err: string): void }
): TermConn {
  const ws = new WebSocket(wsURL("/api/ws/term?id=" + encodeURIComponent(id)));
  let exited = false; // 收到过 exit 帧：其后的连接关闭是正常收尾，不重复通知
  ws.onmessage = (e) => {
    let f: { t?: string; data?: string; error?: string };
    try {
      f = JSON.parse(e.data as string);
    } catch {
      return;
    }
    if (f.t === "data" && f.data) {
      h.onData(f.data);
    } else if (f.t === "exit") {
      exited = true;
      h.onExit(f.error ?? "");
    }
  };
  // 浏览器 WS 未 open 时 send 会静默丢弃：排队待 open 冲刷
  let open = false;
  const queue: string[] = [];
  let ping: number | undefined;
  const sendRaw = (s: string) => {
    if (open) {
      ws.send(s);
    } else {
      queue.push(s);
    }
  };
  ws.onopen = () => {
    open = true;
    while (queue.length > 0) {
      ws.send(queue.shift()!);
    }
    ping = window.setInterval(() => sendRaw('{"t":"ping"}'), TERM_PING_MS);
  };
  const stop = () => window.clearInterval(ping);
  let closedByUs = false;
  ws.onclose = () => {
    stop();
    if (!closedByUs && !exited) {
      exited = true;
      h.onExit("连接断开");
    }
  };
  ws.onerror = stop; // onerror 后必触发 onclose：异常统一在 onclose 归口
  return {
    send: (d) => sendRaw(JSON.stringify({ t: "write", data: d })),
    resize: (cols, rows) => sendRaw(JSON.stringify({ t: "resize", cols, rows })),
    close: () => {
      closedByUs = true;
      stop();
      ws.close();
    },
  };
}
