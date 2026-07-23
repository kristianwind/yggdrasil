package gameskill

import "testing"

func TestValidateExtraVolumeTarget(t *testing.T) {
	ok := []string{"/usr/src/paperless/media", "/usr/share/app/data", "/etc/letsencrypt", "/data/extra", "/var/lib/postgresql/data"}
	for _, p := range ok {
		if err := validateExtraVolumeTarget(p); err != nil {
			t.Errorf("expected %q to be allowed, got: %v", p, err)
		}
	}
	bad := []string{"/usr", "/usr/bin", "/usr/bin/env", "/usr/lib/x", "/usr/local/bin/y", "/bin", "/lib64/z", "/etc", "/", "/proc", "/root", "../escape", "/a/../b"}
	for _, p := range bad {
		if err := validateExtraVolumeTarget(p); err == nil {
			t.Errorf("expected %q to be rejected", p)
		}
	}
}
