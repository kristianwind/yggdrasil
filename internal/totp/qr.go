package totp

import (
	"bytes"
	"encoding/base64"

	"rsc.io/qr"
)

// An otpauth URI is a 100-character string of base32 and query parameters. The
// enrolment screen used to print it and tell people to "scan the otpauth URI",
// which nobody can do: you scan a picture, not a line of text. The alternative
// on offer was typing a 32-character secret into a phone by hand.
//
// So the panel draws the picture. The QR is generated here rather than fetched
// from an endpoint of its own, and travels inside the same JSON response that
// already carries the secret — a separately addressable image URL would be one
// more place the secret exists, guarded by one more piece of authorisation.
//
// Correction level M, not L: this is displayed on screens of every quality and
// photographed at an angle, and the extra redundancy costs a few hundred bytes.
func QRDataURI(uri string) (string, error) {
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	b.WriteString("data:image/png;base64,")
	enc := base64.NewEncoder(base64.StdEncoding, &b)
	if _, err := enc.Write(code.PNG()); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}
