package backup

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// The cloud targets are exercised against the emulators each provider's own
// SDKs test against, when they are running:
//
//	docker run -d -p 9000:9000 minio/minio server /data      YGG_TEST_S3=http://127.0.0.1:9000
//	docker run -d -p 10000:10000 mcr.microsoft.com/azure-storage/azurite azurite-blob --blobHost 0.0.0.0
//	                                                           YGG_TEST_AZURE=http://127.0.0.1:10000/devstoreaccount1
//	docker run -d -p 4443:4443 fsouza/fake-gcs-server -scheme http -public-host 127.0.0.1:4443
//	                                                           YGG_TEST_GCS=http://127.0.0.1:4443
//
// Each needs a bucket/container named "ygg-test" first. Without the variable
// the test is skipped; the unit tests below run everywhere.

// exerciseTarget runs a target through what a backup, a restore and retention
// do to it, with sizes on and around the chunk boundaries — the edges a
// streamed upload gets wrong.
func exerciseTarget(t *testing.T, tgt Target, chunk int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sizes := []int{0, 1, chunk - 1, chunk, chunk + 1, 2*chunk + 12345}
	stamp := time.Now().UTC().Format("150405.000000")
	for _, n := range sizes {
		data := make([]byte, n)
		rand.Read(data) //nolint:errcheck
		name := "srv-" + stamp + "/size-" + strconv.Itoa(n) + ".tar.gz"

		// A pipe, as runBackup hands it over: no length, no seeking.
		pr, pw := io.Pipe()
		go func() { _, err := pw.Write(data); pw.CloseWithError(err) }()
		got, err := tgt.Put(ctx, name, pr)
		if err != nil {
			t.Fatalf("put %d bytes: %v", n, err)
		}
		if got != int64(n) {
			t.Errorf("put %d bytes reported %d", n, got)
		}

		rc, err := tgt.Get(ctx, name)
		if err != nil {
			t.Fatalf("get %d bytes: %v", n, err)
		}
		back, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read back %d bytes: %v", n, err)
		}
		if sha256.Sum256(back) != sha256.Sum256(data) {
			t.Errorf("%d bytes: read back %d bytes that differ", n, len(back))
		}
		if err := tgt.Delete(ctx, name); err != nil {
			t.Errorf("delete: %v", err)
		}
		if rc, err := tgt.Get(ctx, name); err == nil {
			rc.Close()
			t.Errorf("%s still readable after delete", name)
		}
	}

	// List answers the folder, as the "Test" button and a local target do: files
	// at the top level, not the server folders under it.
	if _, err := tgt.Put(ctx, "top-"+stamp+".tar.gz", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := tgt.Put(ctx, "nested-"+stamp+"/a.tar.gz", strings.NewReader("y")); err != nil {
		t.Fatal(err)
	}
	objs, err := tgt.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var sawTop bool
	for _, o := range objs {
		if o.Name == "top-"+stamp+".tar.gz" {
			sawTop = true
			if o.Size != 1 || o.ModTime.IsZero() {
				t.Errorf("listed %+v", o)
			}
		}
		if strings.Contains(o.Name, "/") {
			t.Errorf("list returned nested object %q", o.Name)
		}
	}
	if !sawTop {
		t.Errorf("list (%d objects) misses the top-level archive", len(objs))
	}
	tgt.Delete(ctx, "top-"+stamp+".tar.gz")      //nolint:errcheck
	tgt.Delete(ctx, "nested-"+stamp+"/a.tar.gz") //nolint:errcheck
}

