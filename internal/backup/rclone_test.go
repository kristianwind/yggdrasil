package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

type scriptedRclone struct {
	answers []string // stdout per call
	calls   [][]string
}

func (s *scriptedRclone) RunRclone(_ context.Context, conf []byte, argv []string, _ io.Reader, stdout io.Writer) ([]byte, string, error) {
	s.calls = append(s.calls, argv)
	i := len(s.calls) - 1
	if i < len(s.answers) {
		stdout.Write([]byte(s.answers[i])) //nolint:errcheck
	}
	return []byte("[ygg]\ntype = jottacloud\ntoken = {}\n"), "", nil
}

// rclone's config asks follow-up questions (Jottacloud: which device and
// mountpoint); each is answered with rclone's own default, then the loop ends.
func TestRcloneSetupAnswersFollowUpsWithDefaults(t *testing.T) {
	run := &scriptedRclone{answers: []string{
		`{"State":"choose_device","Option":{"Name":"config_device","Default":"Jotta"},"Error":""}`,
		`{"State":"choose_mp","Option":{"Name":"config_mountpoint","Default":"","Examples":[{"Value":"Archive"}]},"Error":""}`,
		`{"State":"","Option":null,"Error":""}`,
	}}
	conf, err := RcloneSetup(context.Background(), run, Config{Provider: "jottacloud", Password: "  tok  "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), "[ygg]") {
		t.Errorf("conf = %q", conf)
	}
	if got := strings.Join(run.calls[0], " "); !strings.Contains(got, "config create ygg jottacloud config_type=standard config_login_token=tok --non-interactive") {
		t.Errorf("first call = %s", got)
	}
	if got := strings.Join(run.calls[1], " "); !strings.Contains(got, "--state choose_device --result Jotta") {
		t.Errorf("second call = %s", got)
	}
	if got := strings.Join(run.calls[2], " "); !strings.Contains(got, "--state choose_mp --result Archive") {
		t.Errorf("third call = %s (an empty default falls back to the first example)", got)
	}
}

func TestRcloneSetupRefusesWhatCannotWork(t *testing.T) {
	run := &scriptedRclone{}
	for _, c := range []Config{
		{Provider: "jottacloud"},                          // no token
		{Provider: "dropbox", Password: "abc"},            // not the authorize JSON
		{Provider: "mega", Password: "p"},                 // no e-mail
		{Provider: "webdav", Password: "p"},               // no url
		{Provider: "ftp-of-doom", Password: "x"},          // unknown
		{Provider: "custom", Password: "[a]\nx=1\n[b]\n"}, // two remotes
		{Provider: "custom", Password: "user = x\n"},      // no type
	} {
		if _, err := RcloneSetup(context.Background(), run, c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if len(run.calls) != 0 {
		t.Errorf("rclone was run %d times for configs that were refused up front", len(run.calls))
	}
	if _, err := RcloneSetup(context.Background(), nil, Config{Provider: "jottacloud", Password: "x"}); err == nil {
		t.Error("setup without docker accepted")
	}
}

func TestCustomRcloneConfIsRenamed(t *testing.T) {
	conf, err := customRcloneConf("[myjotta]\r\ntype = jottacloud\r\n# comment\r\ntoken = {\"a\":1}\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if string(conf) != "[ygg]\ntype = jottacloud\ntoken = {\"a\":1}\n" {
		t.Errorf("conf = %q", conf)
	}
}

func TestRcloneErrNamesTheFix(t *testing.T) {
	err := rcloneErr("2026/10/28 19:36:20 NOTICE: x\n2026/10/28 19:36:21 ERROR : couldn't list: oauth2: \"invalid_grant\"\n", errors.New("exit 1"))
	if !strings.Contains(err.Error(), "log in again") || strings.Contains(err.Error(), "2026/10/28") {
		t.Errorf("err = %v", err)
	}
	err = rcloneErr("2026/10/28 19:43:49 NOTICE: Fatal error: failed to get oauth token: bad\n", errors.New("exit 1"))
	if err.Error() != "rclone: failed to get oauth token: bad" {
		t.Errorf("err = %v", err)
	}
}

func TestRcloneRemotePaths(t *testing.T) {
	for p, want := range map[string]string{"": "ygg:a.tar.gz", "/": "ygg:a.tar.gz", "backups/": "ygg:backups/a.tar.gz", "/x/y": "ygg:x/y/a.tar.gz"} {
		tg, err := OpenRclone(Config{RcloneConf: "[ygg]\ntype = memory\n", Path: p}, &scriptedRclone{}, &sync.Mutex{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := tg.(*rcloneTarget).remote("a.tar.gz"); got != want {
			t.Errorf("path %q: remote = %q, want %q", p, got, want)
		}
	}
}
