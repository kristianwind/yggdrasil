package query

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Minecraft Bedrock uses RakNet's "unconnected ping". The pong's ID string is a
// semicolon-delimited record containing player counts.
var bedrockMagic = []byte{0x00, 0xFF, 0xFF, 0x00, 0xFE, 0xFE, 0xFE, 0xFE, 0xFD, 0xFD, 0xFD, 0xFD, 0x12, 0x34, 0x56, 0x78}

func queryBedrock(host string, port int, timeout time.Duration) (*Status, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)

	// Unconnected ping: 0x01 | time(int64) | magic | client GUID(int64)
	var req bytes.Buffer
	req.WriteByte(0x01)
	binary.Write(&req, binary.BigEndian, time.Now().UnixMilli())
	req.Write(bedrockMagic)
	binary.Write(&req, binary.BigEndian, int64(2))

	conn.SetWriteDeadline(deadline)
	if _, err := conn.Write(req.Bytes()); err != nil {
		return nil, err
	}

	buf := make([]byte, 2048)
	conn.SetReadDeadline(deadline)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return parseBedrockPong(buf[:n])
}

// parseBedrockPong extracts the status from an unconnected-pong (0x1C) packet.
// The ID string layout is:
//
//	edition;motd;protocol;version;players;maxplayers;serverGUID;...
func parseBedrockPong(data []byte) (*Status, error) {
	if len(data) < 35 || data[0] != 0x1C {
		return nil, fmt.Errorf("bedrock: unexpected pong header")
	}
	// Skip: id(1) + time(8) + serverGUID(8) + magic(16), then a 2-byte string length.
	idx := 1 + 8 + 8 + 16
	if len(data) < idx+2 {
		return nil, fmt.Errorf("bedrock: truncated pong")
	}
	strLen := int(binary.BigEndian.Uint16(data[idx : idx+2]))
	idx += 2
	if len(data) < idx+strLen {
		strLen = len(data) - idx
	}
	fields := strings.Split(string(data[idx:idx+strLen]), ";")

	st := &Status{Online: true}
	if len(fields) > 1 {
		st.Name = fields[1]
	}
	if len(fields) > 3 {
		st.Version = fields[3]
	}
	if len(fields) > 4 {
		st.Players, _ = strconv.Atoi(fields[4])
	}
	if len(fields) > 5 {
		st.MaxPlayers, _ = strconv.Atoi(fields[5])
	}
	return st, nil
}
