package api

import "testing"

func TestParseDayzRoster(t *testing.T) {
	// Real .ADM lines (id is the BIS base64 id, name is quoted).
	log := `` +
		`20:50:27 | Player "LoveLazer" (id=YhvxviAH-6JpwV53dE4KjnjpeeUh2H_OOtmjXcZyncs= pos=<13087.2, 10066.5, 10.2>) is connected` + "\n" +
		`20:51:00 | Player "Bob" (id=AAAA1111 pos=<1,2,3>) is connected` + "\n" +
		`20:56:00 | Player "LoveLazer" (id=YhvxviAH-6JpwV53dE4KjnjpeeUh2H_OOtmjXcZyncs= pos=<1,2,3>) has been disconnected` + "\n" +
		`21:03:40 | Player "LoveLazer" (id=YhvxviAH-6JpwV53dE4KjnjpeeUh2H_OOtmjXcZyncs= pos=<1,2,3>) is connected` + "\n"

	got := parseDayzRoster(log)
	// Bob (connected, never left) and LoveLazer (reconnected after leaving) are online.
	if len(got) != 2 {
		t.Fatalf("want 2 online, got %d: %+v", len(got), got)
	}
	byName := map[string]dayzRosterEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if byName["LoveLazer"].ID != "YhvxviAH-6JpwV53dE4KjnjpeeUh2H_OOtmjXcZyncs=" {
		t.Errorf("LoveLazer id wrong: %q", byName["LoveLazer"].ID)
	}
	if byName["LoveLazer"].Since != "21:03:40" {
		t.Errorf("LoveLazer since should be the reconnect time, got %q", byName["LoveLazer"].Since)
	}
	if _, ok := byName["Bob"]; !ok {
		t.Error("Bob should be online")
	}

	// A player who connected then disconnected is not online.
	log2 := `10:00:00 | Player "Gone" (id=X1 pos=<0,0,0>) is connected` + "\n" +
		`10:05:00 | Player "Gone" (id=X1 pos=<0,0,0>) has been disconnected` + "\n"
	if r := parseDayzRoster(log2); len(r) != 0 {
		t.Fatalf("want empty roster, got %+v", r)
	}

	// Header-only log yields nothing.
	if r := parseDayzRoster("AdminLog started on 2026-07-22\n"); len(r) != 0 {
		t.Fatalf("want empty, got %+v", r)
	}
}
