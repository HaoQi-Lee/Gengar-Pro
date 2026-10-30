// folders.test.ts 目录树纯函数测试：分组/排序/嵌套/子项计数。
import { describe, expect, it } from "vitest";
import { allFolderPaths, buildTree } from "./folders";
import type { FolderNode } from "./folders";
import type { HostView } from "./types";

const h = (name: string, folder: string): HostView => ({
  id: name, name, addr: "10.0.0.1", port: 22, user: "root", tags: [], os: "",
  folder, status: "unknown", authMethod: "key", keyPath: "", passwordRef: "",
});

// FolderNode 的 host 分支无 children：测试统一经 kids 取子级，省去逐处收窄
const kids = (n: FolderNode | undefined): FolderNode[] =>
  n !== undefined && n.kind === "folder" ? n.children : [];

describe("buildTree", () => {
  it("根主机在前，目录按名排序，目录内保持传入序", () => {
    const tree = buildTree([h("z-root", ""), h("b", "生产"), h("a", "生产"), h("x", "alpha")]);
    expect(tree.map((n) => n.kind)).toEqual(["host", "folder", "folder"]);
    expect(tree[0]).toMatchObject({ kind: "host", name: "z-root" });
    expect(tree[1]).toMatchObject({ kind: "folder", name: "alpha" });
    expect(tree[2]).toMatchObject({ kind: "folder", name: "生产" });
    const prod = kids(tree[2]);
    expect(prod.map((n) => n.name)).toEqual(["b", "a"]);
  });
  it("嵌套路径构造中间目录", () => {
    const tree = buildTree([h("web-01", "生产/web")]);
    expect(tree).toHaveLength(1);
    expect(tree[0]).toMatchObject({ kind: "folder", name: "生产", depth: 0 });
    const web = kids(tree[0])[0];
    expect(web).toMatchObject({ kind: "folder", name: "web", depth: 1 });
    expect(kids(web)[0]).toMatchObject({ kind: "host", name: "web-01" });
  });
  it("同层主机排在子目录前", () => {
    const tree = buildTree([h("direct", "生产"), h("nested", "生产/web")]);
    const prod = kids(tree[0]);
    expect(prod.map((n) => n.kind)).toEqual(["host", "folder"]);
  });
  it("递归子项计数", () => {
    const tree = buildTree([
      h("a", "生产/web"),
      h("b", "生产/db"),
      h("c", "生产/db"),
    ]);
    expect(tree[0]).toMatchObject({ kind: "folder", name: "生产", hostCount: 3 });
    const sub = kids(tree[0]);
    expect(sub.map((n) => n.name)).toEqual(["db", "web"]); // 子目录同样按名排序
    expect(sub[1]).toMatchObject({ kind: "folder", name: "web", hostCount: 1 });
  });
});

describe("allFolderPaths", () => {
  it("列出全部目录路径（含中间目录）", () => {
    const paths = allFolderPaths([h("a", "生产/web"), h("b", "生产/db"), h("c", "")]);
    expect(paths).toEqual(["生产", "生产/web", "生产/db"]);
  });
});
