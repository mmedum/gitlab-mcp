package instance

import (
	"cmp"
	"strconv"
	"strings"
)

// Version is a GitLab version as /api/v4/metadata reports it, such as
// "17.3.1-ee" or "19.5.0-pre".
type Version struct {
	Major, Minor, Patch int
	// Suffix follows the dash, without it: "pre", "ee", "rc1".
	Suffix string
	// Raw is the text as given, trimmed.
	Raw string
}

// ParseVersion reads "major.minor[.patch][-suffix]". A leading "v" is
// tolerated and a missing patch reads as 0.
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	body := strings.TrimPrefix(strings.TrimPrefix(raw, "v"), "V")
	nums, suffix, hasSuffix := strings.Cut(body, "-")
	if hasSuffix && suffix == "" {
		return Version{}, invalidf("version %q has an empty suffix", raw)
	}
	parts := strings.Split(nums, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, invalidf("version %q is not major.minor.patch", raw)
	}
	var n [3]int
	for k, p := range parts {
		v, ok := parseDigits(p)
		if !ok {
			return Version{}, invalidf("version %q is not major.minor.patch", raw)
		}
		n[k] = v
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2], Suffix: suffix, Raw: raw}, nil
}

// parseDigits accepts only ASCII digits, so "+1" and "" are refused.
func parseDigits(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	v, err := strconv.Atoi(s)
	return v, err == nil
}

// isPreRelease reports a suffix that comes before the release itself.
// "ee" names the edition, not a stage, so it is not one.
func (v Version) isPreRelease() bool {
	s := strings.ToLower(v.Suffix)
	return s == "pre" || strings.HasPrefix(s, "rc")
}

// Compare orders by major, minor and patch. At equal numbers a "pre" or
// "rc" build sorts before the release; any other suffix compares equal
// to none.
func (v Version) Compare(o Version) int {
	if c := cmp.Compare(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch vp, op := v.isPreRelease(), o.isPreRelease(); {
	case vp && !op:
		return -1
	case !vp && op:
		return 1
	}
	return 0
}

// AtLeast reports whether v is major.minor or newer, ignoring the
// suffix. A "19.5.0-pre" instance runs master on its way to 19.5, which
// already carries 19.5's routes, so it counts as 19.5. gitlab.com
// reports versions like that.
func (v Version) AtLeast(major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

// String is the canonical form, "17.3.1" or "17.3.1-ee".
func (v Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Suffix != "" {
		s += "-" + v.Suffix
	}
	return s
}

// Metadata is what the server keeps from /api/v4/metadata.
type Metadata struct {
	Version    Version
	Revision   string
	Enterprise bool
}

// NewMetadata parses the version metadata reports.
func NewMetadata(version, revision string, enterprise bool) (Metadata, error) {
	v, err := ParseVersion(version)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{Version: v, Revision: revision, Enterprise: enterprise}, nil
}

// AtLeast reports whether the instance is major.minor or newer.
func (m Metadata) AtLeast(major, minor int) bool { return m.Version.AtLeast(major, minor) }

// Edition is "Enterprise" or "Community".
func (m Metadata) Edition() string {
	if m.Enterprise {
		return "Enterprise"
	}
	return "Community"
}
