package gameskill

import "testing"

// A sidecar the operator can turn off, and the three cases that are not two.
//
// The one that matters is the third: a server created before the rune gained the
// variable has no such key in its env, ApplyTemplate substitutes only keys that
// are present, and the value arrives here as the literal "{{OBJECT_CACHE}}".
// Every live WordPress site on the panel is in exactly that state the moment this
// rune ships. Erroring there fails their START; defaulting to on hands them a
// container nobody asked for. Off leaves them as they were.
func TestServiceIsEnabled(t *testing.T) {
	env := map[string]string{"OBJECT_CACHE": "true", "OFF": "false", "TYPO": "ja"}

	for _, tc := range []struct {
		name    string
		enabled string
		want    bool
		wantErr bool
	}{
		{"absent means always on (every rune written before this)", "", true, false},
		{"a literal true", "true", true, false},
		{"a variable that resolved to true", "{{OBJECT_CACHE}}", true, false},
		{"a variable that resolved to false", "{{OFF}}", false, false},
		{"an unset variable on an older server", "{{NOT_IN_ENV}}", false, false},
		{"literal false", "false", false, false},
		{"yes/on are accepted", "on", true, false},
		{"0 is off", "0", false, false},
		{"a typo is an error, not a quiet off", "{{TYPO}}", false, true},
		{"an unrecognised literal is an error", "maybe", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Service{Name: "redis", Enabled: tc.enabled}.IsEnabled(env)
			if (err != nil) != tc.wantErr {
				t.Fatalf("enabled=%q gave err=%v, wantErr=%v", tc.enabled, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("enabled=%q -> %v, want %v", tc.enabled, got, tc.want)
			}
		})
	}
}

// The error has to name the service and the value, or the operator is left with a
// container that is not there and nothing to read about why.
func TestServiceEnabledErrorNamesTheService(t *testing.T) {
	_, err := Service{Name: "redis", Enabled: "perhaps"}.IsEnabled(nil)
	if err == nil {
		t.Fatal("no error for an unrecognised value")
	}
	for _, want := range []string{"redis", "perhaps"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
