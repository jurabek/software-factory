package main

import (
	_ "embed"
	"io"
	"net/http"
)

//go:embed swagger.yaml
var swaggerSpec []byte

const swaggerUI = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Software Factory API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.32.15/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.32.15/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function () {
      SwaggerUIBundle({
        url: "/swagger.yaml",
        dom_id: "#swagger-ui",
        deepLinking: true,
        displayRequestDuration: true,
        persistAuthorization: true,
        tryItOutEnabled: true
      });
    };
  </script>
</body>
</html>`

type swaggerHandler struct {
	spec []byte
}

func newSwaggerHandler() swaggerHandler {
	return swaggerHandler{spec: swaggerSpec}
}

func (h swaggerHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /swagger.yaml", h.serveSpec)
	mux.HandleFunc("GET /docs", h.serveUI)
	mux.HandleFunc("GET /docs/", h.serveUI)
}

func (h swaggerHandler) serveSpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(h.spec)
}

func (swaggerHandler) serveUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, swaggerUI)
}
