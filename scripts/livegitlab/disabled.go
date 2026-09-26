//go:build !live

// The live driver is behind a build tag, so an ordinary `go build ./...`
// never reaches an instance or an account. plan.go carries no tag, so
// `go test ./scripts/livegitlab` walks the plan without either.
package main

import (
	"os"

	"github.com/mmedum/gitlab-mcp/scripts/internal/redact"
)

func main() {
	p := redact.NewPrinter(redact.NewRedactor(false))
	p.Fail("livegitlab drives gitlab.com and is behind a build tag. Run it with:\n" +
		"  make live LIVE_ARGS=\"-namespace example-group/scratch\"")
	os.Exit(2)
}
