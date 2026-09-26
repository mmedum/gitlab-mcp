package credentials

import (
	"errors"
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// errDecoy is what the package-wide keyring answers. Anything in this
// package that reaches the OS keyring without a test asking for the
// in-memory one gets it, rather than a maintainer's real entries.
var errDecoy = errors.New("decoy keyring: a test reached the OS keyring")

// TestMain replaces the keyring for the whole package before any test
// runs. A test that writes to a maintainer's keyring is how a refresh
// token vanishes.
func TestMain(m *testing.M) {
	keyring.MockInitWithError(errDecoy)
	os.Exit(m.Run())
}

// useMockKeyring swaps in go-keyring's working in-memory store for one
// test and puts the decoy back after it.
func useMockKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	t.Cleanup(func() { keyring.MockInitWithError(errDecoy) })
}

// TestDecoyKeyringIsInPlace proves TestMain's replacement is active: the
// production backend answers with the decoy's error, which no real
// keyring returns.
func TestDecoyKeyringIsInPlace(t *testing.T) {
	b := OSKeyring()
	if _, err := b.Get(ServiceName, "decoy"); !errors.Is(err, errDecoy) {
		t.Fatalf("Get = %v; the OS keyring is not replaced", err)
	}
	if err := b.Set(ServiceName, "decoy", "x"); !errors.Is(err, errDecoy) {
		t.Fatalf("Set = %v; the OS keyring is not replaced", err)
	}
	if err := b.Delete(ServiceName, "decoy"); !errors.Is(err, errDecoy) {
		t.Fatalf("Delete = %v; the OS keyring is not replaced", err)
	}
}

// TestOSKeyringAgainstMock covers the wrapper methods without a Secret
// Service.
func TestOSKeyringAgainstMock(t *testing.T) {
	useMockKeyring(t)
	b := OSKeyring()
	if _, err := b.Get(ServiceName, "p"); !IsKeyringNotFound(err) {
		t.Fatalf("empty get: %v", err)
	}
	if err := b.Set(ServiceName, "p", "secret"); err != nil {
		t.Fatal(err)
	}
	if v, err := b.Get(ServiceName, "p"); err != nil || v != "secret" {
		t.Fatalf("get: %q %v", v, err)
	}
	if err := b.Delete(ServiceName, "p"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ServiceName, "p"); !IsKeyringNotFound(err) {
		t.Fatalf("second delete: %v", err)
	}
	if IsKeyringNotFound(errors.New("something else")) {
		t.Fatal("IsKeyringNotFound matched an unrelated error")
	}
}
