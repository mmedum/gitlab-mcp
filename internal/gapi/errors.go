package gapi

import (
	"errors"
	"fmt"
	"slices"

	"github.com/mmedum/gitlab-mcp/v4/internal/instance"
)

// Class is the closed error vocabulary (D-74).
//
// Closed means both directions, and `scripts/gates classes` asserts
// both: every class the code emits is in Classes, and every class in
// Classes is emitted somewhere or listed in the gate as planned.
type Class string

// The classes. One is kept apart from another only where it asks the
// caller for a different next step, and each comment says what that
// step is.
const (
	// ClassInvalid: malformed or under-specified arguments, a line not in
	// the diff, a label or user that does not exist, or too large. The
	// caller fixes the arguments.
	ClassInvalid Class = "invalid"
	// ClassNotFound: no such project, issue, merge request, file or job,
	// or the token cannot see it. GitLab answers 404 for a private
	// project, so the message always says both. The caller checks the id
	// and its access.
	ClassNotFound Class = "not_found"
	// ClassAuth: not signed in, the token expired or was revoked, or a
	// scope is missing, which the message names. The caller runs login.
	ClassAuth Class = "auth"
	// ClassForbidden: signed in, but the role or a project rule refuses.
	// The caller asks a maintainer.
	ClassForbidden Class = "forbidden"
	// ClassConflict: the state refuses it — a branch exists, a merge
	// request is already open, not mergeable. The caller reads the state
	// and reconsiders.
	ClassConflict Class = "conflict"
	// ClassStale: the witness moved since it was read: a file, a head sha
	// or an updated_at (D-36). Kept apart from conflict because the
	// caller re-reads and retries.
	ClassStale Class = "stale"
	// ClassAmbiguous: a milestone title matched more than one milestone in
	// the project and its groups. The caller renames one or sets the
	// milestone in GitLab.
	ClassAmbiguous Class = "ambiguous"
	// ClassBlocked: a guard refused what the API would have allowed: a
	// quick action, a protected branch, the write allow-list, or a review
	// that needs Ship. The caller passes the override the message names,
	// or stops.
	ClassBlocked Class = "blocked"
	// ClassRateLimited: an instance or application limit (D-75). The
	// caller waits as long as the message says.
	ClassRateLimited Class = "rate_limited"
	// ClassUnavailable: a transient upstream failure. The caller retries.
	ClassUnavailable Class = "unavailable"
	// ClassUnsupported: the account's tier or edition lacks the route, or
	// the API cannot do this. The message says which. The caller does not
	// retry.
	ClassUnsupported Class = "unsupported"
	// ClassAmbiguousOutcome: a create may or may not have happened
	// (D-34). Never retried: the caller reads the verdict and never
	// repeats the call blind.
	ClassAmbiguousOutcome Class = "ambiguous_outcome"
	// ClassUnexpected: anything unclassified, a 202 from note creation
	// included. The caller reports it.
	ClassUnexpected Class = "unexpected"
)

// Classes is the vocabulary. `scripts/gates classes` holds it to the
// constants: each listed once, and none left out.
var Classes = []Class{
	ClassInvalid, ClassNotFound, ClassAuth, ClassForbidden,
	ClassConflict, ClassStale, ClassAmbiguous, ClassBlocked,
	ClassRateLimited, ClassUnavailable, ClassUnsupported,
	ClassAmbiguousOutcome, ClassUnexpected,
}

// Valid reports whether c is in the vocabulary.
func (c Class) Valid() bool { return slices.Contains(Classes, c) }

// Retryable reports whether a failure of this class may clear by trying
// again unchanged. stale needs a re-read and ambiguous_outcome needs a
// look, so neither is.
func (c Class) Retryable() bool {
	return c == ClassUnavailable || c == ClassRateLimited
}

// Error is a classified failure. It reaches the model as a tool result,
// never as a protocol error.
type Error struct {
	Class Class
	// Status is the HTTP status, 0 for a failure that never got one.
	Status int
	// Message says what happened and what to do. It never carries a
	// request URL, a path or a query.
	Message string
	err     error
}

// Error renders "[class] message".
func (e *Error) Error() string { return fmt.Sprintf("[%s] %s", e.Class, e.Message) }

// Unwrap exposes the cause.
func (e *Error) Unwrap() error { return e.err }

// Errf builds a classified error.
func Errf(c Class, format string, args ...any) *Error {
	return &Error{Class: c, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds a classified error carrying a cause. The cause's text is
// not added to the message; say it in format if it is safe to show.
func Wrap(c Class, err error, format string, args ...any) *Error {
	return &Error{Class: c, Message: fmt.Sprintf(format, args...), err: err}
}

// ClassOf returns the class of err, and whether it carried one. An
// instance or URL error from package instance is invalid.
func ClassOf(err error) (Class, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Class, true
	}
	if errors.Is(err, instance.ErrInvalid) {
		return ClassInvalid, true
	}
	return "", false
}

// IsClass reports whether err carries class c.
func IsClass(err error, c Class) bool {
	got, ok := ClassOf(err)
	return ok && got == c
}

// AsError returns err as a classified *Error, classing anything that
// carries no class as unexpected. nil stays nil.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, instance.ErrInvalid) {
		return Wrap(ClassInvalid, err, "%s", err.Error())
	}
	return Wrap(ClassUnexpected, err, "%s", stripURL(err.Error()))
}
