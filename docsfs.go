// Package zorgscope exists for one reason: to embed the project's documentation.
//
// //go:embed cannot reach outside the directory of the package that declares it, and docs/ lives
// at the repository root. A package here is therefore the only way to serve docs/ from the binary
// while keeping the Markdown in exactly one place — no sync target, no second copy that can drift
// from the first. internal/web imports this and renders it at /docs (FR-7.1).
//
// Nothing else belongs in this package. It holds no logic, so importing it costs a caller nothing
// but the bytes, and the architecture the depguard rules describe stays a matter of internal/.
package zorgscope

import "embed"

// DocsFS holds the three documentation categories the site publishes, under their repository
// paths: docs/requirements, docs/decisions and docs/concepts.
//
// The pattern names those three directories rather than docs/ as a whole, which is a decision
// about what a visitor may read, not about binary size. /docs is unauthenticated (FR-7.1 AC3), so
// everything reachable through it is public: docs/superpowers holds the working plans and
// handovers of the build, which are notes to ourselves rather than documentation of the system,
// and docs/logo holds two megabytes of source JPEGs that no page ever shows. Embedding the three
// published categories means an unlisted document cannot become reachable by accident — the set of
// files in the binary is already the set of files the site is allowed to serve.
//
// Widening the patterns is therefore a security change, not a build detail, and it is the change
// this file is most likely to suffer: "all:docs" or a bare "docs" looks tidier, passes every test
// about rendering, and ships the working plans and the logo sources to an unauthenticated page.
// internal/web's TestOnlyTheThreePublishedCategoriesAreEmbedded walks this file system and fails on
// anything outside the three directories, so that edit cannot pass in silence. It is also what the
// #nosec G203 in internal/web/docs.go leans on: the rendered Markdown may skip escaping because
// every byte of it is repository content named right here.
//
//go:embed docs/requirements docs/decisions docs/concepts
var DocsFS embed.FS
