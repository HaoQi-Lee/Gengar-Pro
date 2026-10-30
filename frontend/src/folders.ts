// folders.ts 侧栏目录树纯函数：按 HostView.folder（斜杠路径）构造有序树。
// 根主机在前、目录按名排序、目录内主机在前且保持传入序。
import type { HostView } from "./types";

export type FolderNode =
  | { kind: "host"; host: HostView; name: string; depth: number }
  | {
      kind: "folder";
      name: string;
      path: string;
      depth: number;
      children: FolderNode[];
      hostCount: number;
    };

type FolderBranch = Extract<FolderNode, { kind: "folder" }>;

// 目录名排序：码点序（拉丁目录排在中日韩目录前）。不用 localeCompare——
// ICU 的 zh 排序把汉字排在校拉丁前面，且无 locale 时随系统区域漂移，皆不可复现。
const byName = (a: FolderBranch, b: FolderBranch): number =>
  a.name < b.name ? -1 : a.name > b.name ? 1 : 0;

// splitFolder 把 folder 字符串拆为段（后端已规范化，这里只做防御性过滤）
function splitFolder(folder: string): string[] {
  return (folder ?? "")
    .split("/")
    .map((s) => s.trim())
    .filter((s) => s !== "" && s !== ".");
}

export function buildTree(hosts: HostView[]): FolderNode[] {
  // path → 该目录直挂的主机（保持传入序）
  const hostsByPath = new Map<string, HostView[]>();
  for (const h of hosts) {
    const key = splitFolder(h.folder ?? "").join("/");
    const list = hostsByPath.get(key);
    if (list) {
      list.push(h);
    } else {
      hostsByPath.set(key, [h]);
    }
  }
  // path → 目录节点；ensure 递归构造中间目录
  const folders = new Map<string, FolderBranch>();
  const ensure = (path: string): FolderBranch => {
    const hit = folders.get(path);
    if (hit) {
      return hit;
    }
    const segs = splitFolder(path);
    const node: FolderBranch = {
      kind: "folder",
      name: segs[segs.length - 1],
      path,
      depth: segs.length - 1,
      children: [],
      hostCount: 0,
    };
    folders.set(path, node);
    if (segs.length > 1) {
      ensure(segs.slice(0, -1).join("/")).children.push(node);
    }
    return node;
  };
  for (const path of [...hostsByPath.keys()].sort()) {
    if (path !== "") {
      ensure(path);
    }
  }
  // 顶层：根主机在前，根目录按名排序
  const out: FolderNode[] = (hostsByPath.get("") ?? []).map((h) => ({
    kind: "host" as const,
    host: h,
    name: h.name,
    depth: 0,
  }));
  const rootFolders = [...folders.values()].filter((f) => f.depth === 0).sort(byName);
  out.push(...rootFolders);
  // 每个目录：直挂主机在前、子目录按名排序；自底向上累加计数
  const tally = (n: FolderBranch): void => {
    const myHosts = (hostsByPath.get(n.path) ?? []).map((h) => ({
      kind: "host" as const,
      host: h,
      name: h.name,
      depth: n.depth + 1,
    }));
    n.children = [
      ...myHosts,
      ...n.children
        .filter((c): c is FolderBranch => c.kind === "folder")
        .sort(byName),
    ];
    for (const c of n.children) {
      if (c.kind === "host") {
        n.hostCount += 1;
      } else {
        tally(c);
        n.hostCount += c.hostCount;
      }
    }
  };
  rootFolders.forEach(tally);
  return out;
}

/** 全部目录路径（含中间目录）——展开态初始化为全展开用。 */
export function allFolderPaths(hosts: HostView[]): string[] {
  const set = new Set<string>();
  for (const h of hosts) {
    const segs = splitFolder(h.folder ?? "");
    for (let i = 1; i <= segs.length; i++) {
      set.add(segs.slice(0, i).join("/"));
    }
  }
  return [...set];
}
