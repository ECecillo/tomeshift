// Package main wires the epub-translator CLI process entrypoint.
//
// It contains bootstrap-only concerns:
//   - loading runtime config from environment
//   - adapting config into command execution contracts
//   - delegating command dispatch to internal/commandexec
//
// This package is not intended to be imported by other packages. Reusable
// command logic is exposed from internal/commandexec instead.
package main
