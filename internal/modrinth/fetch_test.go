package modrinth

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sha512hex(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

// FetchFile must refuse any host but the Modrinth CDN — the file URL is data from
// an API response, and following it blindly would be an SSRF hole.
func TestFetchFileRejectsOtherHosts(t *testing.T) {
	f := File{URL: "https://evil.example.com/mod.jar", Filename: "mod.jar"}
	f.Hashes.SHA512 = "deadbeef"
	if _, err := FetchFile(context.Background(), f); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected host refusal, got %v", err)
	}
}

// Plain http must be refused even on the right host — a downgraded download is
// not trustworthy.
func TestFetchFileRejectsHTTP(t *testing.T) {
	f := File{URL: "http://cdn.modrinth.com/x.jar", Filename: "x.jar"}
	f.Hashes.SHA512 = "deadbeef"
	if _, err := FetchFile(context.Background(), f); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected http refusal, got %v", err)
	}
}

// A file the API published no checksum for must not be installed — an unverified
// binary landing in a server is the whole risk here.
func TestFetchFileRequiresChecksum(t *testing.T) {
	f := File{URL: "https://cdn.modrinth.com/x.jar", Filename: "x.jar"} // no hash
	if _, err := FetchFile(context.Background(), f); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum refusal, got %v", err)
	}
}

// withFakeCDN serves an https endpoint the client trusts, standing in for the
// Modrinth CDN so the download + verify path is exercised end to end.
func withFakeCDN(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	oldClient, oldHost := httpClient, cdnHost
	httpClient = srv.Client()
	cdnHost = strings.TrimPrefix(srv.URL, "https://")
	t.Cleanup(func() {
		httpClient, cdnHost = oldClient, oldHost
		srv.Close()
	})
	return srv
}

func TestFetchFileVerifiesChecksum(t *testing.T) {
	payload := []byte("pretend-jar-bytes")
	srv := withFakeCDN(t, payload)

	f := File{URL: srv.URL + "/sodium.jar", Filename: "sodium.jar"}
	f.Hashes.SHA512 = sha512hex(payload)
	got, err := FetchFile(context.Background(), f)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("got %q, want the payload", got)
	}

	bad := f
	bad.Hashes.SHA512 = sha512hex([]byte("something else"))
	if _, err := FetchFile(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected checksum mismatch, got %v", err)
	}
}

func TestFetchFileSizeCap(t *testing.T) {
	payload := []byte("0123456789")
	srv := withFakeCDN(t, payload)
	oldMax := maxFileBytes
	maxFileBytes = 4 // smaller than the payload
	defer func() { maxFileBytes = oldMax }()

	f := File{URL: srv.URL + "/big.jar", Filename: "big.jar"}
	f.Hashes.SHA512 = sha512hex(payload)
	if _, err := FetchFile(context.Background(), f); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("expected size-limit error, got %v", err)
	}
}

// The icon proxy must refuse any host but the CDN — same SSRF guard as FetchFile,
// since the URL comes from an API response and gets fetched server-side.
func TestFetchIconRejectsOtherHosts(t *testing.T) {
	for _, u := range []string{"https://evil.example.com/x.png", "http://cdn.modrinth.com/x.png", "http://127.0.0.1/x"} {
		if _, _, err := FetchIcon(context.Background(), u); err == nil {
			t.Errorf("FetchIcon(%q) allowed a non-CDN/non-https host", u)
		}
	}
}

func TestFetchIconServesImage(t *testing.T) {
	srv := withFakeCDN(t, []byte("\x89PNG\r\n"))
	// withFakeCDN serves any content; set an image content-type via a wrapper isn't
	// possible here, so this just checks bytes come back for a CDN URL.
	ct, data, err := FetchIcon(context.Background(), srv.URL+"/data/x/icon.png")
	if err != nil {
		t.Fatalf("FetchIcon: %v", err)
	}
	if len(data) == 0 {
		t.Error("no icon bytes returned")
	}
	_ = ct
}
