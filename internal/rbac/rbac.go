// Package rbac implements Yggdrasil's scoped permission model: a user may be
// granted a set of permissions at a scope (global, a realm, a game type, or a
// single server). Global admins bypass all checks; everyone else is evaluated
// against their grants.
package rbac

// Permission is a single capability that can be granted within a scope.
type Permission string

const (
	ServerView     Permission = "server.view"     // see the server + status
	ServerControl  Permission = "server.control"  // start / stop / restart
	ServerConsole  Permission = "server.console"  // console + RCON
	ServerFiles    Permission = "server.files"    // browse / edit / upload files
	ServerCreate   Permission = "server.create"   // create new servers in scope
	ServerDelete   Permission = "server.delete"   // delete servers
	ServerBackup   Permission = "server.backup"   // run / view backups
	ServerSchedule Permission = "server.schedule" // manage schedules
)

// All lists every assignable permission (order is the UI display order).
var All = []Permission{
	ServerView, ServerControl, ServerConsole, ServerFiles,
	ServerCreate, ServerDelete, ServerBackup, ServerSchedule,
}

// Valid reports whether p is a known permission.
func Valid(p Permission) bool {
	for _, a := range All {
		if a == p {
			return true
		}
	}
	return false
}

// ScopeType identifies what a grant applies to.
type ScopeType string

const (
	ScopeGlobal    ScopeType = "global"
	ScopeRealm     ScopeType = "realm"
	ScopeGameskill ScopeType = "gameskill"
	ScopeServer    ScopeType = "server"
)

// Grant is a set of permissions a user holds at one scope. For ScopeGlobal the
// ScopeID is empty.
type Grant struct {
	ScopeType ScopeType    `json:"scope_type"`
	ScopeID   string       `json:"scope_id,omitempty"`
	Perms     []Permission `json:"perms"`
}

func (g Grant) has(p Permission) bool {
	for _, x := range g.Perms {
		if x == p {
			return true
		}
	}
	return false
}

// Target describes the object an action touches. A server has all three IDs; a
// realm- or gameskill-level action (e.g. create) sets only the relevant ones.
type Target struct {
	ServerID    string
	RealmID     string
	GameskillID string
}

// Allowed reports whether the given grants permit perm on target. A grant
// matches when its scope covers the target and it includes the permission.
func Allowed(grants []Grant, perm Permission, target Target) bool {
	for _, g := range grants {
		if !g.has(perm) {
			continue
		}
		switch g.ScopeType {
		case ScopeGlobal:
			return true
		case ScopeRealm:
			if target.RealmID != "" && g.ScopeID == target.RealmID {
				return true
			}
		case ScopeGameskill:
			if target.GameskillID != "" && g.ScopeID == target.GameskillID {
				return true
			}
		case ScopeServer:
			if target.ServerID != "" && g.ScopeID == target.ServerID {
				return true
			}
		}
	}
	return false
}

// VisibleServer reports whether the user can at least view the server — used to
// filter list endpoints.
func VisibleServer(grants []Grant, target Target) bool {
	return Allowed(grants, ServerView, target)
}
