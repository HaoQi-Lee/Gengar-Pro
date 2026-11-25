// TerminalPane.tsx 单个 SSH 终端：xterm 渲染 WS 下行流（base64 → 字节），
// 输入 onData → conn.send 上行；ResizeObserver → fit → conn.resize。
// 断线（exit 帧，含连接异常统一映射）后停止上行并显示遮罩；tab 关闭由外层
// 调 TerminalClose 后卸载本组件（卸载只 dispose 渲染端）。隐藏期间保持挂载
// 以保留回滚缓冲。
//
// 复制粘贴：不自绘右键菜单、不拦 contextmenu——浏览器原生菜单即工作
// （xterm 在右键时把 helper textarea 移到光标下聚焦，菜单「粘贴」作用于
// textarea → 原生 paste 事件 → onData 上行；菜单「复制」走 copy 事件 →
// xterm copyHandler 填 selectionText。浏览器代读剪贴板，不受 http 非安全
// 上下文限制）。另有 XShell 习惯键位（下方 attachCustomKeyEventHandler）。
// 自绘菜单是 Wails 桌面时代的前提（WebView2 默认吞菜单），Web 化后不成立。
//
// ZMODEM（2026-11-25 设计）：下行字节先过 zmodem 门面——非协议字节进
// xterm、协议字节驱动 rz/sz 传输；传输中键盘输入被门禁（Ctrl+C=取消）；
// 拖拽文件到面板=注入 rz 并发送（落到远端 shell 当前目录）；下载 offer
// 自动接收经 Blob 保存。初始头会透传显示 `**B00…` 回显，协议固有现象。
import { useEffect, useRef, useState, type DragEvent as ReactDragEvent } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { connectTerm } from "../api/events";
import { createZmodem, fmtXfer, idleState, type ZmodemCtl, type ZmodemState } from "../zmodem";
import "@xterm/xterm/css/xterm.css";

function b64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}

