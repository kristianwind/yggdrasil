package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// fakeSourceRCON speaks just enough of the Source RCON wire protocol for a real
// client to authenticate against it, and records the commands it receives. The
// point is to exercise the actual dial/auth/execute path rather than stub it: the
// bug this guards against lives between the console handler and the rcon package,
// so a hand-fed fake at that boundary would not have caught it.
type fakeSourceRCON struct {
	ln       net.Listener
	password string
	got      chan string
}

func newFakeSourceRCON(t *testing.T, password string) *fakeSourceRCON {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSourceRCON{ln: ln, password: password, got: make(chan string, 8)}
	t.Cleanup(func() { ln.Close() })
	go f.serve()
	return f
}

func (f *fakeSourceRCON) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			for {
				var length int32
				if err := binary.Read(conn, binary.LittleEndian, &length); err != nil {
					return
				}
				payload := make([]byte, length)
				if _, err := io.ReadFull(conn, payload); err != nil {
					return
				}
				id := int32(binary.LittleEndian.Uint32(payload[0:4]))
				typ := int32(binary.LittleEndian.Uint32(payload[4:8]))
				body := string(bytes.SplitN(payload[8:], []byte{0}, 2)[0])
				switch typ {
				case 3: // auth
					if body != f.password {
						id = -1
					}
					conn.Write(encodeRCON(id, 2, ""))
				case 2: // exec
					f.got <- body
					conn.Write(encodeRCON(id, 0, "ack: "+body))
				}
			}
		}()
	}
}

func (f *fakeSourceRCON) port(t *testing.T) int {
	t.Helper()
	_, p, err := net.SplitHostPort(f.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func encodeRCON(id, typ int32, body string) []byte {
	payload := new(bytes.Buffer)
	binary.Write(payload, binary.LittleEndian, id)
	binary.Write(payload, binary.LittleEndian, typ)
	payload.WriteString(body)
	payload.Write([]byte{0, 0})
	out := new(bytes.Buffer)
	binary.Write(out, binary.LittleEndian, int32(payload.Len()))
	out.Write(payload.Bytes())
	return out.Bytes()
}

// seedConsoleServer registers a rune and a server row. rconYAML is spliced in as
// the rune's rcon: block so each test can describe a game with or without one.
func seedConsoleServer(t *testing.T, s *Server, rconYAML string, rconPort int) string {
	t.Helper()
	skillID := "testgame-" + uuid.New().String()[:8]
	yaml := "gameskill:\n" +
		"  id: " + skillID + "\n" +
		"  name: TestGame\n" +
		"  docker: { image: x }\n" +
		"  startup: { command: run }\n" +
		rconYAML +
		"  ports:\n    - { name: game, default: 25565, protocol: tcp }\n"
	if _, err := s.db.Exec(
		"INSERT INTO gameskills (id,name,category,version,yaml_blob,builtin) VALUES (?,?,'t',1,?,1)",
		skillID, "TestGame", yaml); err != nil {
		t.Fatal(err)
	}
	sid := uuid.New().String()
	ports := `{"game":25565,"rcon":` + strconv.Itoa(rconPort) + `}`
	if _, err := s.db.Exec(
		"INSERT INTO servers (id,name,gameskill_id,status,env_json,ports_json,data_dir) VALUES (?,?,?,'running',?,?,'/tmp/x')",
		sid, "console-test", skillID, `{"RCON_PASSWORD":"secret"}`, ports); err != nil {
		t.Fatal(err)
	}
	return sid
}

const rconEnabledYAML = "  rcon:\n    enabled: true\n    type: minecraft\n    password_var: RCON_PASSWORD\n"

// A command for a game whose rune declares RCON must go over RCON. Rust and DayZ
// read commands only over RCON, so before this the command was written to the
// container's stdin, where nothing listens: it vanished with no error while the
// UI echoed it back as if it had run.
func TestConsoleSendPrefersRCON(t *testing.T) {
	s := testServer(t)
	fake := newFakeSourceRCON(t, "secret")
	sid := seedConsoleServer(t, s, rconEnabledYAML, fake.port(t))

	var stdin bytes.Buffer
	out := s.consoleSend(context.Background(), sid, "say hello", &stdin)

	select {
	case got := <-fake.got:
		if got != "say hello" {
			t.Errorf("rcon received %q, want %q", got, "say hello")
		}
	default:
		t.Fatal("command never reached RCON; it went nowhere (stdin: " + strconv.Quote(stdin.String()) + ")")
	}
	if stdin.Len() != 0 {
		t.Errorf("command was also written to stdin: %q", stdin.String())
	}
	if len(out) != 1 || out[0] != "ack: say hello" {
		t.Errorf("reply echoed to the operator = %v, want [ack: say hello]", out)
	}
}

// A game with no rcon: block (Bedrock) must keep using stdin — that is its real
// control channel, and routing it to RCON would break a working console.
func TestConsoleSendFallsBackToStdinWithoutRCON(t *testing.T) {
	s := testServer(t)
	sid := seedConsoleServer(t, s, "  rcon:\n    enabled: false\n", 0)

	var stdin bytes.Buffer
	out := s.consoleSend(context.Background(), sid, "list", &stdin)

	if stdin.String() != "list\n" {
		t.Errorf("stdin = %q, want %q", stdin.String(), "list\n")
	}
	if len(out) != 0 {
		t.Errorf("no note expected for a game without RCON, got %v", out)
	}
}

// Minecraft stamps rcon.password into server.properties at install time only, so
// a password changed in the panel later fails to authenticate while stdin still
// works. The command must still be delivered — and the operator must be told why,
// because a silently dropped command is the whole bug.
func TestConsoleSendReportsRCONFailureAndStillDelivers(t *testing.T) {
	s := testServer(t)
	fake := newFakeSourceRCON(t, "a-different-password")
	sid := seedConsoleServer(t, s, rconEnabledYAML, fake.port(t))

	var stdin bytes.Buffer
	out := s.consoleSend(context.Background(), sid, "list", &stdin)

	if stdin.String() != "list\n" {
		t.Errorf("command not delivered to stdin after RCON failed: %q", stdin.String())
	}
	if len(out) != 1 || !strings.Contains(out[0], "rcon unavailable") {
		t.Errorf("operator was not told RCON failed, got %v", out)
	}
}
