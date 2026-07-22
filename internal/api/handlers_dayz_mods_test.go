package api

import (
	"reflect"
	"testing"
)

func TestWsIDRe(t *testing.T) {
	for _, ok := range []string{"1559212036", "0", "42"} {
		if !wsIDRe(ok) {
			t.Errorf("wsIDRe(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "12a", "@1234", "12 34", "-5", "1.0"} {
		if wsIDRe(bad) {
			t.Errorf("wsIDRe(%q) = true, want false", bad)
		}
	}
}

func TestSameIDSet(t *testing.T) {
	ids := []string{"a", "b", "c"}
	if !sameIDSet([]string{"c", "a", "b"}, ids) {
		t.Error("permutation should match")
	}
	if sameIDSet([]string{"a", "b"}, ids) {
		t.Error("shorter set must not match")
	}
	if sameIDSet([]string{"a", "b", "d"}, ids) {
		t.Error("different member must not match")
	}
	if sameIDSet([]string{"a", "b", "b"}, ids) {
		t.Error("duplicate must not match a set with distinct members")
	}
}

func TestParseDayzModSuggestion_RejectsBadOrder(t *testing.T) {
	ids := []string{"111", "222", "333"}

	// A valid permutation is kept.
	good := `{"dependencies":[{"name":"CF","reason":"needed by 222"}],"order_note":"ok","recommended_order":["222","111","333"]}`
	s := parseDayzModSuggestion(good, ids)
	if !reflect.DeepEqual(s.RecommendedOrder, []string{"222", "111", "333"}) {
		t.Fatalf("valid permutation dropped: %v", s.RecommendedOrder)
	}
	if len(s.Dependencies) != 1 || s.Dependencies[0].Name != "CF" {
		t.Fatalf("dependencies not parsed: %+v", s.Dependencies)
	}

	// An order that injects an id must be discarded (never let the model add a mod).
	inject := `{"recommended_order":["111","222","999"]}`
	if got := parseDayzModSuggestion(inject, ids).RecommendedOrder; got != nil {
		t.Fatalf("injected id survived: %v", got)
	}

	// An order that drops an id must be discarded too.
	drop := `{"recommended_order":["111","222"]}`
	if got := parseDayzModSuggestion(drop, ids).RecommendedOrder; got != nil {
		t.Fatalf("dropped id survived: %v", got)
	}

	// Prose around the JSON is tolerated.
	fenced := "here you go:\n```json\n{\"order_note\":\"fine\",\"recommended_order\":[\"111\",\"222\",\"333\"]}\n```"
	if parseDayzModSuggestion(fenced, ids).OrderNote != "fine" {
		t.Fatal("failed to extract JSON from prose")
	}
}
