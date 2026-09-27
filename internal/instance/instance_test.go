package instance

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		raw     string
		want    string // String(); "" means an error is expected
		wantErr string // substring of the error
	}{
		{raw: "gitlab.example.com", want: "https://gitlab.example.com"},
		{raw: "  https://gitlab.example.com  ", want: "https://gitlab.example.com"},
		{raw: "https://gitlab.example.com/", want: "https://gitlab.example.com"},
		{raw: "https://gitlab.example.com/api/v4", want: "https://gitlab.example.com"},
		{raw: "https://gitlab.example.com/api/v4/", want: "https://gitlab.example.com"},
		{raw: "https://gitlab.example.com/API/V4", want: "https://gitlab.example.com"},
		{raw: "https://gitlab.example.com/gitlab", want: "https://gitlab.example.com/gitlab"},
		{raw: "https://gitlab.example.com//gitlab//", want: "https://gitlab.example.com/gitlab"},
		{raw: "https://gitlab.example.com/gitlab/api/v4", want: "https://gitlab.example.com/gitlab"},
		{raw: "gitlab.example.com:8443/gitlab", want: "https://gitlab.example.com:8443/gitlab"},
		{raw: "HTTPS://GitLab.Example.COM.", want: "https://gitlab.example.com"},
		{raw: "https://gitlab.example.com:443", want: "https://gitlab.example.com"},
		{raw: "http://127.0.0.1:3000", want: "http://127.0.0.1:3000"},
		{raw: "http://localhost:80/", want: "http://localhost"},
		{raw: "http://[::1]:8080", want: "http://[::1]:8080"},
		{raw: "https://[::1]", want: "https://[::1]"},
		{raw: "http://gitlab.example.com", wantErr: "http is refused"},
		{raw: "", wantErr: "empty"},
		{raw: "   ", wantErr: "empty"},
		{raw: "ftp://gitlab.example.com", wantErr: "must use https"},
		{raw: "https://user:s3cr3t-value@gitlab.example.com", wantErr: "credentials do not belong"},
		{raw: "https://gitlab.example.com/?private_token=x", wantErr: "query"},
		{raw: "https://gitlab.example.com/#top", wantErr: "fragment"},
		{raw: "https://gitlab.example.com/example-group/project/-/issues/1", wantErr: "page URL"},
		{raw: "https://gitlab.example.com/gitlab/../x", wantErr: `".."`},
		{raw: "https://gitlab.example.com/api/v4/projects", wantErr: "API endpoint"},
		{raw: "https://", wantErr: "no host"},
		{raw: "https://gitlab.example.com:99999", wantErr: "port"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := Parse(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Parse(%q) = %v, want error containing %q", tt.raw, got, tt.wantErr)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("error %v does not wrap ErrInvalid", err)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "s3cr3t") || strings.HasPrefix(err.Error(), "[") {
					t.Errorf("error %q echoes credentials or carries a class", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.raw, err)
			}
			if got.String() != tt.want {
				t.Errorf("Parse(%q) = %q, want %q", tt.raw, got.String(), tt.want)
			}
		})
	}
}

func TestAccessors(t *testing.T) {
	tests := []struct {
		raw                                    string
		scheme, host, hostname, path, web, api string
	}{
		{"gitlab.example.com", "https", "gitlab.example.com", "gitlab.example.com", "",
			"https://gitlab.example.com", "https://gitlab.example.com/api/v4"},
		{"https://gitlab.example.com:8443/gitlab/api/v4", "https", "gitlab.example.com:8443", "gitlab.example.com", "/gitlab",
			"https://gitlab.example.com:8443/gitlab", "https://gitlab.example.com:8443/gitlab/api/v4"},
		{"http://[::1]:8080", "http", "[::1]:8080", "::1", "",
			"http://[::1]:8080", "http://[::1]:8080/api/v4"},
	}
	for _, tt := range tests {
		i, err := Parse(tt.raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.raw, err)
		}
		if i.IsZero() {
			t.Errorf("%q: IsZero = true", tt.raw)
		}
		if i.Scheme() != tt.scheme || i.Host() != tt.host || i.Hostname() != tt.hostname || i.Path() != tt.path {
			t.Errorf("%q: got %q %q %q %q, want %q %q %q %q", tt.raw,
				i.Scheme(), i.Host(), i.Hostname(), i.Path(), tt.scheme, tt.host, tt.hostname, tt.path)
		}
		if got := i.WebBase().String(); got != tt.web {
			t.Errorf("%q: WebBase = %q, want %q", tt.raw, got, tt.web)
		}
		if got := i.APIRoot().String(); got != tt.api {
			t.Errorf("%q: APIRoot = %q, want %q", tt.raw, got, tt.api)
		}
	}
	if !(Instance{}).IsZero() {
		t.Error("zero Instance: IsZero = false")
	}
}

