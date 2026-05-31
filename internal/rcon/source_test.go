package rcon

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	pkt := encodePacket(7, typeExecCommand, "list")
	// First 4 bytes are the length of the remainder.
	length := int32(binary.LittleEndian.Uint32(pkt[0:4]))
	if int(length) != len(pkt)-4 {
		t.Fatalf("length field = %d, want %d", length, len(pkt)-4)
	}
	id, typ, body, err := decodePacket(pkt[4:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if id != 7 || typ != typeExecCommand || body != "list" {
		t.Errorf("round trip mismatch: id=%d typ=%d body=%q", id, typ, body)
	}
}

func TestDecodeRejectsShort(t *testing.T) {
	if _, _, _, err := decodePacket([]byte{1, 2, 3}); err == nil {
		t.Error("expected error for short packet")
	}
}

// mockSourceServer implements just enough of the Source RCON protocol to test
// the client end to end: it authenticates a fixed password and echoes commands.
func mockSourceServer(t *testing.T, password string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			var length int32
			if err := binary.Read(conn, binary.LittleEndian, &length); err != nil {
				return
			}
			payload := make([]byte, length)
			if _, err := io.ReadFull(conn, payload); err != nil {
				return
			}
			id, typ, body, _ := decodePacket(payload)
			switch typ {
			case typeAuth:
				respID := id
				if body != password {
					respID = -1
				}
				conn.Write(encodePacket(respID, typeAuthResponse, ""))
			case typeExecCommand:
				conn.Write(encodePacket(id, typeResponse, "echo: "+body))
			}
		}
	}()
	return ln
}

func TestSourceClientAuthAndExecute(t *testing.T) {
	ln := mockSourceServer(t, "secret")
	defer ln.Close()

	host, port := splitHostPort(t, ln.Addr().String())
	c, err := Dial(Config{Type: "minecraft", Host: host, Port: port, Password: "secret", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	resp, err := c.Execute("list")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if resp != "echo: list" {
		t.Errorf("response = %q, want %q", resp, "echo: list")
	}
}

func TestSourceClientBadPassword(t *testing.T) {
	ln := mockSourceServer(t, "secret")
	defer ln.Close()

	host, port := splitHostPort(t, ln.Addr().String())
	_, err := Dial(Config{Type: "minecraft", Host: host, Port: port, Password: "wrong", Timeout: 2 * time.Second})
	if err == nil {
		t.Error("expected auth failure with wrong password")
	}
}

func TestBattlEyePacketFraming(t *testing.T) {
	// 'B','E' header, 4-byte CRC, 0xFF marker, then payload.
	pkt := buildPacket([]byte{0x00, 'p', 'w'})
	if pkt[0] != 'B' || pkt[1] != 'E' || pkt[6] != 0xFF {
		t.Errorf("bad header framing: % x", pkt[:7])
	}
	if string(pkt[7:]) != "\x00pw" {
		t.Errorf("payload mismatch: % x", pkt[7:])
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	return host, port
}
