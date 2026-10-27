// Package docs 提供 OpenAPI 文档与 Swagger UI（官方 contract DOC-001）。
//
// 文档本体是**静态 JSON**（openapi.json），从官方 /week05/openapi.json 裁剪出
// 第 5 课范围，逐字保留原契约 —— 这样"Bearer 令牌访问接口与 Swagger 里写的一致"
// （任务清单第一板块第 4 条）是天然成立的，不需要再靠代码注解同步。
//
// Swagger UI 用 CDN 版本；若离线环境需要内嵌，可把静态资源放进 assets/ 再改这个页面。
package docs

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openapiJSON []byte

// ServeOpenAPI 返回 OpenAPI 文档本体。
func ServeOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(openapiJSON)
}

// swaggerHTML 是 Swagger UI 页面：从 CDN 加载 swagger-ui-dist，指向本机 /openapi.json。
const swaggerHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>CampusClaw API · Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  <style>body{margin:0;background:#fafafa}#swagger-ui{padding:8px 0}</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({
        url: "/openapi.json",
        dom_id: "#swagger-ui",
        deepLinking: true,
        persistAuthorization: true,
        withCredentials: false,
      });
    };
  </script>
</body>
</html>`

// ServeSwaggerUI 返回 Swagger UI 页面。
func ServeSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerHTML))
}
