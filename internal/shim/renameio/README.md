# `internal/shim/renameio`

This directory exists only to satisfy a transitive dependency used by
`github.com/coder/hnsw`.

## Why this shim exists

`quant` depends on `github.com/coder/hnsw`, and `hnsw` v0.6.1 imports
`github.com/google/renameio` in its `SavedGraph.Save` implementation.

That upstream `renameio` module does not build on Windows for the API surface
`hnsw` uses (`renameio.TempFile`), which breaks cross-compilation during
release builds, for example:

```text
../../../go/pkg/mod/github.com/coder/hnsw@v0.6.1/encode.go:304:23:
undefined: renameio.TempFile
```

To keep the dependency graph stable and avoid vendoring or forking `hnsw`, this
repo replaces `github.com/google/renameio` with this minimal local module in
`go.mod`.

## What this shim implements

Only the small API surface required by `hnsw`:

- `TempFile`
- `PendingFile`
- `(*PendingFile).Cleanup`
- `(*PendingFile).CloseAtomicallyReplace`

This is intentionally not a full reimplementation of upstream `renameio`.

## When this can be removed

Delete this shim when any one of these is true:

1. `github.com/coder/hnsw` no longer depends on `github.com/google/renameio`.
2. `github.com/coder/hnsw` releases a version that builds on Windows without
   this override.
3. `quant` stops depending on `github.com/coder/hnsw`.
4. `quant` switches to a maintained fork of `hnsw` that fixes the Windows
   build path directly.

When removing it:

1. Delete the `replace github.com/google/renameio => ./internal/shim/renameio`
   line from the root `go.mod`.
2. Remove this directory.
3. Re-run:

```bash
go test ./...
GOOS=windows GOARCH=amd64 go build ./cmd/quant
```
