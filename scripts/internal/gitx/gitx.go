// Package gitx is the few git questions the gates and drivers ask, as
// one line of plumbing each. Anything that parses what git prints lives
// here, so two gates cannot read the same output two ways.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Output runs git in dir and returns what it wrote to stdout. A failure
// carries git's own stderr, trimmed.
func Output(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return string(out), nil
}

// Root is the top of the work tree dir is in.
func Root(dir string) (string, error) {
	out, err := Output(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.FromSlash(strings.TrimSpace(out)), nil
}

// Files lists what git would put in a commit from root: tracked files
// and untracked files that are not ignored, relative to root,
// slash-separated and sorted. A tracked file deleted from the work tree
// is left out, since there is nothing to read.
func Files(root string) ([]string, error) {
	return list(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
}

// Tracked lists the files in the index, relative to root, sorted, with
// the same deleted-file rule as Files.
func Tracked(root string) ([]string, error) {
	return list(root, "ls-files", "-z", "--cached")
}

func list(root string, args ...string) ([]string, error) {
	out, err := Output(root, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for name := range strings.SplitSeq(out, "\x00") {
		if name == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			continue
		}
		files = append(files, name)
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// MergeBase is the commit a pull request is measured from: the best
// common ancestor of base and head. The base SHA a pull request event
// records is stale in a stacked pull request; this is not.
func MergeBase(root, base, head string) (string, error) {
	out, err := Output(root, "merge-base", base, head)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if !isSHA(sha) {
		return "", fmt.Errorf("git merge-base %s %s printed %q, not a commit", base, head, sha)
	}
	return sha, nil
}

// Resolve turns a revision into a full commit SHA.
func Resolve(root, rev string) (string, error) {
	out, err := Output(root, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s is not a commit here: %w", rev, err)
	}
	return strings.TrimSpace(out), nil
}

// Show is a file's content at a revision.
func Show(root, rev, path string) ([]byte, error) {
	out, err := Output(root, "show", rev+":"+path)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// ChangedFiles lists the paths that differ between two revisions,
// sorted.
func ChangedFiles(root, from, to string) ([]string, error) {
	out, err := Output(root, "diff", "--name-only", "-z", from, to)
	if err != nil {
		return nil, err
	}
	var files []string
	for name := range strings.SplitSeq(out, "\x00") {
		if name != "" {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	return files, nil
}

// Messages are the full messages of the commits in from..to, newest
// first.
func Messages(root, from, to string) ([]string, error) {
	out, err := Output(root, "log", "-z", "--format=%B", from+".."+to)
	if err != nil {
		return nil, err
	}
	var msgs []string
	for m := range strings.SplitSeq(out, "\x00") {
		if strings.TrimSpace(m) != "" {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

// ErrNoTag is LatestTag finding no tag reachable from HEAD.
var ErrNoTag = errors.New("no tag reachable from HEAD")

// LatestTag is the newest tag reachable from HEAD.
func LatestTag(root string) (string, error) {
	cmd := exec.Command("git", "describe", "--tags", "--abbrev=0")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", ErrNoTag
		}
		return "", fmt.Errorf("git describe: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
