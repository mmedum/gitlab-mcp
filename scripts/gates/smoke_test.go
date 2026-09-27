package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildSmokeFake builds the stand-in server once per test.
func buildSmokeFake(t *testing.T) string {
	t.Helper()
	bin := executable(filepath.Join(t.TempDir(), "smokefake"))
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/smokefake").CombinedOutput(); err != nil {
		t.Fatalf("build the fake: %v\n%s", err, out)
	}
	return bin
}

func TestSmokePassesACorrectServer(t *testing.T) {
	bin := buildSmokeFake(t)
	var out bytes.Buffer
	if err := runSmoke(&out, bin, smokeEnv(t.TempDir()), 2); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "smoke ok") || !strings.Contains(out.String(), "3 tools") {
		t.Errorf("out = %s", out.String())
	}
}

// Each mode breaks the fake in one way; the smoke must name that way.
func TestSmokeFailsEveryWay(t *testing.T) {
	bin := buildSmokeFake(t)
	cases := []struct{ mode, want string }{
		{"stray", "not JSON-RPC frames"},
		{"crash", "did not exit cleanly when the client hung up"},
		{"inflight", "hung up with a request in flight and the server exited"},
		{"dotted", `tool name "gitlab.search"`},
		{"noauth", "must be a tool error carrying [auth]"},
		{"rowrites", "read-only mode registered create_issue"},
		{"oldproto", `asked for 2025-11-25 and the server answered "2024-11-05"`},
		{"plus", "uses {+x}"},
		{"readopen", "must be an error carrying [auth]"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			env := append(smokeEnv(t.TempDir()), "SMOKE_FAKE="+tc.mode)
			err := runSmoke(&bytes.Buffer{}, bin, env, 2)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSmokeFloorFires(t *testing.T) {
	bin := buildSmokeFake(t)
	err := runSmoke(&bytes.Buffer{}, bin, smokeEnv(t.TempDir()), 10)
	if err == nil || !strings.Contains(err.Error(), "gave 3 tools and the floor is 10") {
		t.Errorf("err = %v", err)
	}
}

func TestSmokeReportsAMissingBinary(t *testing.T) {
	err := runSmoke(&bytes.Buffer{}, filepath.Join(t.TempDir(), "nothing-here"), nil, 1)
	if err == nil {
		t.Fatal("a missing binary passed")
	}
}
