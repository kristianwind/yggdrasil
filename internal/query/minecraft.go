package query

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Minecraft Java Server List Ping (1.7+). A handshake + status request returns a
// JSON document with version, MOTD and player counts.

func writeVarInt(buf *[]byte, v int) {
	uv := uint32(v)
	for {
		b := byte(uv & 0x7F)
		uv >>= 7
		if uv != 0 {
			b |= 0x80
		}
		*buf = append(*buf, b)
		if uv == 0 {
			break
		}
	}
}

func readVarInt(r *bufio.Reader) (int, error) {
	var result uint32
	var shift uint
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 35 {
			return 0, fmt.Errorf("varint too long")
		}
	}
	return int(result), nil
}

func packet(id int, payload []byte) []byte {
	var body []byte
	writeVarInt(&body, id)
	body = append(body, payload...)
	var out []byte
	writeVarInt(&out, len(body))
	return append(out, body...)
}

func queryMinecraftJava(host string, port int, timeout time.Duration) (*Status, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	// Handshake: protocol(-1 / 0), server address, port, next state = 1 (status)
	var hs []byte
	writeVarInt(&hs, 0) // protocol version (0 = determine)
	writeVarInt(&hs, len(host))
	hs = append(hs, []byte(host)...)
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	hs = append(hs, portBytes[:]...)
	writeVarInt(&hs, 1) // next state: status

	if _, err := conn.Write(packet(0x00, hs)); err != nil {
		return nil, err
	}
	// Status request (empty packet 0x00)
	if _, err := conn.Write(packet(0x00, nil)); err != nil {
		return nil, err
	}

	r := bufio.NewReader(conn)
	if _, err := readVarInt(r); err != nil { // total packet length
		return nil, err
	}
	if _, err := readVarInt(r); err != nil { // packet id (expect 0x00)
		return nil, err
	}
	strLen, err := readVarInt(r)
	if err != nil {
		return nil, err
	}
	jsonBytes := make([]byte, strLen)
	if _, err := readFull(r, jsonBytes); err != nil {
		return nil, err
	}
	return parseMinecraftStatus(jsonBytes)
}

type mcStatus struct {
	Version struct {
		Name string `json:"name"`
	} `json:"version"`
	Players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
	} `json:"players"`
	Description json.RawMessage `json:"description"`
}

func parseMinecraftStatus(jsonBytes []byte) (*Status, error) {
	var s mcStatus
	if err := json.Unmarshal(jsonBytes, &s); err != nil {
		return nil, fmt.Errorf("minecraft status json: %w", err)
	}
	return &Status{
		Online:     true,
		Version:    s.Version.Name,
		Players:    s.Players.Online,
		MaxPlayers: s.Players.Max,
	}, nil
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
