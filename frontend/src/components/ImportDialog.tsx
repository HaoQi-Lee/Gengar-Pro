// frontend/src/components/ImportDialog.tsx XShell 会话导入（web 上传版）：
// 选目录（webkitdirectory，保留层级）或多选 .xsh 文件 → 读内容上传解析
// → 预览（重复行禁选）→ 勾选提交。密码不导入——toast 提示经 ✎ 补录。
// 行上的 name 即后端重名自动加后缀后的最终名，所见即所导。
import { useRef, useState } from "react";
import { ImportXshell, PreviewXshell } from "../api/client";
import type { HostReq, XshellRowView } from "../types";

interface Props {
  onClose: () => void;
  onImported: () => Promise<unknown>; // 刷新主机清单
  onToast: (text: string) => void;
}

interface UploadFile {
  name: string; // 相对路径（目录上传时剥根目录；文件上传为文件名）
  data: string; // base64
}

function bytesToB64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let bin = "";
  const chunk = 0x8000; // String.fromCharCode 参数长度上限
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(bin);
}

// relPath 归一：webkitRelativePath 形如 "Sessions/web/a.xsh"——多于一段时
// 剥首段（所选目录名），保留内部层级（目录→标签/folder 映射不变）。
function relPathOf(f: File): string {
  const rel = (f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name;
  const parts = rel.split("/");
  return parts.length > 1 ? parts.slice(1).join("/") : rel;
}

export default function ImportDialog({ onClose, onImported, onToast }: Props) {
  const [rows, setRows] = useState<XshellRowView[] | null>(null); // null=未解析
  const [skipped, setSkipped] = useState(0);
  const [failed, setFailed] = useState(0);
  const [checked, setChecked] = useState<Set<string>>(new Set()); // 按 source 勾选
  const [scanning, setScanning] = useState(false);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState("");
  const filesRef = useRef<UploadFile[]>([]);
  const runRef = useRef(0); // 解析代次：后一次选择自增接管，迟到的旧解析丢弃
  const dirInputRef = useRef<HTMLInputElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  // 读选中的 .xsh → base64（覆盖式：后一次选择替代前一次）。
  // input.value 先快照后置空：重选同一目录/文件也触发 change（value 未变时
  // 浏览器不派发）；run 守卫恢复旧版 cancelled 语义——异步完成序≠调用序。
  const handleFiles = async (list: FileList | null, input: HTMLInputElement) => {
    const picked = list === null ? [] : Array.from(list); // 快照 File 引用后再置空
    input.value = "";
    if (picked.length === 0) {
      return;
    }
    const run = ++runRef.current;
    setScanning(true);
    setError("");
    try {
      const uploads: UploadFile[] = [];
      for (const f of picked) {
        if (!f.name.toLowerCase().endsWith(".xsh")) {
          continue;
        }
        uploads.push({ name: relPathOf(f), data: bytesToB64(await f.arrayBuffer()) });
      }
      if (run !== runRef.current) {
        return; // 读取期间用户已重选：整体丢弃
      }
      if (uploads.length === 0) {
        setError("所选内容中没有 .xsh 会话文件");
        return;
      }
      const res = await PreviewXshell(uploads);
      if (run !== runRef.current) {
        return;
      }
      filesRef.current = uploads; // 与 rows 同步写入，消除失步窗口
      setRows(res.rows);
      setSkipped(res.skipped);
      setFailed(res.failed);
      // 默认勾选全部非重复行
      setChecked(new Set(res.rows.filter((r) => !r.duplicate).map((r) => r.source)));
    } catch (e) {
      if (run === runRef.current) {
        setRows(null);
        setError(String(e));
      }
    } finally {
      if (run === runRef.current) {
        setScanning(false);
      }
    }
  };

  const toggle = (r: XshellRowView) => {
    setChecked((prev) => {
      const next = new Set(prev);
      if (next.has(r.source)) {
        next.delete(r.source);
      } else {
        next.add(r.source);
      }
      return next;
    });
  };

  const allChecked =
    rows !== null && rows.length > 0 && rows.every((r) => r.duplicate || checked.has(r.source));
  const toggleAll = () => {
    if (rows === null) {
      return;
    }
    setChecked(
      allChecked ? new Set() : new Set(rows.filter((r) => !r.duplicate).map((r) => r.source))
    );
  };

  const doImport = async () => {
    if (rows === null || checked.size === 0) {
      return;
    }
    const bySource = new Map(rows.map((r) => [r.source, r]));
    const reqs: HostReq[] = [...checked]
      .map((src) => bySource.get(src))
      .filter((r): r is XshellRowView => r !== undefined)
      .map((r) => ({
        id: "",
        name: r.name, // 后端已处理重名（自动 -2/-3 后缀），原样提交
        addr: r.host,
        port: r.port,
        user: r.user,
        tags: r.tags ?? [],
        folder: r.folder ?? "", // Source 的目录部分：导入即归位目录树
        authMethod: r.authMethod === "key" ? "key" : "password",
        keyPath: "", // 评审 ①-5：UserKey 指向导入者本机文件，不带入；私钥需经 ✎ 上传
        password: "", // 结构导入：密码留空，经 ✎ 补录
        keyPassphrase: "",
      }));
    setImporting(true);
    setError("");
    const run = runRef.current; // 重扫迟到结果不得覆盖用户随后的新选择
    try {
      await ImportXshell(reqs);
    } catch (e) {
      setError(String(e));
      // 失败重试防重名 UNIQUE：重新解析刷新行名/重复标记
      if (filesRef.current.length > 0) {
        PreviewXshell(filesRef.current)
          .then((res) => {
            if (run !== runRef.current) {
              return;
            }
            setRows(res.rows);
            setSkipped(res.skipped);
            setFailed(res.failed);
            setChecked(new Set(res.rows.filter((r) => !r.duplicate).map((r) => r.source)));
          })
          .catch(() => {});
      }
      return;
    } finally {
      setImporting(false);
    }
    await onImported();
    onClose();
    onToast(`已导入 ${reqs.length} 台 · 密码/私钥需在 ✎ 补录后才能连通`);
  };

  return (
    <div className="overlay">
      <div className="dialog import-dialog">
        <div className="dialog-title">从 XShell 导入</div>
        <div className="import-bar">
          {/* 隐藏 input + 按钮触发：保持样式一致 */}
          <input
            ref={dirInputRef}
            type="file"
            multiple
            // @ts-expect-error webkitdirectory 非标准属性（Chromium/Firefox 支持）
            webkitdirectory=""
            className="hidden-input"
            onChange={(e) => void handleFiles(e.target.files, e.target)}
          />
          <input
            ref={fileInputRef}
            type="file"
            multiple
            accept=".xsh"
            className="hidden-input"
            onChange={(e) => void handleFiles(e.target.files, e.target)}
          />
          <button className="btn ghost" onClick={() => dirInputRef.current?.click()}>
            选择 Sessions 目录…
          </button>
          <button className="btn ghost" onClick={() => fileInputRef.current?.click()}>
            选择 .xsh 文件…
          </button>
        </div>
        {scanning && <div className="import-hint">解析中…</div>}
        {error && <div className="form-error">{error}</div>}
        {rows !== null && (
          <>
            <div className="import-hint">
              共 {rows.length} 台可导入 · 已存在 {rows.filter((r) => r.duplicate).length} 台（灰置）
              {skipped > 0 || failed > 0 ? ` · 跳过 ${skipped} · 解析失败 ${failed}` : ""}
            </div>
            <div className="import-table">
              <div className="import-row import-head">
                <label className="import-check">
                  <input type="checkbox" checked={allChecked} onChange={toggleAll} />
                </label>
                <span>名称</span>
                <span>地址</span>
                <span>用户</span>
                <span>标签</span>
              </div>
              {rows.map((r) => (
                <div
                  key={r.source}
                  className={r.duplicate ? "import-row dup" : "import-row"}
                  onClick={() => !r.duplicate && toggle(r)}
                >
                  <label className="import-check">
                    <input
                      type="checkbox"
                      disabled={r.duplicate}
                      checked={checked.has(r.source)}
                      onChange={() => toggle(r)}
                      onClick={(e) => e.stopPropagation()}
                    />
                  </label>
                  <span title={r.source}>{r.name}</span>
                  <span className="import-addr">
                    {r.host}:{r.port}
                    {r.authMethod === "key" ? " 🔑" : ""}
                  </span>
                  <span>{r.user}</span>
                  <span className="import-tags">
                    {(r.tags ?? []).length > 0 ? r.tags.join(", ") : "—"}
                  </span>
                </div>
              ))}
              {rows.length === 0 && <div className="import-hint">没有可导入的 SSH 会话</div>}
            </div>
          </>
        )}
        <div className="dialog-footer">
          <span className="hint">密码不导入，私钥也需上传（在 ✎ 中补录）</span>
          <button className="btn ghost" onClick={onClose}>
            取消
          </button>
          <button
            className="btn primary"
            disabled={importing || checked.size === 0}
            onClick={() => void doImport()}
          >
            导入 {checked.size} 台
          </button>
        </div>
      </div>
    </div>
  );
}
