// Package tsv is the one reader for the record files the gates hold the
// code against — API verdicts, field verdicts, live-driver waivers,
// coverage exemptions — so they cannot disagree about blank lines,
// comments, a trailing TAB or a key listed twice.
package tsv

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Row is one record line: its fields, and the line it came from.
type Row struct {
	Fields []string
	Line   int
}

// Read reads a tab-separated record file. See Parse.
func Read(path string, cols int) ([]Row, []string) {
	f, err := os.Open(path) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil, []string{"cannot read " + path + ": " + err.Error()}
	}
	defer func() { _ = f.Close() }()
	return Parse(f, path, cols)
}

// Parse reads tab-separated records from r, skipping blank lines and
// lines starting with #. cols is how many fields a row must carry, and
// the first field is a key that must be unique. name is what problems
// are reported against.
//
// Problems are returned rather than raised, so a person editing the
// file sees every mistake at once.
func Parse(r io.Reader, name string, cols int) ([]Row, []string) {
	var rows []Row
	var problems []string
	seen := map[string]int{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; scanner.Scan(); n++ {
		// Spaces and a carriage return go; a trailing TAB does not,
		// because it is an empty last field, and "no reason given" is a
		// better message than "wrong number of columns".
		line := strings.TrimRight(scanner.Text(), " \r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != cols {
			problems = append(problems, fmt.Sprintf("%s:%d: want %d tab-separated columns, got %d",
				name, n, cols, len(fields)))
			continue
		}
		if first, dup := seen[fields[0]]; dup {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is listed twice (first on line %d)",
				name, n, fields[0], first))
		} else {
			seen[fields[0]] = n
		}
		rows = append(rows, Row{Fields: fields, Line: n})
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, name+": "+err.Error())
	}
	return rows, problems
}
