package netapi

import (
	"embed"
	"io/fs"
	"net/http"
)

// webFS holds the embedded phone voice client (WI-62e19b). Served without
// auth so Safari can load the page; the page authenticates the WebSocket
// with ?token= because browsers cannot set Authorization on WS upgrades.
//
//go:embed web/*
var webFS embed.FS

func webFileServer() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// embed layout is fixed at compile time; panic makes a broken build
		// fail loudly rather than serving a half-empty handler.
		panic("netapi: embed web subtree: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}

// isPublicPath reports routes that must be reachable without a bearer token
// so the embedded client can load and pair. Stream and other API routes stay
// authenticated.
func isPublicPath(path string) bool {
	switch path {
	case "/", "/index.html", "/app.js", "/v1/pair":
		return true
	default:
		return false
	}
}
