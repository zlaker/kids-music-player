package web

import "embed"

//go:embed index.html app.js
var Files embed.FS
