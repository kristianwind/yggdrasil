package rbac

import "testing"

func TestGlobalGrantAllowsEverything(t *testing.T) {
	grants := []Grant{{ScopeType: ScopeGlobal, Perms: []Permission{ServerControl}}}
	tgt := Target{ServerID: "s1", RealmID: "r1", GameskillID: "minecraft-java"}
	if !Allowed(grants, ServerControl, tgt) {
		t.Error("global control grant should allow control")
	}
	if Allowed(grants, ServerConsole, tgt) {
		t.Error("global grant without console perm should not allow console")
	}
}

func TestRealmScope(t *testing.T) {
	grants := []Grant{{ScopeType: ScopeRealm, ScopeID: "family", Perms: []Permission{ServerControl, ServerView}}}
	inRealm := Target{ServerID: "s1", RealmID: "family", GameskillID: "rust"}
	otherRealm := Target{ServerID: "s2", RealmID: "public", GameskillID: "rust"}

	if !Allowed(grants, ServerControl, inRealm) {
		t.Error("should allow control in granted realm")
	}
	if Allowed(grants, ServerControl, otherRealm) {
		t.Error("should NOT allow control in a different realm")
	}
}

func TestGameskillScope(t *testing.T) {
	// "can manage all Minecraft servers but not see DayZ"
	grants := []Grant{{ScopeType: ScopeGameskill, ScopeID: "minecraft-java", Perms: []Permission{ServerView, ServerControl}}}
	mc := Target{ServerID: "s1", RealmID: "r", GameskillID: "minecraft-java"}
	dayz := Target{ServerID: "s2", RealmID: "r", GameskillID: "dayz"}

	if !Allowed(grants, ServerControl, mc) {
		t.Error("should control minecraft")
	}
	if VisibleServer(grants, dayz) {
		t.Error("should NOT see dayz")
	}
}

func TestServerScope(t *testing.T) {
	grants := []Grant{{ScopeType: ScopeServer, ScopeID: "s1", Perms: []Permission{ServerView}}}
	if !VisibleServer(grants, Target{ServerID: "s1"}) {
		t.Error("should see granted server")
	}
	if VisibleServer(grants, Target{ServerID: "s2"}) {
		t.Error("should not see other server")
	}
}

func TestNoGrants(t *testing.T) {
	if Allowed(nil, ServerView, Target{ServerID: "s1"}) {
		t.Error("empty grants must deny")
	}
}

func TestPerScopeDifferentPerms(t *testing.T) {
	// "console + restart on one realm, only view status on another"
	grants := []Grant{
		{ScopeType: ScopeRealm, ScopeID: "a", Perms: []Permission{ServerConsole, ServerControl, ServerView}},
		{ScopeType: ScopeRealm, ScopeID: "b", Perms: []Permission{ServerView}},
	}
	a := Target{RealmID: "a"}
	b := Target{RealmID: "b"}
	if !Allowed(grants, ServerConsole, a) || !Allowed(grants, ServerControl, a) {
		t.Error("realm a should allow console + control")
	}
	if Allowed(grants, ServerControl, b) {
		t.Error("realm b should not allow control")
	}
	if !Allowed(grants, ServerView, b) {
		t.Error("realm b should allow view")
	}
}
