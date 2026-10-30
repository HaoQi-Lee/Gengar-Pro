// App.tsx 根组件：登录态/主机/弹窗/标签页状态。对话 tab 动态多开——每个
// tab 一个服务端会话（WS hello 绑定 id；stale 自动复活重绑），消息/busy/
// 确认弹窗按会话隔离路由；不同登录（哪怕同账号）各自建会话天然隔离。
// tab→会话映射存 sessionStorage（浏览器 tab 级），刷新恢复并重放。
// 事件处理全部走函数式 setState——订阅按 chat tab uid 注册一次，
// 会话 id 经 sidByUid ref 跟随重绑，无过期闭包问题。
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import Chat from "./components/Chat";
import InputBar from "./components/InputBar";
import Sidebar from "./components/Sidebar";
import ConfirmModal from "./components/ConfirmModal";
import AskModal, { type AskData, type AskOption } from "./components/AskModal";
import HostForm from "./components/HostForm";
import ErrorBoundary from "./components/ErrorBoundary";
import { Logout, Me, setUnauthorizedHandler } from "./api/client";
import {
  CancelTask,
  DeleteHost,
  GetHosts,
  OpenTerminal,
  ResolveAsk,
  ResolveConfirm,
  SaveHost,
  SetChatScope,
  SubmitMessage,
  TerminalClose,
} from "./api/client";
import { subscribeChat } from "./api/events";
import ScopePicker from "./components/ScopePicker";
import Login from "./components/Login";
import { applyChatEvent, type ChatState } from "./timing";
import type {
  ChatEvent,
  ConfirmRequestView,
  HostView,
} from "./types";

// 代码分割（评审 ④-5）：终端（xterm ~300KB）与设置/导入/用户弹窗懒加载——
// 登录后首屏不含 xterm，首包预计减半。Markdown 已在 Chat 内静态引用
// （对话区首屏即需要），单独分割无收益。
const TerminalPane = lazy(() => import("./components/TerminalPane"));
const Settings = lazy(() => import("./components/Settings"));
const ImportDialog = lazy(() => import("./components/ImportDialog"));
const UsersDialog = lazy(() => import("./components/UsersDialog"));

interface ConfirmData {
  session: string;
  id: string;
  reqs: ConfirmRequestView[];
}

type AskWithSession = AskData & { session: string };

// 对话 tab：uid 客户端稳定键（sessionId 重绑不变）；终端 tab 与此前一致
// （三态：拨号中/已连 sessionId/失败 error）。
type ChatTab = { kind: "chat"; uid: string; sessionId: string };
type TermTab = { kind: "term"; id: string; hostId: string; title: string; sessionId: string; error: string };
type Tab = ChatTab | TermTab;

const TABS_KEY = "gengar-chat-tabs";

function loadSavedTabs(): { chats: { uid: string; sessionId: string }[]; active: string } | null {
  try {
    const raw = sessionStorage.getItem(TABS_KEY);
    return raw ? (JSON.parse(raw) as { chats: { uid: string; sessionId: string }[]; active: string }) : null;
  } catch {
    return null;
  }
}

function saveTabs(chatTabs: ChatTab[], active: string): void {
  try {
    sessionStorage.setItem(
      TABS_KEY,
      JSON.stringify({
        chats: chatTabs.map(({ uid, sessionId }) => ({ uid, sessionId })),
        active,
      })
    );
  } catch {
    // sessionStorage 不可用（隐私模式等）：仅失去刷新恢复
  }
}

// ask_user 的 input：EventsEmit 里 json.RawMessage 内联为对象，也可能到端时是串
function parseAsk(id: string, input: unknown): AskData | null {
  let raw: unknown = input;
  if (typeof raw === "string") {
    try {
      raw = JSON.parse(raw);
    } catch {
      raw = null;
    }
  }
  const p = (raw ?? {}) as { question?: unknown; header?: unknown; options?: unknown };
  const options: AskOption[] = Array.isArray(p.options)
    ? p.options
        .map((o) => (o ?? {}) as { label?: unknown; description?: unknown })
        .map((o) => ({
          label: typeof o.label === "string" ? o.label : "",
          description: typeof o.description === "string" ? o.description : "",
        }))
        .filter((o) => o.label !== "")
    : [];
  return {
    id,
    question: typeof p.question === "string" ? p.question : "",
    header: typeof p.header === "string" ? p.header : undefined,
    options,
  };
}

