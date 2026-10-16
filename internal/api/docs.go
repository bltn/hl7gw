package api

import (
	_ "embed"
	"net/http"
)

//go:embed assets/docs.html
var docsHTML []byte

//go:embed assets/redoc.standalone.js
var redocJS []byte

func asset(contentType string, body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	})
}
