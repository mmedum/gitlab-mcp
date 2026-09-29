package service

import (
	"context"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/render"
)

// Asker puts a question to the person using the server before a write
// that ships work or deletes something for good (§4.12). Ask returns nil
// when the write may go ahead, and an error to return in its place
// otherwise; the tools layer's register installs one per call, for the
// tools that ask.
type Asker interface {
	Ask(ctx context.Context, q render.Question) error
	// Asks reports whether Ask would put a question or refuse, rather
	// than let the write go ahead unasked: the client can ask, or the
	// configuration requires it.
	Asks() bool
}

type askerKey struct{}

// WithAsker returns a context whose asking writes are put to a.
func WithAsker(ctx context.Context, a Asker) context.Context {
	return context.WithValue(ctx, askerKey{}, a)
}

// ask is the last step before an asking write, after every read and
// every other guard, so the question shows what the write would do and
// nothing is asked that a guard would refuse anyway. A dry run asks
// nothing. A write reached with no asker is refused: only a tool
// registered to ask may make one.
func ask(ctx context.Context, q render.Question) error {
	if gapi.IsDryRun(ctx) {
		return nil
	}
	a, ok := ctx.Value(askerKey{}).(Asker)
	if !ok {
		return gapi.Errf(gapi.ClassUnexpected, "this write has no way to ask the person, which is a defect in this server; nothing was sent")
	}
	return a.Ask(ctx, q)
}

// asks reports whether an asking write on ctx would reach the person.
// A write reads what only its question shows when it does, and not
// otherwise.
func asks(ctx context.Context) bool {
	if gapi.IsDryRun(ctx) {
		return false
	}
	a, ok := ctx.Value(askerKey{}).(Asker)
	return !ok || a.Asks()
}
