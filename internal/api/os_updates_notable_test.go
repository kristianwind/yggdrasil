package api

import "testing"

// "33 updates available, 28 security" does not tell an operator whether applying
// them costs anything. These two do, and both are facts rather than judgements —
// which is why they are a string match here and not a question for a model. A
// model would make a certainty probabilistic, and would only work on the installs
// that configured one.
func TestClassifyNotableFindsWhatCostsSomething(t *testing.T) {
	cases := []struct {
		name           string
		pkgs           []string
		kernel, docker bool
	}{
		{"a kernel image", []string{"linux-image-6.1.0-40-amd64", "curl"}, true, false},
		{"the kernel metapackage", []string{"linux-generic"}, true, false},
		{"headers count too — same reboot", []string{"linux-headers-6.1.0-40"}, true, false},
		{"docker from Docker's own repo", []string{"docker-ce", "docker-ce-cli"}, false, true},
		{"docker from the distro", []string{"docker.io"}, false, true},
		{"containerd restarts it just the same", []string{"containerd.io"}, false, true},
		{"both at once", []string{"linux-image-6.1.0-40-amd64", "docker-ce", "vim"}, true, true},
		{"ordinary packages are not news", []string{"curl", "vim", "openssl", "libc6"}, false, false},
		{"nothing pending", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, d, _ := classifyNotable(c.pkgs)
			if k != c.kernel {
				t.Errorf("kernel = %v, want %v", k, c.kernel)
			}
			if d != c.docker {
				t.Errorf("docker = %v, want %v", d, c.docker)
			}
		})
	}
}

// A package merely having "linux" or "docker" in its name is not a kernel or the
// daemon. Flagging "reboot needed" for docker-compose-plugin would train the
// operator to ignore the badge, which is the only thing the badge has going for it.
func TestClassifyNotableDoesNotOverreach(t *testing.T) {
	k, d, notable := classifyNotable([]string{
		"linux-libc-dev",        // headers for userspace, no reboot
		"util-linux",            // not the kernel at all
		"docker-compose-plugin", // a CLI plugin; restarts nothing
		"python3-docker",        // a library
	})
	if k {
		t.Error("linux-libc-dev / util-linux must not read as a kernel update")
	}
	if d {
		t.Error("docker-ce-cli and friends must not read as a Docker daemon update — " +
			"upgrading the client restarts nothing, and kw01 had exactly this pending")
	}
	if len(notable) != 0 {
		t.Errorf("nothing here is notable, got %v", notable)
	}
}
