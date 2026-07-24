package api

import "testing"

func TestValidatePassword(t *testing.T) {
	bad := []string{"", "short", "password", "admin", "123456", "changeme", "elevenchars"} // all <12 or weak
	for _, p := range bad {
		if err := validatePassword(p); err == nil {
			t.Errorf("expected %q to be rejected", p)
		}
	}
	good := []string{"correcthorsebatterystaple", "a-Long-Enough-1", "12characters!!"}
	for _, p := range good {
		if err := validatePassword(p); err != nil {
			t.Errorf("expected %q to be accepted, got %v", p, err)
		}
	}
}

func TestAccountLockerLocksAndResets(t *testing.T) {
	a := &accountLocker{fails: map[string]*acctFail{}}
	key := "victim"
	if a.locked(key) {
		t.Fatal("should not be locked initially")
	}
	for i := 0; i < 10; i++ {
		a.fail(key)
	}
	if !a.locked(key) {
		t.Error("should be locked after 10 failures")
	}
	a.reset(key)
	if a.locked(key) {
		t.Error("reset should clear the lock")
	}
	// Many distinct usernames must not error (sweep path exercised).
	for i := 0; i < 1000; i++ {
		a.fail(string(rune('a'+i%26)) + string(rune('0'+i%10)) + "-" + string(rune(i)))
	}
}
