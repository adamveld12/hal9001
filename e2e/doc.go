// Package e2e is the API-driven end-to-end suite of REQUIREMENTS §12 scenario
// 20. Every test starts the real daemon in-process (daemon.Run on
// 127.0.0.1:0, a temp home seeded from content/, ACP runtimes pointing at the
// built cmd/fake-acp-agent with a per-test script) and drives it only through
// its public REST API over real HTTP, or through the hal9001 CLI, which is a
// thin client of the same API. The package has no non-test code.
//
// The suite takes tens of seconds, so every test skips under `go test -short`
// (pre-commit); pre-push and `mise run e2e` run it in full, also with -race.
package e2e