export default function TerminalPane({ id, visible }: { id: string; visible: boolean }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const exitedRef = useRef(false);
  const connRef = useRef<ReturnType<typeof connectTerm> | null>(null);
  const zctlRef = useRef<ZmodemCtl | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const [exited, setExited] = useState(false);
  const [exitErr, setExitErr] = useState("");
  const [zs, setZs] = useState<ZmodemState>(idleState);
  const [nowMs, setNowMs] = useState(0); // 速率时钟：进度事件稀疏时驱动重渲染

  const copySelection = () => {
    const t = termRef.current;
    if (!t || !t.hasSelection()) {
      return;
    }
    const text = t.getSelection();
    try {
      if (navigator.clipboard) {
        navigator.clipboard.writeText(text).catch(() => {}); // 暂态权限/占用冲突：静默
      } else {
        // http 非安全上下文：navigator.clipboard undefined，直接调 .writeText 会
        // 同步抛 TypeError——回退 execCommand（用户手势内对 http 可用）：离屏
        // 临时 textarea 承载选区文本（fixed 不引发滚动）
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.left = "-9999px";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      // 保留选区（XShell 行为，C4）：复制是否成功可由选区仍在感知
    } catch {
      // 暂态权限/占用冲突：静默
    }
  };

  const pasteClipboard = () => {
    if (!navigator.clipboard) {
      return; // 非安全上下文：右键粘贴走浏览器原生菜单；键位分支放行原生粘贴
    }
    navigator.clipboard
      .readText()
      .then((text) => {
        if (text !== "") {
          termRef.current?.paste(text); // 与击键同路径：onData → WS write 上行
        }
      })
      .catch(() => {}); // 读剪贴板失败（权限/占用）：静默，暂态
  };

  // pickFiles：弹系统文件选择器（ZRINIT 检测后无预存文件时门面调用）。
  // oncancel（较新浏览器）兜底取消；不支持的浏览器上取消仅静默挂起——
  // 门面侧检测撤回/超时路径会自行复位。
  const pickFiles = () =>
    new Promise<File[] | null>((resolve) => {
      const inp = fileRef.current;
      if (!inp) {
        resolve(null);
        return;
      }
      const done = (files: File[] | null) => {
        inp.onchange = null;
        inp.oncancel = null;
        resolve(files);
      };
      inp.onchange = () => {
        done(inp.files && inp.files.length > 0 ? Array.from(inp.files) : null);
        inp.value = ""; // 允许再次选择同一文件
      };
      inp.oncancel = () => done(null);
      inp.click();
    });

  // attachCustomKeyEventHandler 的闭包在 [id] effect 里冻结，经 ref 转发到最新实现
  const copySelectionRef = useRef(copySelection);
  const pasteClipboardRef = useRef(pasteClipboard);
  const pickFilesRef = useRef(pickFiles);
  // 无依赖 effect 每次渲染后同步，避免渲染期写 ref
  useEffect(() => {
    copySelectionRef.current = copySelection;
    pasteClipboardRef.current = pasteClipboard;
    pickFilesRef.current = pickFiles;
  });

  useEffect(() => {
    const term = new Terminal({
      fontFamily: 'ui-monospace, "Cascadia Code", Consolas, monospace',
      fontSize: 13,
      cursorBlink: true,
      // 底色透明：深紫底与耿鬼水印由 .term-pane 承载（allowTransparency 允许透出）
      allowTransparency: true,
      theme: { background: "#00000000" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostRef.current!);
    termRef.current = term;
    fitRef.current = fit;
    fit.fit();
    term.focus();
    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== "keydown") {
        return true;
      }
      // XShell 双习惯键位（C3）：Ctrl+Shift+C / Ctrl+Insert 复制，Ctrl+Shift+V / Shift+Insert 粘贴
      const copy = (ev.ctrlKey && ev.shiftKey && ev.key.toUpperCase() === "C") ||
        (ev.ctrlKey && ev.key === "Insert");
      const paste = (ev.ctrlKey && ev.shiftKey && ev.key.toUpperCase() === "V") ||
        (ev.shiftKey && ev.key === "Insert");
      if (copy) {
        copySelectionRef.current();
        return false; // 不进终端输入流（Ctrl+C 留给 SIGINT 的语义不变）
      }
      if (paste) {
        if (navigator.clipboard) {
          pasteClipboardRef.current();
          return false; // 不进终端输入流
        }
        return true; // 剪贴板 API 缺失（http 非安全上下文）：放行 xterm 原生
        // paste 事件——textarea 原生粘贴不受安全上下文限制，内容经 onData 上行
      }
      return true; // 其余键（含 Ctrl+C 的 SIGINT）照常进终端
    });
    term.onData((d) => {
      if (exitedRef.current) {
        return;
      }
      const z = zctlRef.current;
      if (z && z.isBusy()) {
        if (d === "\x03") {
          z.cancel(); // 传输中的 Ctrl+C 等同取消按钮；其余键丢弃（会话由协议接管）
        }
        return;
      }
      connRef.current?.send(d); // 断线后的迟到击键：连接已关自然丢弃
    });
    const ro = new ResizeObserver(() => {
      // display:none 下 fit 拿不到真实尺寸，只在可见时同步
      const el = hostRef.current;
      if (!el || el.clientHeight === 0) {
        return;
      }
      fit.fit();
      if (!exitedRef.current) {
        connRef.current?.resize(term.cols, term.rows);
      }
    });
    ro.observe(hostRef.current!);
    // ZMODEM 门面：非协议字节进 xterm；协议字节走二进制上行；状态快照驱动传输条
    const zctl = createZmodem({
      writeTerm: (b) => term.write(b),
      sendPty: (b) => {
        if (!exitedRef.current) {
          connRef.current?.sendBytes(b);
        }
      },
      onState: setZs,
      pickFiles: () => pickFilesRef.current(),
    });
    zctlRef.current = zctl;
    connRef.current = connectTerm(id, {
      onData: (b64) => zctl.consume(b64ToBytes(b64)),
      onExit: (err) => {
        exitedRef.current = true;
        zctl.dispose(); // 传输中止（发 ZABORT）：远端 lrzsz 尽量干净退出
        setExited(true);
        setExitErr(err);
        term.write("\r\n\x1b[31m[连接已断开" + (err ? "：" + err : "") + "]\x1b[0m\r\n");
      },
    });
    return () => {
      zctl.dispose();
      zctlRef.current = null;
      connRef.current?.close();
      connRef.current = null;
      ro.disconnect();
      term.dispose();
    };
  }, [id]);

  // 隐藏期间容器无尺寸；重新可见时补 fit 并聚焦
  useEffect(() => {
    if (visible && fitRef.current) {
      fitRef.current.fit();
      termRef.current?.focus();
    }
  }, [visible]);

  // 拖拽上传：不做提示符检测（vim/less 中拖拽 rz 字符会进编辑器——与
  // XShell 同病，设计已接受）；拖入即注入 rz 并发送。
  const handleDrop = (e: ReactDragEvent<HTMLDivElement>) => {
    e.preventDefault();
    const files = Array.from(e.dataTransfer?.files ?? []);
    if (files.length > 0 && !exitedRef.current) {
      zctlRef.current?.sendFiles(files);
    }
  };

  // 传输中每 500ms 刷新速率显示（进度事件本身也驱动重渲染）
  useEffect(() => {
    if (zs.phase !== "sending" && zs.phase !== "receiving") {
      return;
    }
    const t = window.setInterval(() => setNowMs(performance.now()), 500);
    return () => window.clearInterval(t);
  }, [zs.phase]);

  // 完成态 3s 后自动隐藏传输条（错误态保留到下次传输/检测复位）
  useEffect(() => {
    if (zs.phase === "done") {
      const t = window.setTimeout(() => setZs(idleState), 3000);
      return () => window.clearTimeout(t);
    }
  }, [zs.phase]);

  const busy = zs.phase === "sending" || zs.phase === "receiving";
  const { pct, rate } = fmtXfer(zs, nowMs || performance.now());

  return (
    <div
      className="term-pane"
      onDragOver={(e) => e.preventDefault()} /* 放行 drop（否则浏览器直接打开文件） */
      onDrop={handleDrop}
    >
      <div ref={hostRef} className="term-host" />
      <input ref={fileRef} type="file" multiple style={{ display: "none" }} />
      {(busy || zs.phase === "done" || zs.phase === "error") && (
        <div className={"term-xfer" + (zs.phase === "error" ? " xfer-error" : "")}>
          <span className="xfer-icon">{zs.phase === "receiving" ? "⇣" : "⇡"}</span>
          <span className="xfer-name">{zs.name || zs.message}</span>
          {pct && <span className="xfer-pct">{pct}</span>}
          {rate && <span className="xfer-rate">{rate}</span>}
          {busy && (
            <button className="btn xfer-cancel" onClick={() => zctlRef.current?.cancel()}>
              取消
            </button>
          )}
        </div>
      )}
      {exited && (
        <div className="term-exit">
          连接已断开{exitErr ? "：" + exitErr : ""}，关闭标签页结束
        </div>
      )}
    </div>
  );
}