const emptyChat = (): ChatState => ({ messages: [], thinking: null });

export default function App() {
  // null = 启动探测中；"" = 未登录；非空 = 已登录用户名
  const [username, setUsername] = useState<string | null>(null);
  const [isAdmin, setIsAdmin] = useState(false); // /api/me 的 role === "admin"
  const [hosts, setHosts] = useState<HostView[]>([]);
  const [showHostForm, setShowHostForm] = useState(false);
  const [editHost, setEditHost] = useState<HostView | null>(null); // 非空 = 编辑模式
  const [presetFolder, setPresetFolder] = useState(""); // 新建主机时预填的目录（右键目录添加）
  const [showSettings, setShowSettings] = useState(false);
  const [showImport, setShowImport] = useState(false);
  const [showUsers, setShowUsers] = useState(false);
  const [toast, setToast] = useState("");

  // 标签页：对话 tab 动态多开 + 每个打开的终端一个 tab。
  const [tabs, setTabs] = useState<Tab[]>([]);
  const [activeTab, setActiveTab] = useState("");

  // 按会话隔离的对话状态：sessionId → 消息流 / 忙态 / 轮起始 / 对话目标。
  const [chats, setChats] = useState<Record<string, ChatState>>({});
  const [busyMap, setBusyMap] = useState<Record<string, boolean>>({});
  const [scopes, setScopes] = useState<Record<string, string>>({}); // sessionId → 目标主机名（顿号连接，空=未设定）
  const turnStartRef = useRef<Record<string, number>>({});
  const [confirm, setConfirm] = useState<ConfirmData | null>(null);
  const [ask, setAsk] = useState<AskWithSession | null>(null);
  // WS 连接状态（评审 ④-1）：任一订阅断线 → 顶部横幅「重连中…」
  const [wsDown, setWsDown] = useState(false);
  const setWsStatus = useCallback((st: "connecting" | "open") => {
    setWsDown(st !== "open");
  }, []);

  const toastTimer = useRef<number | undefined>(undefined);
  const openingRef = useRef<Set<string>>(new Set()); // 拨号中的 hostId：防重复 POST
  const closedTabsRef = useRef<Set<string>>(new Set()); // 拨号中被关的 tab：session 建立后立即回收
  const uidRef = useRef(0); // chat tab uid 计数器
  const sidByUid = useRef<Map<string, string>>(new Map()); // chat tab uid → 当前会话 id
  const subsRef = useRef<Map<string, () => void>>(new Map()); // chat tab uid → 退订函数
  const showToast = useCallback((text: string) => {
    if (text === "") {
      return;
    }
    setToast(text);
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(""), 6000);
  }, []);

  const refreshHosts = useCallback((): Promise<HostView[]> => {
    return GetHosts()
      .then((hs) => {
        const list: HostView[] = (hs ?? []).map((h) => ({
          id: h.id,
          name: h.name,
          addr: h.addr,
          port: h.port,
          user: h.user,
          tags: h.tags ?? [],
          os: h.os,
          folder: h.folder ?? "",
          status: h.status === "ok" || h.status === "fail" ? h.status : "unknown",
          authMethod: h.authMethod,
          keyPath: h.keyPath,
          passwordRef: h.passwordRef,
        }));
        setHosts(list);
        return list;
      })
      .catch((): HostView[] => []);
  }, []);

  useEffect(() => {
    void refreshHosts();
  }, [refreshHosts]);

  // 登录态就绪：恢复 sessionStorage 的对话 tab（无则开一个）；登出清空。
  useEffect(() => {
    if (username === "") {
      for (const off of subsRef.current.values()) {
        off();
      }
      subsRef.current.clear();
      sidByUid.current.clear();
      setTabs([]);
      setActiveTab("");
      setChats({});
      setBusyMap({});
      setScopes({});
      setConfirm(null);
      setAsk(null);
      sessionStorage.removeItem(TABS_KEY);
      return;
    }
    if (!username) {
      return;
    }
    const saved = loadSavedTabs();
    const list: ChatTab[] =
      saved?.chats?.length
        ? saved.chats.map((c) => ({ kind: "chat" as const, uid: c.uid, sessionId: c.sessionId }))
        : [{ kind: "chat" as const, uid: "c1", sessionId: "" }];
    // uid 计数器取恢复列表的最大序号 +1（跳号关闭后也不与新 uid 撞车）
    uidRef.current = list.reduce((mx, c) => Math.max(mx, parseInt(c.uid.slice(1), 10) || 0), 0);
    setTabs(list);
    const active = saved?.active && list.some((c) => c.uid === saved.active) ? saved.active : list[0].uid;
    setActiveTab(active);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [username]);

  // 启动探测登录态；401（会话过期/他处登出）随时回落登录页
  useEffect(() => {
    Me()
      .then((me) => {
        setUsername(me.username);
        setIsAdmin(me.role === "admin");
      })
      .catch(() => setUsername(""));
    // 401 回落登录页时一并清弹窗/忙态/对话残留：重登录后不重现陈旧确认框
    setUnauthorizedHandler(() => {
      setUsername("");
      setIsAdmin(false);
    });
    return () => setUnauthorizedHandler(null);
  }, []);

  // 对话 WS 订阅：每个 chat tab uid 一次（events.ts 内部持有当前会话 id，
  // 重连/重绑不换订阅）；tab 关闭则退订。
  useEffect(() => {
    if (!username) {
      return;
    }
    const chatTabs = tabs.filter((t): t is ChatTab => t.kind === "chat");
    for (const t of chatTabs) {
      if (subsRef.current.has(t.uid)) {
        continue;
      }
      sidByUid.current.set(t.uid, t.sessionId);
      setChats((cs) => ({ ...cs, [t.sessionId]: cs[t.sessionId] ?? emptyChat() }));
      const clearSid = (sid: string) => {
        setChats((cs) => ({ ...cs, [sid]: emptyChat() }));
        setBusyMap((b) => ({ ...b, [sid]: false }));
      };
      const off = subscribeChat(t.sessionId, {
        onHello: ({ session: newSid, scope, rebound }) => {
          if (rebound) {
            // 首连分配 / stale 复活：迁移到新会话（旧内容不可续），清弹窗归属
            const old = sidByUid.current.get(t.uid) ?? "";
            sidByUid.current.set(t.uid, newSid);
            setChats((cs) => {
              const { [old]: _drop, ...rest } = cs;
              return { ...rest, [newSid]: emptyChat() };
            });
            setBusyMap((b) => {
              const { [old]: _d, ...rest } = b;
              return rest;
            });
            setScopes((sc) => {
              const { [old]: _d2, ...rest } = sc;
              return { ...rest, [newSid]: scope };
            });
            setConfirm((c) => (c && c.session === old ? null : c));
            setAsk((a) => (a && a.session === old ? null : a));
            setTabs((ts) => ts.map((x) => (x.kind === "chat" && x.uid === t.uid ? { ...x, sessionId: newSid } : x)));
          } else {
            // 重连/刷新：hello 携带服务端权威的目标（重启复活为空 → 弹回选择器）
            setScopes((sc) => ({ ...sc, [newSid]: scope }));
          }
        },
        onReset: () => {
          clearSid(sidByUid.current.get(t.uid) ?? "");
          showToast("对话已重置（设置变更或服务重启）");
        },
        onStatus: (st) => setWsStatus(st),
        onChat: (ev: ChatEvent) => {
          const sid = sidByUid.current.get(t.uid) ?? "";
          if (ev.type === "thinking" && ev.start) {
            setBusyMap((b) => ({ ...b, [sid]: true })); // start:false 被 omitempty 省略，真值判断
          }
          if (ev.type === "done") {
            setBusyMap((b) => ({ ...b, [sid]: false }));
            void refreshHosts(); // 探测可能更新了 OS/状态
          }
          // 时钟与轮基准在 updater 之外捕获：归约器保持纯函数（StrictMode 双调安全）
          const now = performance.now();
          const turnStartAt = turnStartRef.current[sid] ?? now;
          setChats((cs) => ({ ...cs, [sid]: applyChatEvent(cs[sid] ?? emptyChat(), ev, now, turnStartAt) }));
        },
        onConfirm: (p) => {
          const sid = sidByUid.current.get(t.uid) ?? "";
          setConfirm({ session: sid, id: p.id, reqs: p.reqs ?? [] });
        },
        onAsk: (p: { id: string; input: unknown }) => {
          const sid = sidByUid.current.get(t.uid) ?? "";
          const parsed = parseAsk(p.id, p.input);
          if (parsed) {
            setAsk({ ...parsed, session: sid });
          }
        },
        onConfirmDone: (id) => setConfirm((c) => (c && c.id === id ? null : c)),
        onAskDone: (id) => setAsk((a) => (a && a.id === id ? null : a)),
      });
      subsRef.current.set(t.uid, off);
    }
    for (const [uid, off] of subsRef.current) {
      if (!chatTabs.some((t) => t.uid === uid)) {
        off();
        subsRef.current.delete(uid);
        sidByUid.current.delete(uid);
      }
    }
  }, [username, tabs, refreshHosts]);

  // tab→会话映射持久化（sessionStorage 按浏览器 tab 隔离：双开即两会话）
  useEffect(() => {
    if (!username) {
      return;
    }
    saveTabs(tabs.filter((t): t is ChatTab => t.kind === "chat"), activeTab);
  }, [username, tabs, activeTab]);

  const activeChat = tabs.find((t): t is ChatTab => t.kind === "chat" && t.uid === activeTab);

  // ---- 用户操作 → 绑定调用 ----

  const openChat = () => {
    uidRef.current += 1;
    const uid = "c" + uidRef.current;
    setTabs((ts) => [...ts, { kind: "chat", uid, sessionId: "" }]);
    setActiveTab(uid);
  };

  const closeChat = (uid: string) => {
    const chatTabs = tabs.filter((t): t is ChatTab => t.kind === "chat");
    if (chatTabs.length <= 1) {
      return; // 最后一个对话 tab 不可关
    }
    const sid = sidByUid.current.get(uid) ?? "";
    delete turnStartRef.current[sid];
    // 关闭带挂起确认/提问的会话：提示用户服务端将按拒绝放行（断连
    // 语义与 hub 订阅归零一致），避免「批了再关」的错觉。
    if (confirm && confirm.session === sid) {
      setConfirm(null);
      showToast("确认弹窗随会话关闭——服务端按拒绝处理该操作");
    }
    if (ask && ask.session === sid) {
      setAsk(null);
      showToast("提问弹窗随会话关闭——服务端按取消处理");
    }
    setTabs((ts) => {
      const next = ts.filter((t) => !(t.kind === "chat" && t.uid === uid));
      if (activeTab === uid) {
        const first = next.find((t): t is ChatTab => t.kind === "chat");
        if (first) {
          setActiveTab(first.uid);
        }
      }
      return next;
    });
    setChats((cs) => {
      const { [sid]: _d, ...rest } = cs;
      return rest;
    });
    setBusyMap((b) => {
      const { [sid]: _d2, ...rest } = b;
      return rest;
    });
  };

  const submit = (text: string) => {
    const line = text.trim();
    if (line === "" || !activeChat) {
      return;
    }
    const sid = activeChat.sessionId;
    if (sid === "") {
      showToast("会话建立中，稍候再发送");
      return;
    }
    turnStartRef.current[sid] = performance.now();
    setChats((cs) => ({
      ...cs,
      [sid]: {
        messages: [...(cs[sid] ?? emptyChat()).messages, { role: "user", blocks: [{ kind: "text", text: line }] }],
        thinking: null,
      },
    }));
    setBusyMap((b) => ({ ...b, [sid]: true })); // 乐观置忙：斜杠命令不触发 thinking，等 done 解除
    SubmitMessage(sid, line).catch((e) => {
      // 降级启动（事件通道缺失）：promise reject 时事件解不了忙，手动复位
      setBusyMap((b) => ({ ...b, [sid]: false }));
      showToast(String(e));
    });
  };

  const cancel = () => {
    if (activeChat && activeChat.sessionId !== "") {
      CancelTask(activeChat.sessionId).catch(() => {});
    }
  };

  // 决议可靠性（评审 ②-4）：失败或 ok=false 时弹回弹窗 + toast——
  // 批准失败不能静默（用户以为批了实际没批）。只有决议真正被服务端
  // 接受才关弹窗。
  const resolveConfirm = (approved: boolean) => {
    const c = confirm;
    if (!c) {
      return;
    }
    ResolveConfirm(c.session, c.id, approved)
      .then((ok) => {
        if (ok) {
          setConfirm(null);
        } else {
          showToast("决议失败：确认弹窗仍保持，请重试或刷新");
        }
      })
      .catch((e) => {
        showToast("决议失败：" + String(e) + "（弹窗仍保持）");
      });
  };

  const resolveAsk = (answer: string) => {
    const a = ask;
    if (!a) {
      return;
    }
    ResolveAsk(a.session, a.id, answer)
      .then((ok) => {
        if (ok) {
          setAsk(null);
        } else {
          showToast("回答发送失败：提问弹窗仍保持，请重试");
        }
      })
      .catch((e) => {
        showToast("回答发送失败：" + String(e) + "（弹窗仍保持）");
      });
  };

  // 对话开始时一次性设定目标（选定后整个会话不可改——换范围=开新对话 tab）
  const applyScope = (t: ChatTab, hostIds: string[]) => {
    if (t.sessionId === "") {
      return; // 会话尚未绑定（hello 未回）：选择器开始钮已禁用，防御
    }
    const byId = new Map(hosts.map((h) => [h.id, h.name]));
    const names = hostIds.map((id) => byId.get(id) ?? id).sort().join("、");
    SetChatScope(t.sessionId, hostIds)
      .then(() => setScopes((sc) => ({ ...sc, [t.sessionId]: names })))
      .catch((e) => showToast(String(e)));
  };

  const deleteHost = (id: string) => {
    DeleteHost(id)
      .then(() => refreshHosts())
      .catch((e) => showToast(String(e)));
  };

  // 开终端：点击立即建 tab（连接中态），POST 完成后填 sessionId 接 WS；
  // 失败错误落在 tab 内（重试/关闭），不再依赖一闪而过的 toast。
  const startTerminal = (hostId: string) => {
    if (openingRef.current.has(hostId)) {
      return; // 拨号进行中：忽略连点
    }
    openingRef.current.add(hostId);
    const tabId = "term-" + hostId;
    const h = hosts.find((x) => x.id === hostId);
    setTabs((ts) => {
      if (ts.some((t) => t.kind === "term" && t.id === tabId)) {
        // 重试：复位到连接中态（tab 与位置保留）
        return ts.map((t) => (t.kind === "term" && t.id === tabId ? { ...t, sessionId: "", error: "" } : t));
      }
      return [...ts, { kind: "term", id: tabId, hostId, title: h?.name ?? hostId, sessionId: "", error: "" }];
    });
    setActiveTab(tabId);
    OpenTerminal(hostId)
      .then((sid) => {
        if (closedTabsRef.current.has(tabId)) {
          // 拨号期间用户已关 tab：服务器侧会话立即回收，不留孤儿 PTY
          closedTabsRef.current.delete(tabId);
          TerminalClose(sid).catch(() => {});
          return;
        }
        setTabs((ts) => ts.map((t) => (t.kind === "term" && t.id === tabId ? { ...t, sessionId: sid } : t)));
      })
      .catch((e) => {
        if (!closedTabsRef.current.has(tabId)) {
          setTabs((ts) => ts.map((t) => (t.kind === "term" && t.id === tabId ? { ...t, error: String(e) } : t)));
        }
      })
      .finally(() => openingRef.current.delete(hostId));
  };

  const openTerminal = (hostId: string) => {
    const tabId = "term-" + hostId;
    const exist = tabs.find((t): t is TermTab => t.kind === "term" && t.id === tabId);
    if (exist) {
      setActiveTab(tabId); // 已有 tab（连接中/已连/已失败）：聚焦，重试在 tab 内
      return;
    }
    startTerminal(hostId);
  };

  const closeTerminal = (id: string) => {
    const tab = tabs.find((t): t is TermTab => t.kind === "term" && t.id === id);
    if (tab) {
      if (tab.sessionId !== "") {
        TerminalClose(tab.sessionId).catch(() => {});
      } else {
        closedTabsRef.current.add(id); // 拨号中关闭：session 建立后回收
      }
    }
    setTabs((ts) => ts.filter((t) => !(t.kind === "term" && t.id === id)));
    if (activeTab === id) {
      const first = tabs.find((t): t is ChatTab => t.kind === "chat");
      setActiveTab(first ? first.uid : "");
    }
  };

  const addHost = (folder = "") => {
    setEditHost(null);
    setPresetFolder(folder);
    setShowHostForm(true);
  };

  // 拖拽归类：只改 folder——服务端编辑路径本就保留秘密引用与 KeyPath
  const moveHost = (hostId: string, folder: string) => {
    const h = hosts.find((x) => x.id === hostId);
    if (!h || h.folder === folder) {
      return; // 目标不存在或同目录：no-op
    }
    SaveHost({
      id: h.id,
      name: h.name,
      addr: h.addr,
      port: h.port,
      user: h.user,
      tags: h.tags,
      folder,
      authMethod: h.authMethod === "password" ? "password" : "key",
      password: "",
      keyPassphrase: "",
    })
      .then(() => refreshHosts())
      .catch((e) => showToast(String(e)));
  };

  // 删除目录 = 目录内全部主机移回根（虚拟目录随主机存在，清空即消失）
  const deleteFolder = (folder: string) => {
    const inside = hosts.filter((h) => h.folder === folder);
    if (inside.length === 0) {
      return;
    }
    Promise.all(
      inside.map((h) =>
        SaveHost({
          id: h.id,
          name: h.name,
          addr: h.addr,
          port: h.port,
          user: h.user,
          tags: h.tags,
          folder: "",
          authMethod: h.authMethod === "password" ? "password" : "key",
          password: "",
          keyPassphrase: "",
        })
      )
    )
      .then(() => refreshHosts())
      .then(() => showToast(`已移出 ${inside.length} 台主机，目录已删除`))
      .catch((e) => showToast(String(e)));
  };

  const editHostFn = (h: HostView) => {
    setEditHost(h);
    setShowHostForm(true);
  };

  const closeHostForm = () => {
    setEditHost(null);
    setShowHostForm(false);
  };

  const settingsSaved = () => {
    // SaveSettings 重建了全部会话的 Agent 并广播 reset 帧（评审 ②-5）：
    // 各 tab 的 onReset 清对话状态并提示。此处不再手动清——统一走
    // reset 帧通道，本地保存者与远端用户看到同一语义。
  };

  // 主动退出：撤服务端 token 后给干净现场——换账号登录不残留上一账号的
  // 对话/终端标签（与 401 回落不同：意外过期保留现场合理，主动退出应清空）。
  // 终端只关前端 tab 不调 TerminalClose：PTY 保持，与其他关标签路径一致。
  const logout = () => {
    Logout().catch(() => {}); // 失败也照常回登录页（cookie 即将无效）
    setUsername("");
  };

  // 弹窗层叠与渲染顺序一致（settings > hostForm > import > users > ask/confirm，后渲染的盖上面）：
  // 只有最上层弹窗响应键盘，避免 Enter 在表单里误触发被遮挡确认框的批准。
  const topOverlay: "settings" | "hostForm" | "import" | "users" | "ask" | "confirm" | null = showSettings
    ? "settings"
    : showHostForm
      ? "hostForm"
      : showImport
        ? "import"
        : showUsers
          ? "users"
          : ask
            ? "ask"
            : confirm
              ? "confirm"
              : null;

  // 认证守卫：探测中显示载入页；未登录显示登录页（其余 UI 不挂载）
  if (username === null) {
    return <div className="app boot">载入中…</div>;
  }
  if (username === "") {
    return <Login onDone={(me) => { setUsername(me.username); setIsAdmin(me.role === "admin"); void refreshHosts(); }} />;
  }

  const chatTabs = tabs.filter((t): t is ChatTab => t.kind === "chat");
  const termTabs = tabs.filter((t): t is TermTab => t.kind === "term");
  const chatLabel = (i: number) => (chatTabs.length > 1 ? `💬 对话 ${i + 1}` : "💬 对话");

  return (
    <div className="app">
      {wsDown && <div className="ws-banner">⚠ 连接已断开，正在重连…（事件会在恢复后自动补发）</div>}
      <Sidebar
        hosts={hosts}
        busy={Object.values(busyMap).some(Boolean)}
        username={username}
        onLogout={logout}
        onDelete={deleteHost}
        onEdit={editHostFn}
        onAdd={() => addHost()}
        onAddIn={(f) => addHost(f)}
        onDeleteFolder={deleteFolder}
        onMoveHost={moveHost}
        onSettings={() => setShowSettings(true)}
        onImport={() => setShowImport(true)}
        isAdmin={isAdmin}
        onUsers={() => setShowUsers(true)}
        onTerminal={openTerminal}
      />
      <div className="main">
        <div className="tabbar">
          {chatTabs.map((t, i) => (
            <button
              key={t.uid}
              className={activeTab === t.uid ? "tab active" : "tab"}
              onClick={() => setActiveTab(t.uid)}
            >
              <span className="tab-label">{chatLabel(i)}</span>
              {chatTabs.length > 1 && (
                <span
                  className="tab-close"
                  title="关闭对话"
                  role="button"
                  tabIndex={0}
                  onClick={(e) => {
                    e.stopPropagation();
                    closeChat(t.uid);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.stopPropagation();
                      e.preventDefault();
                      closeChat(t.uid);
                    }
                  }}
                >
                  ×
                </span>
              )}
            </button>
          ))}
          <button className="tab new-chat" title="新开对话（独立会话）" onClick={openChat}>
            ＋
          </button>
          {termTabs.map((t) => (
            <button
              key={t.id}
              className={activeTab === t.id ? "tab active" : "tab"}
              onClick={() => setActiveTab(t.id)}
            >
              <span className="tab-label">&gt;_ {t.title}</span>
              <span
                className="tab-close"
                title="断开并关闭"
                role="button"
                tabIndex={0}
                onClick={(e) => {
                  e.stopPropagation();
                  closeTerminal(t.id);
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.stopPropagation();
                    e.preventDefault();
                    closeTerminal(t.id);
                  }
                }}
              >
                ×
              </span>
            </button>
          ))}
        </div>
        {chatTabs.map((t) => {
          const scope = scopes[t.sessionId] ?? "";
          const picking = scope === ""; // 目标一经选定不再弹出：换范围=开新对话 tab
          return (
            <div key={t.uid} className={activeTab === t.uid ? "pane pane-active" : "pane"}>
              <ErrorBoundary label="对话区">
                <Chat
                  messages={chats[t.sessionId]?.messages ?? []}
                  thinking={chats[t.sessionId]?.thinking ?? null}
                />
              </ErrorBoundary>
              <InputBar
                busy={busyMap[t.sessionId] ?? false}
                disabled={picking}
                modalOpen={topOverlay !== null}
                onSubmit={submit}
                onCancel={cancel}
              />
              {picking && (
                <ScopePicker
                  hosts={hosts}
                  sessionReady={t.sessionId !== ""}
                  onConfirm={(ids) => applyScope(t, ids)}
                />
              )}
            </div>
          );
        })}
        {termTabs.map((t) => (
          <div key={t.id} className={activeTab === t.id ? "pane pane-active" : "pane"}>
            <ErrorBoundary label="终端">
              {t.error !== "" ? (
              <div className="term-state">
                <div className="term-state-title">连接失败</div>
                <div className="term-state-msg">{t.error}</div>
                <div className="term-state-ops">
                  <button className="btn ghost small" onClick={() => startTerminal(t.hostId)}>
                    重试
                  </button>
                  <button className="btn ghost small" onClick={() => closeTerminal(t.id)}>
                    关闭
                  </button>
                </div>
              </div>
            ) : t.sessionId !== "" ? (
              <Suspense fallback={<div className="term-state"><div className="term-state-msg">加载终端组件…</div></div>}>
                <TerminalPane id={t.sessionId} visible={activeTab === t.id} />
              </Suspense>
            ) : (
              <div className="term-state">
                <div className="term-state-msg">正在连接 {t.title}…</div>
              </div>
            )}
            </ErrorBoundary>
          </div>
        ))}
      </div>
      {confirm && (
        <ConfirmModal
          data={{ id: confirm.id, reqs: confirm.reqs }}
          active={topOverlay === "confirm"}
          onResolve={resolveConfirm}
        />
      )}
      {ask && <AskModal data={ask} active={topOverlay === "ask"} onAnswer={resolveAsk} />}
      {showHostForm && (
        <HostForm
          key={editHost?.id ?? "new-" + presetFolder}
          edit={editHost}
          presetFolder={editHost ? undefined : presetFolder}
          onClose={closeHostForm}
          onSaved={refreshHosts}
          onToast={showToast}
        />
      )}
      {showImport && (
        <Suspense fallback={null}>
          <ImportDialog
            onClose={() => setShowImport(false)}
            onImported={refreshHosts}
            onToast={showToast}
          />
        </Suspense>
      )}
      {showSettings && (
        <Suspense fallback={null}>
          <Settings onClose={() => setShowSettings(false)} onSaved={settingsSaved} />
        </Suspense>
      )}
      {showUsers && (
        <Suspense fallback={null}>
          <UsersDialog me={username} onClose={() => setShowUsers(false)} onToast={showToast} />
        </Suspense>
      )}
      {toast !== "" && <div className="toast">{toast}</div>}
    </div>
  );
}
