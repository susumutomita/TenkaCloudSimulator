// Package legal embeds redistribution notices with the executable.
package legal

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/*
var notices embed.FS

func Handler() http.Handler {
	files, _ := fs.Sub(notices, "assets")
	return http.FileServer(http.FS(files))
}
