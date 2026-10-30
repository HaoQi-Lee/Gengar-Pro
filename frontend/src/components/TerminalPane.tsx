// TerminalPane.tsx 单个 SSH 终端：xterm 渲染 WS 下行流（base64 → 字节），
// 输入 onData → conn.send 上行；ResizeObserver → fit → conn.resize。
// 断线（exit 帧，含连接异常统一映射）后停止上行并显示遮罩；tab 关闭由外层
// 调 TerminalClose 后卸载本组件（卸载只 dispose 渲染端）。隐藏期间保持挂载
// 以保留回滚缓冲。
import { useEffect, useRef, useState } from "react";
import type { MouseEvent } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { connectTerm } from "../api/events";
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
  const [exited, setExited] = useState(false);
  const [exitErr, setExitErr] = useState("");

  // 右键菜单（自绘：WebView 吞默认菜单；结构留扩展位——后续全选/清屏等）
  const [menu, setMenu] = useState<{ x: number; y: number; hasSel: boolean } | null>(null);

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
      return; // 非安全上下文：右键菜单按钮已禁用；键位分支已放行原生粘贴路径
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

  // attachCustomKeyEventHandler 的闭包在 [id] effect 里冻结，经 ref 转发到最新实现
  const copySelectionRef = useRef(copySelection);
  const pasteClipboardRef = useRef(pasteClipboard);
  // 无依赖 effect 每次渲染后同步，避免渲染期写 ref
  useEffect(() => {
    copySelectionRef.current = copySelection;
    pasteClipboardRef.current = pasteClipboard;
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
      if (!exitedRef.current) {
        connRef.current?.send(d); // 断线后的迟到击键：连接已关自然丢弃
      }
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
    connRef.current = connectTerm(id, {
      onData: (b64) => term.write(b64ToBytes(b64)),
      onExit: (err) => {
        exitedRef.current = true;
        setExited(true);
        setExitErr(err);
        term.write("\r\n\x1b[31m[连接已断开" + (err ? "：" + err : "") + "]\x1b[0m\r\n");
      },
    });
    return () => {
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

  const openMenu = (e: MouseEvent) => {
    e.preventDefault();
    // 钳制在窗口内（菜单约 140x64px，向下向右展开）
    const x = Math.min(e.clientX, window.innerWidth - 150);
    const y = Math.min(e.clientY, window.innerHeight - 70);
    setMenu({ x, y, hasSel: termRef.current?.hasSelection() ?? false });
  };

  // 菜单开着时 Esc / 窗口失焦关闭（窗口级监听：焦点可能在终端内）
  useEffect(() => {
    if (!menu) {
      return;
    }
    const close = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        close();
      }
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("blur", close); // 失焦关闭（设计 §3）：切窗后不残留菜单
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", close);
    };
  }, [menu]);

  return (
    <div className="term-pane" onContextMenu={openMenu}>
      <div ref={hostRef} className="term-host" />
      {exited && (
        <div className="term-exit">
          连接已断开{exitErr ? "：" + exitErr : ""}，关闭标签页结束
        </div>
      )}
      {menu && (
        <>
          {/* 点击外部/失焦关闭：透明全屏层，点击不冒泡到终端 */}
          <div className="term-menu-mask" onClick={() => setMenu(null)} onContextMenu={(e) => { e.preventDefault(); setMenu(null); }} />
          <div className="term-menu" style={{ left: menu.x, top: menu.y }}>
            <button
              className="term-menu-item"
              disabled={!menu.hasSel}
              onClick={() => {
                copySelection();
                setMenu(null);
              }}
            >
              复制
            </button>
            <button
              className="term-menu-item"
              disabled={!navigator.clipboard}
              title={navigator.clipboard ? undefined : "需 HTTPS 或 localhost"}
              onClick={() => {
                pasteClipboard();
                setMenu(null);
              }}
            >
              粘贴
            </button>
          </div>
        </>
      )}
    </div>
  );
}
