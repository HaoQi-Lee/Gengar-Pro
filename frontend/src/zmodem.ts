// zmodem.ts —— zmodem.js 门面：终端字节流与 ZMODEM 传输的接管/恢复。
// 全项目唯一 import zmodem.js 的模块（dist bundle 挂 window.Zmodem；
// 浏览器 window===globalThis，node 测试侧须先 window=globalThis）。
// 组件只依赖本模块导出，不感知 zmodem.js 内部 API。
//
// 角色（协议事实，勿凭直觉改）：远端 rz 打印 ZRINIT → 我们是发送方
// （role="send"，上传）；远端 sz 打印 ZRQINIT → 我们是接收方
// （role="receive"，下载）。初始头字节会透传到终端（协议固有的
// `**B00…` 回显），正常。
import "zmodem.js/dist/zmodem.js";

// dist bundle 无类型；经门面收窄为少量调用面
const Zmodem: any = (globalThis as any).Zmodem;

export type XferPhase = "idle" | "sending" | "receiving" | "done" | "error";

export interface ZmodemState {
  phase: XferPhase;
  name: string;
  bytes: number; // 发送=已发字节；接收=已收字节
  total: number;
  startedAt: number; // performance.now()
  message: string;
}

export const idleState: ZmodemState = {
  phase: "idle", name: "", bytes: 0, total: 0, startedAt: 0, message: "",
};

export interface ZmodemHooks {
  writeTerm(b: Uint8Array): void; // 非 ZMODEM 字节 → xterm 渲染
  sendPty(b: Uint8Array): void; // 协议字节/rz 命令注入 → WS 二进制上行
  onState(s: ZmodemState): void; // 状态快照（不可变对象）
  pickFiles(): Promise<File[] | null>; // 无预存文件时弹系统选择器
  /** 下载落盘（默认 Zmodem.Browser.save_to_disk——内存 Blob + a[download]）；
   * 测试注入以在 node 下驱动完整接收回路。 */
  saveFile?(name: string, payloads: Uint8Array[]): void;
}

export interface ZmodemCtl {
  consume(b: Uint8Array): void; // 下行 PTY 字节（全部喂 Sentry）
  sendFiles(files: File[]): void; // 拖拽入口：注入 rz 并在就绪后发送
  isBusy(): boolean;
  cancel(): void;
  dispose(): void;
}

// RZ_WAIT_MS 拖拽注入 rz 后等待远端应答的时限：lrzsz 握手极快，超时
// 基本等于「没装 lrzsz / 不在 shell 提示符」。
const RZ_WAIT_MS = 10_000;

const u8 = (o: number[] | Uint8Array): Uint8Array => new Uint8Array(o);
const now = (): number => performance.now();
const errText = (err: unknown): string => (err instanceof Error ? err.message : String(err));