func TestWebBaseIsACopy(t *testing.T) {
	i, err := Parse("https://gitlab.example.com/gitlab")
	if err != nil {
		t.Fatal(err)
	}
	i.WebBase().Path = "/changed"
	i.APIRoot().Host = "other.invalid"
	if i.String() != "https://gitlab.example.com/gitlab" || i.APIRoot().String() != "https://gitlab.example.com/gitlab/api/v4" {
		t.Errorf("mutating a returned URL changed the instance: %s", i)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", s, err)
	}
	return u
}

func TestSameOrigin(t *testing.T) {
	i, err := Parse("https://gitlab.example.com/gitlab")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		u    string
		want bool
	}{
		{"https://gitlab.example.com/anything", true},
		{"https://GITLAB.example.com:443/x", true},
		{"HTTPS://gitlab.example.com./x", true},
		{"http://gitlab.example.com/x", false},
		{"https://gitlab.example.com:8443/x", false},
		{"https://other.invalid/x", false},
		{"https://gitlab.example.com.other.invalid/x", false},
	}
	for _, tt := range tests {
		if got := i.SameOrigin(mustURL(t, tt.u)); got != tt.want {
			t.Errorf("SameOrigin(%q) = %v, want %v", tt.u, got, tt.want)
		}
	}
	if i.SameOrigin(nil) {
		t.Error("SameOrigin(nil) = true")
	}
}

func TestUnderAPIRoot(t *testing.T) {
	i, err := Parse("https://gitlab.example.com/gitlab")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		u    string
		want bool
	}{
		{"https://gitlab.example.com/gitlab/api/v4", true},
		{"https://gitlab.example.com/gitlab/api/v4/projects?page=2", true},
		{"https://gitlab.example.com/gitlab/api/v4/projects/example-group%2Fproject/issues", true},
		{"https://gitlab.example.com:443/gitlab/api/v4/projects", true},
		{"https://gitlab.example.com/gitlab/api/v4x/projects", false},
		{"https://gitlab.example.com/api/v4/projects", false},
		{"https://gitlab.example.com/gitlab/api/v4/../../oauth/token", false},
		{"https://gitlab.example.com/gitlab/api/v4/%2e%2e/oauth", false},
		{"https://gitlab.example.com/gitlab/api/v4/projects/%2E%2E%2F%2E%2E%2Foauth", false},
		{"https://gitlab.example.com/gitlab/api/v4/./projects", false},
		{"https://user:pw@gitlab.example.com/gitlab/api/v4/projects", false},
		{"http://gitlab.example.com/gitlab/api/v4/projects", false},
		{"https://other.invalid/gitlab/api/v4/projects", false},
	}
	for _, tt := range tests {
		if got := i.UnderAPIRoot(mustURL(t, tt.u)); got != tt.want {
			t.Errorf("UnderAPIRoot(%q) = %v, want %v", tt.u, got, tt.want)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST.", true},
		{"127.0.0.1", true},
		{"127.10.20.30", true},
		{"::1", true},
		{"[::1]", true},
		{"128.0.0.1", false},
		{"gitlab.example.com", false},
		{"localhost.example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsLoopback(tt.host); got != tt.want {
			t.Errorf("IsLoopback(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}
