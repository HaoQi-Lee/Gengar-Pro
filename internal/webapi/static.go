// 前端产物的静态托管（SPA fallback）。webapi 不依赖前端构建顺序、也
// 不持有 embed 声明（单测 static=nil / fstest.MapFS），由入口以 go:embed
// 的 fs.FS 注入 StaticHandler（embed 只能引用包内路径，故声明在模块根）。
package webapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// StaticHandler 用 fs.FS 构建带 SPA fallback 的静态托管。
// 静态优化（评审 ④-5）：dist 资产为 hashed 文件名（vite 默认）——
// 带 hash 的 assets/* 设 Cache-Control: immutable（浏览器永久缓存，
// 部署新版换 hash 自动失效）；index.html 与无后缀路径 no-cache
// （HTML 是引用新 hash 的入口，必须每次回源）。
func StaticHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if fi, err := fs.Stat(fsys, p); err != nil || fi.IsDir() {
			r.URL.Path = "/" // SPA fallback：未命中或目录（防目录列表泄露）
			p = "index.html"
		}
		// hashed 资产永久缓存；HTML 每次校验
		if strings.HasPrefix(p, "assets/") && path.Ext(p) != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}