export function createZmodem(h: ZmodemHooks): ZmodemCtl {
  let state = idleState;
  let session: any = null; // 已确认的 ZMODEM 会话
  let detection: any = null; // 待确认检测（文件选择器打开期间）
  let pending: File[] | null = null; // 拖拽预存文件
  let picking = false; // 选择器单飞（rz 重发 ZRINIT 会重复触发 on_detect）
  let rzTimer: ReturnType<typeof setTimeout> | undefined;

  const set = (patch: Partial<ZmodemState>) => {
    state = { ...state, ...patch };
    h.onState(state);
  };

  const clearRzTimer = () => {
    if (rzTimer !== undefined) {
      clearTimeout(rzTimer);
      rzTimer = undefined;
    }
  };

  const sentry = new Zmodem.Sentry({
    to_terminal: (octets: number[]) => h.writeTerm(u8(octets)),
    on_retract: () => {
      // 检测被撤回（后续字节非 ZMODEM）：清检测态即可；选择器挂起中
      // 由其决议路径自查 is_valid()。
      detection = null;
      if (!session && state.phase !== "error" && state.phase !== "done") {
        set(idleState);
      }
    },
    sender: (octets: number[] | Uint8Array) => h.sendPty(u8(octets)),
    on_detect: (d: any) => {
      clearRzTimer();
      detection = d;
      if (d.get_session_role() === "send") {
        startSend();
      } else {
        startReceive();
      }
    },
  });

  // ---- 上传（ZRINIT 检测）----
  const startSend = () => {
    if (picking) {
      return; // 选择器已开：detection 引用已被 on_detect 刷新，不重开
    }
    const go = (files: File[] | null) => {
      picking = false;
      if (!files || files.length === 0) {
        if (detection && detection.is_valid()) {
          const sess = detection.confirm();
          try {
            sess.abort(); // 用户空选择：发中止序列让远端 rz 退出
          } catch {
            /* 已结束 */
          }
        }
        detection = null;
        set(idleState);
        return;
      }
      if (!detection || !detection.is_valid()) {
        set({ ...idleState, phase: "error", message: "ZMODEM 会话已失效" });
        return;
      }
      session = detection.confirm();
      detection = null;
      let total = 0;
      for (const f of files) {
        total += f.size;
      }
      let base = 0; // 已完成文件累计字节（多文件续算进度）
      set({
        phase: "sending", name: files[0]!.name, bytes: 0, total,
        startedAt: now(),
        message: files.length > 1 ? `共 ${files.length} 个文件` : "",
      });
      Zmodem.Browser.send_files(session, files, {
        on_progress: (f: File, xfer: any) => {
          if (xfer) {
            set({ name: f.name, bytes: base + xfer.get_offset() });
          }
        },
        on_file_complete: (f: File) => {
          base += f.size;
          set({ message: `已完成 ${f.name}` });
        },
      })
        .then(() => session.close())
        .then(() => {
          set({ phase: "done", message: "上传完成" });
        })
        .catch((err: unknown) => {
          set({ phase: "error", message: errText(err) });
        });
    };
    if (pending) {
      const files = pending;
      pending = null;
      go(files);
    } else {
      picking = true;
      h.pickFiles().then(go, () => go(null));
    }
  };

  // ---- 下载（ZRQINIT 检测，offer 自动接收——XShell 行为）----
  const startReceive = () => {
    session = detection.confirm();
    detection = null;
    set({ phase: "receiving", name: "", bytes: 0, total: 0, startedAt: now(), message: "等待文件信息…" });
    session.on("offer", (offer: any) => {
      const det = offer.get_details();
      let got = 0;
      set({ phase: "receiving", name: det.name, total: det.size, bytes: 0, message: "" });
      offer.on("input", (octets: number[]) => {
        got += octets.length;
        set({ bytes: got });
      });
      offer
        .accept()
        .then((payloads: Uint8Array[]) => {
          // accept 的决议值即载荷数组（zmodem.js 0.1.10 README 写的
          // offer.get_payloads() 并不存在——文档缺陷，库实现是
          // _accept_func(...).then(_get_spool)，见 zsession.js）。
          // 内存 Blob + a[download]：设计约束 ≤100MB 内可接受。
          const save =
            h.saveFile ?? ((name: string, p: Uint8Array[]) => Zmodem.Browser.save_to_disk(p, name));
          save(det.name, payloads);
        })
        .catch((err: unknown) => {
          set({ phase: "error", message: errText(err) });
        });
    });
    session.on("session_end", () => {
      if (state.phase !== "error") {
        set({ phase: "done", message: "传输结束" });
      }
      reset();
    });
    session.start();
  };

  const reset = () => {
    session = null;
    detection = null;
    pending = null;
  };

  return {
    consume: (b: Uint8Array) => {
      try {
        sentry.consume(b);
      } catch (err) {
        // 协议状态机异常：中止会话保终端可用（后续字节回透传）
        if (session) {
          try {
            session.abort();
          } catch {
            /* 已结束 */
          }
        }
        reset();
        set({ phase: "error", message: "ZMODEM 错误：" + errText(err) });
      }
    },

    sendFiles: (files: File[]) => {
      if (session || detection) {
        set({ phase: "error", message: "已有传输进行中" });
        return;
      }
      pending = files;
      set({
        phase: "sending", name: "", bytes: 0, total: 0,
        startedAt: now(), message: "已注入 rz，等待远端应答…",
      });
      h.sendPty(new TextEncoder().encode("rz\r"));
      clearRzTimer();
      rzTimer = setTimeout(() => {
        if (pending && !session && !detection) {
          pending = null;
          set({
            phase: "error",
            message: "远端无应答：确认已安装 lrzsz 且终端处于 shell 提示符",
          });
        }
      }, RZ_WAIT_MS);
    },

    isBusy: () => !!session || !!detection,

    cancel: () => {
      clearRzTimer();
      pending = null;
      if (session) {
        try {
          session.abort();
        } catch {
          /* 已结束 */
        }
        reset();
        set({ phase: "error", message: "已取消" });
      } else if (detection && detection.is_valid()) {
        detection.deny(); // Sentry 发 CAN×5 中止序列
        detection = null;
        set(idleState);
      }
    },

    dispose: () => {
      if (session || detection) {
        const s = session;
        session = null;
        detection = null;
        pending = null;
        clearRzTimer();
        if (s) {
          try {
            s.abort(); // 断线/卸载路径：尽量让远端 lrzsz 干净退出
          } catch {
            /* 已结束 */
          }
        }
      }
    },
  };
}

// fmtXfer 传输条显示派生（纯函数）：百分比封顶 100；速率按平均带宽换算。
export function fmtXfer(s: ZmodemState, nowMs: number): { pct: string; rate: string } {
  const pct =
    s.total > 0 ? Math.min(100, Math.floor((s.bytes / s.total) * 100)) + "%" : "";
  let rate = "";
  if (s.bytes > 0 && (s.phase === "sending" || s.phase === "receiving")) {
    const bps = (s.bytes * 1000) / Math.max(1, nowMs - s.startedAt);
    if (bps >= 1048576) {
      rate = (bps / 1048576).toFixed(1) + " MB/s";
    } else if (bps >= 1024) {
      rate = (bps / 1024).toFixed(1) + " KB/s";
    } else {
      rate = Math.round(bps) + " B/s";
    }
  }
  return { pct, rate };
}
