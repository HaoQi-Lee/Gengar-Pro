package webapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestStaticHandler(t *testing.T) {
	const indexHTML = "<html>gengar-index</html>"
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte(indexHTML)},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := StaticHandler(fsys)

	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr
	}

	// 根路径 → index.html
	if rr := get("/"); rr.Code != http.StatusOK || rr.Body.String() != indexHTML {
		t.Fatalf("GET / = %d %q; want 200 %q", rr.Code, rr.Body.String(), indexHTML)
	}
	// 未命中路径 → SPA fallback 回 index.html（子路径刷新场景）
	if rr := get("/hosts/web"); rr.Code != http.StatusOK || rr.Body.String() != indexHTML {
		t.Fatalf("GET /hosts/web = %d %q; want 200 %q", rr.Code, rr.Body.String(), indexHTML)
	}
	// 真实文件命中 → 原内容
	if rr := get("/assets/app.js"); rr.Code != http.StatusOK || rr.Body.String() != "console.log(1)" {
		t.Fatalf("GET /assets/app.js = %d %q; want 200 console.log(1)", rr.Code, rr.Body.String())
	}
	// 目录路径 → 回退 index.html，不列目录（带/走 fs.Stat 无效路径分支，
	// 不带/走 IsDir 分支，两条都必须落到 fallback）
	for _, p := range []string{"/assets/", "/assets"} {
		if rr := get(p); rr.Code != http.StatusOK || rr.Body.String() != indexHTML {
			t.Fatalf("GET %s = %d %q; want 200 %q（不列目录）", p, rr.Code, rr.Body.String(), indexHTML)
		}
	}
}
