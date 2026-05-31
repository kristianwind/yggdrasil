package query

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"
)

func TestVarIntRoundTrip(t *testing.T) {
	for _, v := range []int{0, 1, 127, 128, 255, 300, 25565, 2097151, 2147483647} {
		var buf []byte
		writeVarInt(&buf, v)
		got, err := readVarInt(bufio.NewReader(bytes.NewReader(buf)))
		if err != nil {
			t.Fatalf("readVarInt(%d): %v", v, err)
		}
		if got != v {
			t.Errorf("varint round trip: got %d want %d", got, v)
		}
	}
}

func TestParseA2SInfo(t *testing.T) {
	// Build a synthetic A2S_INFO response.
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x49}) // header + 'I'
	b.WriteByte(17)                               // protocol
	b.WriteString("Test Server\x00")
	b.WriteString("de_dust2\x00")
	b.WriteString("rust\x00")
	b.WriteString("Rust\x00")
	b.Write([]byte{0x26, 0x01}) // appid 0x0126
	b.WriteByte(12)             // players
	b.WriteByte(50)             // max players

	st, err := parseA2SInfo(b.Bytes())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Name != "Test Server" || st.Map != "de_dust2" || st.Players != 12 || st.MaxPlayers != 50 {
		t.Errorf("unexpected status: %+v", st)
	}
}

func TestParseMinecraftStatus(t *testing.T) {
	json := `{"version":{"name":"1.21.1"},"players":{"max":20,"online":3},"description":"hi"}`
	st, err := parseMinecraftStatus([]byte(json))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Version != "1.21.1" || st.Players != 3 || st.MaxPlayers != 20 {
		t.Errorf("unexpected status: %+v", st)
	}
}

func TestParseBedrockPong(t *testing.T) {
	var b bytes.Buffer
	b.WriteByte(0x1C)
	b.Write(make([]byte, 8+8+16)) // time + guid + magic
	id := "MCPE;Dedicated;630;1.21.0;7;100;1234;Bedrock"
	b.Write([]byte{byte(len(id) >> 8), byte(len(id))})
	b.WriteString(id)

	st, err := parseBedrockPong(b.Bytes())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Players != 7 || st.MaxPlayers != 100 || st.Version != "1.21.0" {
		t.Errorf("unexpected status: %+v", st)
	}
}

// End-to-end against a mock Minecraft SLP server.
func TestMinecraftJavaQueryE2E(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		// Read handshake + status request (length-prefixed); we don't validate them.
		for i := 0; i < 2; i++ {
			n, err := readVarInt(r)
			if err != nil {
				return
			}
			io := make([]byte, n)
			readFull(r, io)
		}
		status := `{"version":{"name":"1.21.1"},"players":{"max":20,"online":5},"description":"Mock"}`
		var payload []byte
		writeVarInt(&payload, len(status))
		payload = append(payload, []byte(status)...)
		conn.Write(packet(0x00, payload))
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	st, err := Query("minecraft", host, port, 2*time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if st.Players != 5 || st.MaxPlayers != 20 || st.Version != "1.21.1" {
		t.Errorf("unexpected status: %+v", st)
	}
}
