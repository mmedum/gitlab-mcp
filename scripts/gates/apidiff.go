package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"time"
)

// apiDiff refetches the OpenAPI file at a GitLab release tag and rewrites
// the snapshot, saying what moved. It needs the network, so it is
// manual and in the release checklist; `make check` reads what it wrote.
//
// With no argument it resolves the newest vX.Y.Z-ee tag and pins it: the
// tag goes into the snapshot, so the next run can be asked for the same
// one. A network failure, a non-200 or a document below the operation
// floor returns an error and leaves the committed file untouched.
func apiDiff(out io.Writer, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 90 * time.Second}
	tag := ""
	if len(args) > 0 {
		tag = args[0]
	}
	return runAPIDiff(ctx, out, client, gitlabBase, snapshotFile, tag, snapshotFloor, time.Now())
}

func runAPIDiff(ctx context.Context, out io.Writer, client *http.Client, base, path, tag string, floor int, now time.Time) error {
	if tag == "" {
		t, err := newestStableTag(ctx, client, base)
		if err != nil {
			return fmt.Errorf("resolve the newest release tag: %w", err)
		}
		tag = t
	}
	if !stableTag.MatchString(tag) {
		return fmt.Errorf("%q is not a GitLab release tag of the form v19.4.1-ee", tag)
	}
	fresh, err := fetchSnapshot(ctx, client, base, tag, floor, now)
	if err != nil {
		return fmt.Errorf("%w; %s is untouched", err, path)
	}

	var old apiSnapshot
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // a path this repository owns
		if err := json.Unmarshal(raw, &old); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	data, err := encodeSnapshot(fresh)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return err
	}
	added, gone, changed := diffSnapshots(old, fresh)
	for _, k := range added {
		_, _ = fmt.Fprintln(out, "NEW     "+k)
	}
	for _, k := range gone {
		_, _ = fmt.Fprintln(out, "GONE    "+k)
	}
	for _, k := range changed {
		_, _ = fmt.Fprintln(out, "CHANGED "+k)
	}
	_, _ = fmt.Fprintf(out, "%s: %d operations at %s (was %s), sha256 %s; %d new, %d gone, %d changed.\n",
		path, len(fresh.Operations), tag, orNone(old.Tag), fresh.SHA256[:12], len(added), len(gone), len(changed))
	if len(added)+len(gone)+len(changed) > 0 {
		_, _ = fmt.Fprintln(out, "`make api-coverage` and `make api-fields` now hold testdata/api-coverage.tsv and "+
			"testdata/api-fields.tsv to it; a new operation fails until it has a verdict.")
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// diffSnapshots names the operations added, gone, and changed in any
// recorded way — a parameter, a field, a lifecycle.
func diffSnapshots(old, fresh apiSnapshot) (added, gone, changed []string) {
	index := func(s apiSnapshot) map[string]apiOperation {
		m := map[string]apiOperation{}
		for _, op := range s.Operations {
			m[op.key()] = op
		}
		return m
	}
	before, after := index(old), index(fresh)
	for k, op := range after {
		was, ok := before[k]
		switch {
		case !ok:
			added = append(added, k)
		default:
			a, _ := json.Marshal(was)
			b, _ := json.Marshal(op)
			if string(a) != string(b) {
				changed = append(changed, k)
			}
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			gone = append(gone, k)
		}
	}
	slices.Sort(added)
	slices.Sort(gone)
	slices.Sort(changed)
	return added, gone, changed
}
