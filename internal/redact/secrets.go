package redact

import "regexp"

// secretRule is one shape of secret a job log can carry. When keep is
// set, the first submatch is kept and only the rest is masked, so
// "Bearer " or "aws_secret_access_key=" still says what was there.
type secretRule struct {
	name string
	re   *regexp.Regexp
	keep bool
}

// secretRules are applied in order. Private key blocks go first because
// their bodies contain base64 that later rules could match piecemeal;
// JWTs before bearer headers, so a bearer JWT is one mask, not two.
//
// GitLab masks only the CI variables marked masked (§4.1.4); a token
// echoed by a script, or printed by a tool on failure, reaches the log
// as it is. These are the shapes that turn up that way.
var secretRules = []secretRule{
	{name: "private-key", re: regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----|\z)`)},
	{name: "gitlab-token", re: token},
	{name: "jwt", re: regexp.MustCompile(`\beyJ[0-9A-Za-z_\-]{8,}\.eyJ[0-9A-Za-z_\-]{8,}\.[0-9A-Za-z_\-]{8,}`)},
	{name: "aws-access-key", re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{name: "aws-secret-key", re: regexp.MustCompile(`(?i)(\baws_?secret_?access_?key["']?\s*[:=]\s*["']?)[0-9A-Za-z/+=]{40}`), keep: true},
	{name: "cloud-api-key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}`)},
	{name: "github-token", re: regexp.MustCompile(`\b(?:gh[pousr]_[0-9A-Za-z]{36,}|github_pat_[0-9A-Za-z_]{22,})`)},
	{name: "slack-token", re: regexp.MustCompile(`\bxox[abposr]-[0-9A-Za-z\-]{10,}`)},
	{name: "authorization", re: regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)[0-9A-Za-z\-._~+/]{16,}=*`), keep: true},
	{name: "url-password", re: regexp.MustCompile(`([a-z][a-z0-9+.\-]*://[^\s:/@]+:)[^\s@/]+@`), keep: true},
}

// MaskSecrets replaces every secret shape in a job log with
// [MASKED <kind>] and returns the masked text and how many it replaced.
// The count goes into the result, so a reader knows the log was altered
// and where to look.
func MaskSecrets(text string) (string, int) {
	n := 0
	for _, r := range secretRules {
		mark := "[MASKED " + r.name + "]"
		text = r.re.ReplaceAllStringFunc(text, func(m string) string {
			n++
			if !r.keep {
				return mark
			}
			prefix := r.re.FindStringSubmatch(m)[1]
			if r.name == "url-password" {
				return prefix + mark + "@"
			}
			return prefix + mark
		})
	}
	return text, n
}
