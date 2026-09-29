package tools

import (
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
)

// Every error reaches the model as a tool result reading
// "[class] message", from the closed vocabulary of
// docs/architecture.md §6.5 (CLAUDE.md rule 16). A JSON-RPC error is
// for a caller that got the protocol wrong; GitLab refusing something is
// a result the model should read.

// hinted carries guidance a tool adds to an error from below. The class
// still comes from the wrapped error, so a hinted rate limit is still
// [rate_limited]; the hint is what makes the failure actionable when the
// upstream message alone would mislead.
type hinted struct {
	hint string
	err  error
}

func (h *hinted) Error() string { return h.hint + ": " + h.err.Error() }
func (h *hinted) Unwrap() error { return h.err }

// withHint wraps err with guidance the caller can act on.
func withHint(err error, format string, args ...any) error {
	return &hinted{hint: fmt.Sprintf(format, args...), err: err}
}

// refuse is a guard's refusal. It always names what it protects and the
// exact argument that would allow the call; a refusal the caller cannot
// act on is a bug.
func refuse(protecting, unlock string) error {
	return gapi.Errf(gapi.ClassBlocked, "%s. Pass %s to allow it", protecting, unlock)
}

// classOf is the class an error reports, unexpected when it carries
// none.
func classOf(err error) string {
	if c, ok := gapi.ClassOf(err); ok {
		return string(c)
	}
	return string(gapi.ClassUnexpected)
}

// message is an error's text without its class. The hint is checked
// before the client's error, because it exists precisely where that
// error alone would mislead.
func message(err error) string {
	var h *hinted
	if errors.As(err, &h) {
		return message(h.err) + "; " + h.hint
	}
	var e *gapi.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return gapi.AsError(err).Message
}

// errorText is the one place the "[class] message" format is written.
func errorText(err error) string { return "[" + classOf(err) + "] " + message(err) }

// errorResult is a failed call as the model sees it.
func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: errorText(err)}}}
}
