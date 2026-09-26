package redact

import (
	"strings"
	"testing"
)

func TestMaskSecrets(t *testing.T) {
	var (
		awsKeyID  = "AKIA" + "ABCDEFGHIJKLMNOP"
		awsSecret = strings.Repeat("aB3/", 10)
		jwt       = "eyJ" + "hbGciOiJIUzI1" + ".eyJ" + "zdWIiOiIxMjM0" + "." + "c2lnbmF0dXJl"
		ghToken   = "ghp" + "_" + strings.Repeat("a1", 18)
		slack     = "xoxb" + "-" + "1234567890-abcdef"
		apiKey    = "AIza" + strings.Repeat("x", 35)
		keyBlock  = "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIEow\nIBAAK\n-----END RSA PRIVATE KEY-----"
	)
	tests := []struct {
		name  string
		in    string
		want  string
		count int
	}{
		{"gitlab token", "echo " + fakePAT, "echo [MASKED gitlab-token]", 1},
		{"aws key id", "key=" + awsKeyID + " ok", "key=[MASKED aws-access-key] ok", 1},
		{"aws secret keeps its name", "AWS_SECRET_ACCESS_KEY=" + awsSecret, "AWS_SECRET_ACCESS_KEY=[MASKED aws-secret-key]", 1},
		{"private key block", "before\n" + keyBlock + "\nafter", "before\n[MASKED private-key]\nafter", 1},
		{"unterminated private key", "x\n-----BEGIN " + "PRIVATE KEY-----\nMIIE\n", "x\n[MASKED private-key]", 1},
		{"bearer header", "Authorization: Bearer abcdefghijklmnop0123", "Authorization: Bearer [MASKED authorization]", 1},
		{"bearer jwt is one mask", "Authorization: Bearer " + jwt, "Authorization: Bearer [MASKED jwt]", 1},
		{"basic header", "Authorization: Basic YWxpY2U6aHVudGVyMjIy", "Authorization: Basic [MASKED authorization]", 1},
		{"github token", "t=" + ghToken, "t=[MASKED github-token]", 1},
		{"slack token", "t=" + slack, "t=[MASKED slack-token]", 1},
		{"cloud api key", "k=" + apiKey, "k=[MASKED cloud-api-key]", 1},
		{"password in a url", "git clone https://alice:hunter2@gitlab.example.com/x.git", "git clone https://alice:[MASKED url-password]@gitlab.example.com/x.git", 1},
		{"two of a kind", fakePAT + " " + fakePAT, "[MASKED gitlab-token] [MASKED gitlab-token]", 2},
		{"ordinary log", "Running with gitlab-runner\n$ make test\nok", "Running with gitlab-runner\n$ make test\nok", 0},
		{"a bearer word in prose", "the bearer of news", "the bearer of news", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := MaskSecrets(tt.in)
			if got != tt.want || n != tt.count {
				t.Errorf("MaskSecrets(%q) = %q, %d; want %q, %d", tt.in, got, n, tt.want, tt.count)
			}
			// Masked output passes through again unchanged.
			if again, n2 := MaskSecrets(got); again != got || n2 != 0 {
				t.Errorf("second pass = %q, %d", again, n2)
			}
		})
	}
}
