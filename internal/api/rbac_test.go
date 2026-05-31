package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/kristianwind/yggdrasil/internal/db"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return &Server{db: database}
}

func TestLoadGrantsRoundTrip(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	userID := uuid.New().String()
	s.db.Exec("INSERT INTO users (id, username, password_hash, role) VALUES (?,?,?,'user')",
		userID, "scoped", "x")
	// Realm-scoped: control + view; server-scoped: console.
	s.db.Exec("INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES (?,?,?,?,?)",
		uuid.New().String(), userID, "realm", "family", "server.control,server.view")
	s.db.Exec("INSERT INTO permissions (id, user_id, scope_type, scope_id, perms) VALUES (?,?,?,?,?)",
		uuid.New().String(), userID, "server", "srv-1", "server.console")

	grants := s.loadGrants(ctx, userID)
	if len(grants) != 2 {
		t.Fatalf("expected 2 grants, got %d", len(grants))
	}

	// Realm grant permits control in "family" but not elsewhere.
	if !rbac.Allowed(grants, rbac.ServerControl, rbac.Target{RealmID: "family"}) {
		t.Error("should allow control in family realm")
	}
	if rbac.Allowed(grants, rbac.ServerControl, rbac.Target{RealmID: "public"}) {
		t.Error("should not allow control in public realm")
	}
	// Server grant permits console on srv-1 only.
	if !rbac.Allowed(grants, rbac.ServerConsole, rbac.Target{ServerID: "srv-1"}) {
		t.Error("should allow console on srv-1")
	}
	if rbac.Allowed(grants, rbac.ServerConsole, rbac.Target{ServerID: "srv-2"}) {
		t.Error("should not allow console on srv-2")
	}
}

func TestServerTargetResolution(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	realmID := uuid.New().String()
	serverID := uuid.New().String()
	s.db.Exec("INSERT INTO realms (id, name) VALUES (?,?)", realmID, "Family")
	s.db.Exec(`INSERT INTO servers (id, name, gameskill_id, realm_id, data_dir)
	           VALUES (?,?,?,?,?)`, serverID, "MC", "minecraft-java", realmID, "/tmp/x")

	tgt := s.serverTarget(ctx, serverID)
	if tgt.ServerID != serverID || tgt.RealmID != realmID || tgt.GameskillID != "minecraft-java" {
		t.Errorf("unexpected target: %+v", tgt)
	}
}

func TestEmptyGrantsDenyByDefault(t *testing.T) {
	s := testServer(t)
	grants := s.loadGrants(context.Background(), "nobody")
	if rbac.Allowed(grants, rbac.ServerView, rbac.Target{ServerID: "any"}) {
		t.Error("user with no grants must be denied")
	}
}
