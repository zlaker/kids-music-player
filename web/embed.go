package web

import "embed"

//go:embed index.html app.js app.css alpine.min.js manifest.webmanifest icon-192.png icon-512.png apple-touch-icon.png favicon-32.png
var Files embed.FS
