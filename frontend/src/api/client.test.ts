// client.test.ts REST 封装测试：mock fetch——{hosts} 解包、{error} 展开、
// 401 统一回调（登录除外：透传文案）。
import { afterEach, describe, expect, it, vi } from "vitest";
import { CreateUser, DeleteUser, GetHosts, GetUsers, Login, ResetUserPassword, SaveHost, setUnauthorizedHandler, TestHostDraft } from "./client";

function mockFetch(status: number, body: unknown) {
  const fn = vi.fn().mockResolvedValue(
    new Response(body === undefined ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    })
  );
  globalThis.fetch = fn as unknown as typeof fetch;
  return fn;
}

afterEach(() => setUnauthorizedHandler(null));

describe("client", () => {
  it("GetHosts 解包 {hosts} 并透传 null", async () => {
    const fn = mockFetch(200, { hosts: [{ id: "1", name: "a" }] });
    const hs = await GetHosts();
    expect(fn).toHaveBeenCalledWith("/api/hosts", { method: "GET" });
    expect(hs).toEqual([{ id: "1", name: "a" }]);
    mockFetch(200, { hosts: null });
    expect(await GetHosts()).toBeNull();
  });

  it("错误响应展开 {error} 字段", async () => {
    mockFetch(400, { error: "名称、地址、用户为必填" });
    await expect(
      SaveHost({ name: "", addr: "", user: "", tags: [], authMethod: "key" })
    ).rejects.toThrow("名称、地址、用户为必填");
  });

  it("401 触发未登录回调", async () => {
    const on401 = vi.fn();
    setUnauthorizedHandler(on401);
    mockFetch(401, { error: "未登录" });
    await expect(GetHosts()).rejects.toThrow("未登录");
    expect(on401).toHaveBeenCalled();
  });

  it("登录 401 透传服务端文案且不触发未登录回调", async () => {
    const on401 = vi.fn();
    setUnauthorizedHandler(on401);
    mockFetch(401, { error: "用户名或密码错误" });
    await expect(Login("admin", "bad")).rejects.toThrow("用户名或密码错误");
    expect(on401).not.toHaveBeenCalled();
  });

  it("非 JSON 错误体保留 HTTP 状态", async () => {
    const fn = vi.fn().mockResolvedValue(new Response("boom", { status: 500 }));
    globalThis.fetch = fn as unknown as typeof fetch;
    await expect(GetHosts()).rejects.toThrow("HTTP 500");
  });

  it("GetUsers 解包 {users}，缺省为空数组", async () => {
    mockFetch(200, { users: [{ username: "admin", role: "admin", createdAt: "t" }] });
    expect(await GetUsers()).toEqual([{ username: "admin", role: "admin", createdAt: "t" }]);
    mockFetch(200, {});
    expect(await GetUsers()).toEqual([]);
  });

  it("TestHostDraft POST /api/hosts/test 并解包 message", async () => {
    const fn = mockFetch(200, { message: "连通正常 · ubuntu 22.04" });
    const msg = await TestHostDraft({ name: "x", addr: "10.0.0.1", user: "root", tags: [], authMethod: "password" });
    expect(fn).toHaveBeenCalledWith("/api/hosts/test", expect.objectContaining({ method: "POST" }));
    expect(msg).toBe("连通正常 · ubuntu 22.04");
  });

  it("用户管理调用正确的方法与路径", async () => {
    // mockImplementation 每次新造 Response：body 流只能读一次，复用实例会
    // 在第二个调用报 "Body is unusable"
    const fn = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      )
    );
    globalThis.fetch = fn as unknown as typeof fetch;
    await CreateUser("bob", "bobpw123");
    expect(fn).toHaveBeenLastCalledWith("/api/users", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: "bob", password: "bobpw123" }),
    });
    await ResetUserPassword("bob", "newpw456");
    expect(fn).toHaveBeenLastCalledWith("/api/users/bob/password", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: "newpw456" }),
    });
    await DeleteUser("bob");
    expect(fn).toHaveBeenLastCalledWith("/api/users/bob", { method: "DELETE" });
  });
});
