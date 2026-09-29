//go:build !evals

// The evals are behind a build tag so an ordinary `go build ./...` never
// reaches for a model. tasks.go carries no tag, so `go test
// ./scripts/evals` walks every prompt without one.
package main

import (
	"os"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/redact"
)

func main() {
	p := redact.NewPrinter(redact.NewRedactor(false))
	p.Fail("evals scores a model and is behind a build tag. Run it with:\n" +
		"  make evals-check     (no model, no key)\n" +
		"  make evals           (needs the claude CLI signed in)")
	os.Exit(2)
}
