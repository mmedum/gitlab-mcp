package service

import (
	"errors"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/localimage"
)

// Each refusal internal/localimage gives carries the class the caller
// acts on (§6.5); a kind with no words here is a defect, and anything
// else passes through as it is.
func TestImageRefusalsCarryTheirClass(t *testing.T) {
	for kind, want := range map[localimage.Kind]gapi.Class{
		localimage.NotAbsolute:   gapi.ClassInvalid,
		localimage.DotDot:        gapi.ClassInvalid,
		localimage.NoDirectories: gapi.ClassBlocked,
		localimage.Outside:       gapi.ClassBlocked,
		localimage.Escapes:       gapi.ClassBlocked,
		localimage.NoDirectory:   gapi.ClassNotFound,
		localimage.Missing:       gapi.ClassNotFound,
		localimage.NotRegular:    gapi.ClassInvalid,
		localimage.Changed:       gapi.ClassConflict,
		localimage.TooLarge:      gapi.ClassInvalid,
		localimage.NotImage:      gapi.ClassInvalid,
		localimage.Unreadable:    gapi.ClassInvalid,
		localimage.Kind(99):      gapi.ClassUnexpected,
	} {
		if got, _ := gapi.ClassOf(imageRefused(&localimage.Error{Kind: kind})); got != want {
			t.Errorf("kind %d: [%s], want [%s]", kind, got, want)
		}
	}
	other := errors.New("not a refusal")
	if got := imageRefused(other); !errors.Is(got, other) {
		t.Errorf("another error became %v", got)
	}
}
