package webui

import "embed"

// Dist is replaced by the Vite build before the Go binary is built.
//
//go:embed dist/*
var Dist embed.FS
