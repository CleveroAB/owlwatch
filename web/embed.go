// Package web embeds the built frontend (web/dist) into the server binary.
// Build the frontend first: cd web && npm ci && npm run build.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
