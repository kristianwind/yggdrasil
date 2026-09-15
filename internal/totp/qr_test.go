package totp

import (
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

func decodeQR(t *testing.T, uri string) image.Image {
	t.Helper()
	data, err := QRDataURI(uri)
	if err != nil {
		t.Fatalf("QRDataURI: %v", err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(data, prefix) {
		t.Fatalf("not a PNG data URI: %.40q", data)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(data, prefix))
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	img, err := png.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("the payload must be a real PNG, not just base64: %v", err)
	}
	return img
}

// The failure this guards against is the one you cannot see in a screenshot
// taken on a light background: a QR with transparent "white" modules renders as
// dark-on-dark inside the panel's card and simply will not scan. It has to carry
// its own opaque background.
func TestQRIsOpaqueBlackOnWhite(t *testing.T) {
	img := decodeQR(t, URI("JBSWY3DPEHPK3PXP", "kristian", "Yggdrasil"))
	b := img.Bounds()
	if b.Dx() < 20 || b.Dy() < 20 {
		t.Fatalf("suspiciously small: %v", b)
	}
	var dark, light int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a != 0xffff {
				t.Fatalf("pixel %d,%d is transparent — it would vanish on the dark card", x, y)
			}
			if r < 0x4000 && g < 0x4000 && bl < 0x4000 {
				dark++
			} else {
				light++
			}
		}
	}
	if dark == 0 || light == 0 {
		t.Fatalf("a QR needs both colours, got dark=%d light=%d", dark, light)
	}
}

// A stubbed or cached image would pass every structural check above while
// enrolling everyone with the same secret, which is the worst possible bug here:
// it looks fine and it is a total compromise.
func TestQRDiffersPerSecret(t *testing.T) {
	a, err := QRDataURI(URI("JBSWY3DPEHPK3PXP", "kristian", "Yggdrasil"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := QRDataURI(URI("KRSXG5BAMZQWY3DP", "kristian", "Yggdrasil"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two different secrets produced the same image")
	}
}

// Long account names are ordinary — an email address is one. The URI must still
// fit at correction level M rather than failing enrolment for that user alone.
func TestQREncodesARealisticLongURI(t *testing.T) {
	uri := URI("JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", "kristian.wind@some-rather-long-domain.example.com", "Yggdrasil")
	if len(uri) < 120 {
		t.Fatalf("the fixture stopped being long: %d chars", len(uri))
	}
	decodeQR(t, uri)
}
