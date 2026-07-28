package api

import "testing"

// A private repo's contents API returns download_url with a short-lived ?token=…
// embedded. Keeping it would cache a credential, hand it to the browser, and break
// installs once it expires — so it must be stripped (we use the PAT header instead).
func TestSanitizeGHDownloadURL(t *testing.T) {
	cases := map[string]string{
		"https://raw.githubusercontent.com/o/r/main/dir/x.yaml?token=ABC123": "https://raw.githubusercontent.com/o/r/main/dir/x.yaml",
		"https://raw.githubusercontent.com/o/r/main/x.yaml":                  "https://raw.githubusercontent.com/o/r/main/x.yaml",
		"": "",
	}
	for in, want := range cases {
		if got := sanitizeGHDownloadURL(in); got != want {
			t.Errorf("sanitizeGHDownloadURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// The sanitized URL must still parse into repo/path/ref for source tracking.
func TestSanitizedURLStillParsesAsSource(t *testing.T) {
	clean := sanitizeGHDownloadURL(
		"https://raw.githubusercontent.com/kristianwind/uruz/main/yggdrasil/uruz.yaml?token=XYZ")
	repo, path, ref := parseRawGithubSource(clean)
	if repo != "kristianwind/uruz" || path != "yggdrasil" || ref != "main" {
		t.Errorf("parseRawGithubSource(%q) = (%q,%q,%q)", clean, repo, path, ref)
	}
}

func TestGhTokenFingerprint(t *testing.T) {
	if got := ghTokenFingerprint(""); got != "anon" {
		t.Errorf("empty token fingerprint = %q, want \"anon\"", got)
	}
	a, b := ghTokenFingerprint("token-a"), ghTokenFingerprint("token-b")
	if a == b {
		t.Error("different tokens produced the same fingerprint — cache would leak across credentials")
	}
	if a != ghTokenFingerprint("token-a") {
		t.Error("fingerprint is not stable for the same token")
	}
	// It must not be the token itself.
	if a == "token-a" {
		t.Error("fingerprint leaks the raw token")
	}
}
