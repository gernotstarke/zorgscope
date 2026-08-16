// Package web embeds templates and static assets (ADR-0003, C-7).
package web

import "embed"

// FS contains templates/*.html and static/*.
//
//go:embed templates/*.html static/*
var FS embed.FS
