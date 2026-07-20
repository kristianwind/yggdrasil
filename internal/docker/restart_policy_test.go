package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

// The "start automatically after a reboot" setting maps to a Docker restart
// policy, and the mapping is the whole bug: on-failure (the old policy for every
// server) makes Docker's restart-manager bring the container back when the daemon
// starts — a host reboot — so a server with autostart OFF came back anyway,
// overriding the setting. Off must therefore be "no", the only policy Docker does
// not restart on daemon start. On stays on-failure: crash recovery plus reboot.
func TestRestartPolicyFor(t *testing.T) {
	if got := restartPolicyFor(true).Name; got != container.RestartPolicyOnFailure {
		t.Errorf("autostart on: policy = %q, want on-failure (crash recovery + reboot)", got)
	}
	off := restartPolicyFor(false)
	if off.Name != container.RestartPolicyDisabled {
		t.Errorf("autostart off: policy = %q, want %q — anything else lets Docker restart it on reboot",
			off.Name, container.RestartPolicyDisabled)
	}
	// The whole point: an autostart-off server must NOT get a policy Docker revives
	// at daemon start. on-failure, always and unless-stopped all do; only "no" doesn't.
	for _, revived := range []container.RestartPolicyMode{
		container.RestartPolicyOnFailure, container.RestartPolicyAlways, container.RestartPolicyUnlessStopped,
	} {
		if off.Name == revived {
			t.Errorf("autostart off got %q, which Docker restarts on reboot — the exact bug", off.Name)
		}
	}
}
