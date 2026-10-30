// Sidebar.tsx 服务器清单（目录树）：目录行整行点击展开/收起、递归计数与钉住徽标；
// 主机行状态点、名字、标签、钉住（对话默认目标）、打开终端入口，按深度缩进。
// 目录操作：右键空白（新建目录/添加主机）、右键目录（添加主机到此目录/删除目录
// ——删除=目录内主机移回根，行内二次确认）；主机行可拖拽进目录或拖到空白回根
// （虚拟目录随主机存在，新建目录即预填目录的主机表单）。删除主机走两次点击确认
// （防误删——钥匙串引用一并清除）。底部账户区（当前用户名 + 退出登录）。
import { useEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import gengarLogo from "../assets/gengar-logo.png";
import { allFolderPaths, buildTree } from "../folders";
import type { FolderNode } from "../folders";
import type { HostView } from "../types";

interface Props {
  hosts: HostView[];
  busy: boolean; // 任务进行中禁开设置/添加：SaveSettings 会重建 Agent 清空对话
  username: string; // 底部账户区显示的当前登录用户名
  onLogout: () => void; // 退出登录（服务端撤 token + 前端清现场）
  onDelete: (id: string) => void;
  onEdit: (h: HostView) => void;
  onAdd: () => void; // 添加主机（根目录）
  onAddIn: (folder: string) => void; // 在指定目录下添加主机（HostForm 预填目录）
  onDeleteFolder: (folder: string) => void; // 删除目录（目录内主机移回根）
  onMoveHost: (hostId: string, folder: string) => void; // 拖拽归类（folder="" 即回根）
  onSettings: () => void;
  onImport: () => void; // 从 XShell 批量导入会话
  isAdmin: boolean; // 仅 admin 渲染用户管理入口
  onUsers: () => void; // 打开用户管理弹窗
  onTerminal: (id: string) => void; // 终端与 Agent 任务并行（E7），busy 不禁用
}

export default function Sidebar({ hosts, busy, username, onLogout, onDelete, onEdit, onAdd, onAddIn, onDeleteFolder, onMoveHost, onSettings, onImport, isAdmin, onUsers, onTerminal }: Props) {
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  const tree = useMemo(() => buildTree(hosts), [hosts]);
  // 默认全展开——但只自动展开「新增」目录：用户已收起的不被后续刷新
  //（钉住切换/每轮任务结束的清单刷新/保存导入）重新翻开。
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const knownPathsRef = useRef<Set<string>>(new Set());
  useEffect(() => {
    const paths = new Set(allFolderPaths(hosts));
    const fresh = [...paths].filter((p) => !knownPathsRef.current.has(p));
    knownPathsRef.current = paths;
    if (fresh.length === 0) {
      return;
    }
    setExpanded((prev) => {
      const next = new Set(prev);
      for (const p of fresh) {
        next.add(p);
      }
      return next;
    });
  }, [hosts]);

  // 右键菜单：folder=null 为空白处菜单，否则为该目录的菜单
  const [menu, setMenu] = useState<{ x: number; y: number; folder: string | null } | null>(null);
  const [promptOpen, setPromptOpen] = useState(false); // 新建目录输入弹窗
  const [confirmingFolder, setConfirmingFolder] = useState<string | null>(null); // 删除目录二次确认
  const [dropTarget, setDropTarget] = useState<string | null>(null); // dragover 高亮的目录
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const dragHostRef = useRef<string>("");

  // 菜单打开期间：任意点击 / Escape 关闭
  useEffect(() => {
    if (!menu) {
      return;
    }
    const close = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setMenu(null);
      }
    };
    document.addEventListener("click", close);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("click", close);
      document.removeEventListener("keydown", onKey);
    };
  }, [menu]);

  const openMenu = (e: ReactMouseEvent, folder: string | null) => {
    e.preventDefault();
    e.stopPropagation();
    setConfirmingFolder(null);
    setMenu({ x: e.clientX, y: e.clientY, folder });
  };

  // 新建目录（虚拟目录语义：校验后直接进「添加主机」表单，随第一台主机诞生）
  const createFolder = (raw: string): string => {
    const name = raw.trim();
    if (name === "") {
      return "目录名不能为空";
    }
    if (name.includes("/")) {
      return "仅支持单级目录名（不含 /）";
    }
    if (allFolderPaths(hosts).includes(name)) {
      return "同名目录已存在";
    }
    setPromptOpen(false);
    onAddIn(name);
    return "";
  };

  const toggleExpand = (path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  const renderHost = (h: HostView, depth: number): JSX.Element => (
    <div
      key={h.id}
      className={"host-row" + (draggingId === h.id ? " dragging" : "")}
      style={{ marginLeft: depth * 14 }}
      draggable
      onDragStart={(e) => {
        dragHostRef.current = h.id;
        setDraggingId(h.id);
        e.dataTransfer.effectAllowed = "move";
        e.dataTransfer.setData("text/plain", h.id); // Firefox 需要非空 data 才启动拖拽
      }}
      onDragEnd={() => {
        setDraggingId(null);
        dragHostRef.current = "";
        setDropTarget(null);
      }}
    >
      <span className={"dot dot-" + h.status} title={h.status} />
      <div className="host-info">
        <div className="host-name">{h.name}</div>
        <div className="host-meta">
          {h.addr}:{h.port} · {h.user}
          {h.os ? " · " + h.os : ""}
        </div>
        {h.tags.length > 0 && (
          <div className="host-tags">
            {h.tags.map((t, i) => (
              <span key={t + "-" + i} className="tag">
                {t}
              </span>
            ))}
          </div>
        )}
      </div>
      <div className="host-ops">
        <button
          className="term"
          title="打开终端"
          onClick={() => onTerminal(h.id)}
        >
          &gt;_
        </button>
        <button
          className="edit"
          title={busy ? "任务进行中，稍后再编辑" : "编辑主机"}
          disabled={busy}
          onClick={() => onEdit(h)}
        >
          ✎
        </button>
        <button
          className={confirmingId === h.id ? "del confirming" : "del"}
          title={confirmingId === h.id ? "再点一次确认删除" : "删除主机"}
          onClick={() => {
            if (confirmingId === h.id) {
              setConfirmingId(null);
              onDelete(h.id);
            } else {
              setConfirmingId(h.id);
            }
          }}
        >
          {confirmingId === h.id ? "确认?" : "×"}
        </button>
      </div>
    </div>
  );

  const renderNodes = (nodes: FolderNode[]): JSX.Element[] =>
    nodes.flatMap((n) => {
      if (n.kind === "host") {
        return [renderHost(n.host, n.depth)];
      }
      const open = expanded.has(n.path);
      // 删除目录二次确认态：整行变为确认条（不再触发展开/收起）
      if (confirmingFolder === n.path) {
        return [
          <div key={"f-" + n.path} className="folder-row confirming" style={{ paddingLeft: 8 + n.depth * 14 }}>
            <span className="folder-name">⚠️ 移出「{n.name}」内 {n.hostCount} 台主机并删除？</span>
            <span className="folder-ops">
              <button
                className="btn danger small"
                onClick={(e) => {
                  e.stopPropagation();
                  setConfirmingFolder(null);
                  onDeleteFolder(n.path);
                }}
              >
                确认
              </button>
              <button
                className="btn ghost small"
                onClick={(e) => {
                  e.stopPropagation();
                  setConfirmingFolder(null);
                }}
              >
                取消
              </button>
            </span>
          </div>,
          ...(open ? renderNodes(n.children) : []),
        ];
      }
      return [
        <div
          key={"f-" + n.path}
          className={(dropTarget === n.path ? "folder-row drop-target" : "folder-row")}
          style={{ paddingLeft: 8 + n.depth * 14 }}
          role="button"
          tabIndex={0}
          aria-expanded={open}
          onClick={() => toggleExpand(n.path)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              toggleExpand(n.path);
            }
          }}
          onContextMenu={(e) => openMenu(e, n.path)}
          onDragOver={(e) => {
            if (dragHostRef.current === "") {
              return;
            }
            e.preventDefault();
            e.stopPropagation();
            e.dataTransfer.dropEffect = "move";
            setDropTarget(n.path);
          }}
          onDragLeave={() => setDropTarget((p) => (p === n.path ? null : p))}
          onDrop={(e) => {
            e.preventDefault();
            e.stopPropagation();
            const id = dragHostRef.current || e.dataTransfer.getData("text/plain");
            dragHostRef.current = "";
            setDraggingId(null);
            setDropTarget(null);
            if (id !== "") {
              onMoveHost(id, n.path);
              setExpanded((prev) => new Set(prev).add(n.path)); // 拖入后展开目标目录
            }
          }}
          title={n.path}
        >
          <span className="folder-caret">{open ? "▾" : "▸"}</span>
          <span className="folder-name">📁 {n.name}</span>
          <span className="folder-count">{n.hostCount}</span>
        </div>,
        ...(open ? renderNodes(n.children) : []),
      ];
    });

  return (
    <aside className="sidebar">
      <div className="sidebar-head">
        <div className="brand-row">
          <img src={gengarLogo} alt="Gengar" className="brand-logo" />
          <span className="brand-name">GENGAR PRO</span>
        </div>
        <div className="sidebar-actions">
          <button
            className="btn ghost small"
            onClick={onAdd}
            disabled={busy}
            title={busy ? "任务进行中，稍后再添加" : "添加主机"}
          >
            + 添加
          </button>
          <button
            className="btn ghost small"
            onClick={onImport}
            disabled={busy}
            title={busy ? "任务进行中，稍后再导入" : "从 XShell 导入会话"}
          >
            导入
          </button>
          {isAdmin && (
            <button
              className="btn ghost small"
              onClick={onSettings}
              disabled={busy}
              title={busy ? "任务进行中，稍后再设置" : "设置（API Key 等全局配置，仅管理员）"}
            >
              设置
            </button>
          )}
          {isAdmin && (
            <button
              className="btn ghost small"
              onClick={onUsers}
              disabled={busy}
              title={busy ? "任务进行中，稍后再管理" : "用户管理"}
            >
              用户
            </button>
          )}
        </div>
      </div>
      <div
        className="host-list"
        onContextMenu={(e) => openMenu(e, null)}
        onDragOver={(e) => {
          if (dragHostRef.current !== "") {
            e.preventDefault(); // 空白区（根）作为放置目标
          }
        }}
        onDrop={(e) => {
          e.preventDefault();
          const id = dragHostRef.current || e.dataTransfer.getData("text/plain");
          dragHostRef.current = "";
          setDraggingId(null);
          setDropTarget(null);
          if (id !== "") {
            onMoveHost(id, ""); // 拖到空白 = 移回根
          }
        }}
      >
        {hosts.length === 0 && (
          <div className="host-empty">还没有主机。点击「+ 添加」或右键新建目录。</div>
        )}
        {renderNodes(tree)}
      </div>
      <div className="sidebar-foot">
        <span className="foot-user" title="当前登录用户">
          {username}
        </span>
        <button className="btn ghost small" onClick={onLogout}>
          退出登录
        </button>
      </div>
      {promptOpen && <FolderPrompt onClose={() => setPromptOpen(false)} onCreate={createFolder} />}
      {menu &&
        (() => {
          const m = menu; // 捕获为 const：闭包内保持收窄
          const left = Math.min(m.x, window.innerWidth - 190);
          return (
            <div className="ctx-menu" style={{ left, top: m.y }} onClick={(e) => e.stopPropagation()}>
              {m.folder === null ? (
                <>
                  <button
                    className="ctx-item"
                    onClick={() => {
                      setMenu(null);
                      setPromptOpen(true);
                    }}
                  >
                    📁 新建目录…
                  </button>
                  <button
                    className="ctx-item"
                    onClick={() => {
                      setMenu(null);
                      onAdd();
                    }}
                  >
                    ＋ 添加主机
                  </button>
                </>
              ) : (
                <>
                  <button
                    className="ctx-item"
                    onClick={() => {
                      setMenu(null);
                      onAddIn(m.folder as string);
                    }}
                  >
                    ＋ 添加主机到此目录
                  </button>
                  <button
                    className="ctx-item danger"
                    onClick={() => {
                      setMenu(null);
                      setConfirmingFolder(m.folder as string);
                    }}
                  >
                    ✕ 删除目录…
                  </button>
                </>
              )}
            </div>
          );
        })()}
    </aside>
  );
}

// FolderPrompt 新建目录输入弹窗：虚拟目录语义——确认后直接进入预填该目录的
// 添加主机表单（目录随第一台主机诞生，取消表单即目录不存在）。
function FolderPrompt({ onClose, onCreate }: { onClose: () => void; onCreate: (name: string) => string }) {
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const submit = () => {
    setError(onCreate(name));
  };
  return (
    <div className="overlay">
      <div className="dialog folder-prompt">
        <div className="dialog-title">新建目录</div>
        <div className="form-grid">
          <label>
            目录名（单级）
            <input
              autoFocus
              value={name}
              placeholder="生产"
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  submit();
                }
              }}
            />
          </label>
        </div>
        {error !== "" && <div className="form-error">{error}</div>}
        <div className="dialog-footer">
          <span className="hint">创建后将打开添加主机表单</span>
          <button className="btn ghost" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" onClick={submit}>
            创建
          </button>
        </div>
      </div>
    </div>
  );
}
