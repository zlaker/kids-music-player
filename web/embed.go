package web

import "embed"

//go:embed index.html app.js app.css alpine.min.js
var Files embed.FS