func TestS3Target(t *testing.T) {
	ep := os.Getenv("YGG_TEST_S3")
	if ep == "" {
		t.Skip("YGG_TEST_S3 not set")
	}
	tgt, err := Open(Config{Type: "s3", Host: ep, Bucket: "ygg-test", Path: "/panel/backups/",
		Username: "minioadmin", Password: "minioadmin", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	// S3 parts are at least 5 MiB, so the boundaries are tested at the real
	// part size only for the small cases; what matters is a stream of unknown
	// length going up and coming back whole.
	exerciseTarget(t, tgt, 1<<20)
}

func TestAzureTarget(t *testing.T) {
	ep := os.Getenv("YGG_TEST_AZURE")
	if ep == "" {
		t.Skip("YGG_TEST_AZURE not set")
	}
	// Azurite's well-known development account; public, not a secret.
	tgt, err := Open(Config{Type: "azure", Host: ep, Bucket: "ygg-test", Path: "panel",
		Username: "devstoreaccount1",
		Password: "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="})
	if err != nil {
		t.Fatal(err)
	}
	az := tgt.(*azureTarget)
	az.block = 256 << 10
	// Create the container through the target's own signed requests, which is
	// also the first check that Shared Key signing is right.
	if resp, err := az.do(context.Background(), "PUT", az.base, url.Values{"restype": {"container"}}, nil, nil); err == nil {
		resp.Body.Close()
	} else if !strings.Contains(err.Error(), "ContainerAlreadyExists") {
		t.Fatalf("create container: %v", err)
	}
	exerciseTarget(t, tgt, 256<<10)
}

func TestGCSTarget(t *testing.T) {
	ep := os.Getenv("YGG_TEST_GCS")
	if ep == "" {
		t.Skip("YGG_TEST_GCS not set")
	}
	// The emulator does not check tokens; the token exchange is tested below.
	tgt := &gcsTarget{api: ep, bucket: "ygg-test", prefix: cleanPrefix("panel"),
		token: func(context.Context) (string, error) { return "", nil }, hc: &http.Client{}, chunk: 256 << 10}
	exerciseTarget(t, tgt, 256<<10)
}

func TestCleanPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "/": "", ".": "", "backups": "backups/", "/backups/": "backups/",
		" a/b ": "a/b/", "a//b/": "a/b/", "../x": "x/",
	} {
		if got := cleanPrefix(in); got != want {
			t.Errorf("cleanPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSAS(t *testing.T) {
	for _, in := range []string{
		"sv=2022-11-02&ss=b&srt=co&sp=rwdlac&se=2027-01-01T00:00:00Z&sig=abc%3D",
		"?sv=2022-11-02&sp=rwdl&sig=abc",
		"https://acct.blob.core.windows.net/ygg?sv=2022-11-02&sp=rwdl&sig=abc",
	} {
		if q, ok := parseSAS(in); !ok || q.Get("sig") == "" {
			t.Errorf("parseSAS(%q) not recognised", in)
		}
	}
	// An access key is base64 and must not be read as a SAS.
	if _, ok := parseSAS("Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="); ok {
		t.Error("an access key was taken for a SAS token")
	}
}

func TestSplitEndpoint(t *testing.T) {
	for in, want := range map[string]struct {
		host   string
		secure bool
	}{
		"s3.amazonaws.com":                      {"s3.amazonaws.com", true},
		"https://abc.r2.cloudflarestorage.com/": {"abc.r2.cloudflarestorage.com", true},
		"http://192.168.1.10:9000":              {"192.168.1.10:9000", false},
		"s3.eu-central-003.backblazeb2.com":     {"s3.eu-central-003.backblazeb2.com", true},
	} {
		h, s, err := splitEndpoint(in)
		if err != nil || h != want.host || s != want.secure {
			t.Errorf("splitEndpoint(%q) = %q %v %v", in, h, s, err)
		}
	}
	for _, bad := range []string{"ftp://x", "https://s3.amazonaws.com/mybucket", "://"} {
		if _, _, err := splitEndpoint(bad); err == nil {
			t.Errorf("splitEndpoint(%q) accepted", bad)
		}
	}
}

func TestOpenRejectsIncompleteCloudConfigs(t *testing.T) {
	for _, c := range []Config{
		{Type: "s3", Username: "k", Password: "s"},                         // no bucket
		{Type: "s3", Bucket: "b", Username: "k"},                           // no secret
		{Type: "gcs", Bucket: "b", Password: `{"type":"authorized_user"}`}, // not a service account
		{Type: "gcs", Bucket: "b", Password: "not json"},
		{Type: "azure", Bucket: "c", Password: "a2V5"}, // no account
		{Type: "azure", Username: "acct", Bucket: "c", Password: "not base64 !!"},
	} {
		if _, err := Open(c); err == nil {
			t.Errorf("Open(%+v) accepted", c)
		}
	}
}

// The emulator ignores tokens, so the service-account exchange is checked
// against a fake token endpoint: what the panel signs, and that it reuses the
// token instead of asking for one per request.
func TestGCSServiceAccountToken(t *testing.T) {
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, pk)}))

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.ParseForm() //nolint:errcheck
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		claims := jwt.MapClaims{}
		if _, err := jwt.ParseWithClaims(r.Form.Get("assertion"), claims, func(*jwt.Token) (any, error) { return &pk.PublicKey, nil }); err != nil {
			t.Errorf("assertion does not verify with the key's public half: %v", err)
		}
		if claims["iss"] != "backup@proj.iam.gserviceaccount.com" || claims["scope"] != gcsScope || claims["aud"] != "http://"+r.Host+"/token" {
			t.Errorf("claims = %v", claims)
		}
		w.Write([]byte(`{"access_token":"tok-1","expires_in":3600}`)) //nolint:errcheck
	}))
	defer srv.Close()

	keyJSON, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "backup@proj.iam.gserviceaccount.com",
		"private_key": pemKey, "token_uri": srv.URL + "/token",
	})
	tgt, err := Open(Config{Type: "gcs", Bucket: "b", Password: string(keyJSON)})
	if err != nil {
		t.Fatal(err)
	}
	g := tgt.(*gcsTarget)
	for i := 0; i < 3; i++ {
		tok, err := g.token(context.Background())
		if err != nil || tok != "tok-1" {
			t.Fatalf("token = %q, %v", tok, err)
		}
	}
	if calls != 1 {
		t.Errorf("%d token requests for 3 uses, want 1", calls)
	}
}

func mustPKCS8(t *testing.T, k *rsa.PrivateKey) []byte {
	b, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
