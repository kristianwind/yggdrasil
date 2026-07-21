package query

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"time"
)

// A2S_INFO query (Source/Steam). Handles the modern challenge-response flow.
var a2sInfoRequest = append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x54}, []byte("Source Engine Query\x00")...)

func queryA2S(host string, port int, timeout time.Duration) (*Status, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)

	resp, err := a2sExchange(conn, a2sInfoRequest, deadline)
	if err != nil {
		return nil, err
	}

	// Challenge response: 0xFF*4, 0x41, <4-byte challenge> → resend with it.
	if len(resp) >= 9 && resp[4] == 0x41 {
		req := append(append([]byte{}, a2sInfoRequest...), resp[5:9]...)
		resp, err = a2sExchange(conn, req, deadline)
		if err != nil {
			return nil, err
		}
	}

	return parseA2SInfo(resp)
}

func a2sExchange(conn net.Conn, req []byte, deadline time.Time) ([]byte, error) {
	conn.SetWriteDeadline(deadline)
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	buf := make([]byte, 1400)
	conn.SetReadDeadline(deadline)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// queryA2SPlayers runs an A2S_PLAYER query (the player list) — same challenge flow
// as A2S_INFO. Returns the player names; empty names (some games send blanks) are
// dropped by the caller. Not every game answers this (Rust deliberately doesn't),
// so callers treat an error/empty as "names unavailable" and fall back to a count.
func queryA2SPlayers(host string, port int, timeout time.Duration) ([]string, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)

	// A2S_PLAYER header 0x55 + a 4-byte challenge; start with -1 to be given one.
	req := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x55, 0xFF, 0xFF, 0xFF, 0xFF}
	resp, err := a2sExchange(conn, req, deadline)
	if err != nil {
		return nil, err
	}
	if len(resp) >= 9 && resp[4] == 0x41 { // challenge → resend with it
		req = append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x55}, resp[5:9]...)
		resp, err = a2sExchange(conn, req, deadline)
		if err != nil {
			return nil, err
		}
	}
	return parseA2SPlayers(resp)
}

// parseA2SPlayers parses an A2S_PLAYER (0x44) response: a count, then per player an
// index byte, a null-terminated name, a 4-byte score and a 4-byte duration.
func parseA2SPlayers(data []byte) ([]string, error) {
	if len(data) < 6 || data[4] != 0x44 {
		return nil, fmt.Errorf("a2s: unexpected player response")
	}
	r := bytes.NewReader(data[5:])
	count, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, count)
	for i := 0; i < int(count); i++ {
		if _, err := r.ReadByte(); err != nil { // player index
			break
		}
		var sb []byte
		for {
			b, err := r.ReadByte()
			if err != nil || b == 0 {
				break
			}
			sb = append(sb, b)
		}
		for j := 0; j < 8; j++ { // score (int32) + duration (float32)
			if _, err := r.ReadByte(); err != nil {
				break
			}
		}
		names = append(names, string(sb))
	}
	return names, nil
}

// parseA2SInfo parses an A2S_INFO (0x49) response body.
func parseA2SInfo(data []byte) (*Status, error) {
	if len(data) < 6 || data[4] != 0x49 {
		return nil, fmt.Errorf("a2s: unexpected response header")
	}
	r := bytes.NewReader(data[5:]) // skip header + 'I'

	readByte := func() byte { b, _ := r.ReadByte(); return b }
	readString := func() string {
		var sb []byte
		for {
			b, err := r.ReadByte()
			if err != nil || b == 0 {
				break
			}
			sb = append(sb, b)
		}
		return string(sb)
	}

	_ = readByte() // protocol version
	name := readString()
	mapName := readString()
	_ = readString() // folder
	_ = readString() // game
	var appID uint16
	binary.Read(r, binary.LittleEndian, &appID)
	players := int(readByte())
	maxPlayers := int(readByte())

	return &Status{
		Online:     true,
		Name:       name,
		Map:        mapName,
		Players:    players,
		MaxPlayers: maxPlayers,
	}, nil
}
