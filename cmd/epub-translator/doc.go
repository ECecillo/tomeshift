// Package main wires the epub-translator CLI process entrypoint.
//
// It is bootstrap only: read the configuration from the environment, hand
// dispatch to internal/commandexec, exit with the code it returns. The typed
// configuration itself lives in internal/config so the command layer and the
// entrypoint share one struct rather than a copy each.
//
// This package is not intended to be imported by other packages. Reusable
// command logic is exposed from internal/commandexec instead.
package main
