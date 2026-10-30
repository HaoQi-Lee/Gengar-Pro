// client.ts REST 封装。函数名与签名对齐各端点的请求/响应形状（组件层只
// 依赖本模块）；返回形状在函数内解包（{hosts}/{id}/{message}/{ok}）。
// 401 统一经 setUnauthorizedHandler 回调（App 切回登录页）；登录接口
// 例外（raw401：透传 body 的 {error} 文案，不触发回调）。
import type { HostReq, HostView, SettingsView, UserView, XshellRowView } from "../types";

export class ApiError extends Error {
  status: number;
  constructor(msg: string, status: number) {
    super(msg);
    this.status = status;
  }
}

let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

// raw401：401 不触发全局未登录回调、不走固定文案，读 body 的 {error} 透传
//（登录失败要显示「用户名或密码错误」而非「未登录或会话过期」）。
async function call<T>(
  method: string,
  path: string,
  body?: unknown,
  opts?: { raw401?: boolean }
): Promise<T> {
  const init: RequestInit = { method };
  if (body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (res.status === 401 && !opts?.raw401) {
    onUnauthorized?.();
    throw new ApiError("未登录或会话过期", 401);
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const j = await res.json();
      if (j && typeof j.error === "string") {
        msg = j.error;
      }
    } catch {
      // 非 JSON 错误体：保留 HTTP 状态描述
    }
    throw new ApiError(msg, res.status);
  }
  const ct = res.headers.get("Content-Type") ?? "";
  if (!ct.includes("application/json")) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

// ---- 登录 ----
export function Login(username: string, password: string): Promise<{ username: string; role: string }> {
  return call("POST", "/api/login", { username, password }, { raw401: true });
}
export function Logout(): Promise<void> {
  return call("POST", "/api/logout");
}
export function Me(): Promise<{ username: string; role: string }> {
  return call("GET", "/api/me");
}

// ---- 主机 ----
export async function GetHosts(): Promise<HostView[] | null> {
  const j = await call<{ hosts: HostView[] | null }>("GET", "/api/hosts");
  return j?.hosts ?? null;
}
export function SaveHost(req: HostReq): Promise<void> {
  return call("POST", "/api/hosts", req);
}
export async function TestHostDraft(req: HostReq): Promise<string> {
  const j = await call<{ message: string }>("POST", "/api/hosts/test", req);
  return j?.message ?? "";
}
export function DeleteHost(id: string): Promise<void> {
  return call("DELETE", `/api/hosts/${encodeURIComponent(id)}`);
}
export async function TestHost(id: string): Promise<string> {
  const j = await call<{ message: string }>("POST", `/api/hosts/${encodeURIComponent(id)}/test`);
  return j?.message ?? "";
}
export async function OpenTerminal(hostId: string): Promise<string> {
  const j = await call<{ id: string }>("POST", `/api/hosts/${encodeURIComponent(hostId)}/term`);
  return j?.id ?? "";
}
export function TerminalClose(id: string): Promise<void> {
  return call("POST", `/api/term/${encodeURIComponent(id)}/close`);
}

// ---- 对话目标（每个会话独立） ----
export function SetChatScope(session: string, hostIds: string[]): Promise<void> {
  return call("PUT", "/api/chat/scope", { session, hostIds });
}

// ---- 设置 ----
export function GetSettings(): Promise<SettingsView> {
  return call("GET", "/api/settings");
}
export function SaveSettings(s: SettingsView): Promise<void> {
  return call("PUT", "/api/settings", s);
}

// ---- 对话（按会话路由：每个对话 tab 一个 session） ----
export function SubmitMessage(session: string, text: string): Promise<void> {
  return call("POST", "/api/chat", { text, session });
}
export function CancelTask(session: string): Promise<void> {
  return call("POST", "/api/chat/cancel", { session });
}
export async function ResolveConfirm(session: string, id: string, approved: boolean): Promise<boolean> {
  const j = await call<{ ok: boolean }>("POST", `/api/confirm/${encodeURIComponent(id)}`, {
    approved,
    session,
  });
  return j?.ok === true;
}
export async function ResolveAsk(session: string, id: string, answer: string): Promise<boolean> {
  const j = await call<{ ok: boolean }>("POST", `/api/ask/${encodeURIComponent(id)}`, {
    answer,
    session,
  });
  return j?.ok === true;
}

// ---- XShell 上传导入 ----
export interface XshellUpload {
  name: string; // 相对路径（目录上传时已剥根目录）
  data: string; // base64 原始字节
}
export interface XshellScanResult {
  rows: XshellRowView[];
  skipped: number;
  failed: number;
}
export async function PreviewXshell(files: XshellUpload[]): Promise<XshellScanResult> {
  const j = await call<XshellScanResult>("POST", "/api/xshell/import", { files });
  return { rows: j?.rows ?? [], skipped: j?.skipped ?? 0, failed: j?.failed ?? 0 };
}
export function ImportXshell(reqs: HostReq[]): Promise<void> {
  return call("POST", "/api/xshell/commit", { hosts: reqs });
}

// ---- 用户管理（admin） ----
export async function GetUsers(): Promise<UserView[]> {
  const j = await call<{ users: UserView[] | null }>("GET", "/api/users");
  return j?.users ?? [];
}
export function CreateUser(username: string, password: string): Promise<void> {
  return call("POST", "/api/users", { username, password });
}
export function ResetUserPassword(username: string, password: string): Promise<void> {
  return call("PUT", `/api/users/${encodeURIComponent(username)}/password`, { password });
}
export function DeleteUser(username: string): Promise<void> {
  return call("DELETE", `/api/users/${encodeURIComponent(username)}`);
}
