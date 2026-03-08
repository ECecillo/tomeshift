// Package commandexec contains CLI command orchestration.
//
// It owns root command dispatch and maps command outcomes to standardized exit
// codes while remaining decoupled from concrete config implementations through
// injected loaders.
package commandexec
