package rcon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

// Source RCON packet types.
const (
	typeAuth         = 3
	typeExecCommand  = 2
	typeAuthResponse = 2
	typeResponse     = 0
)

const maxPacketSize = 4096 + 16

type sourceClient struct {
	conn    net.Conn
	timeout time.Duration
	reqID   int32
}

func dialSource(cfg Config) (Client, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("rcon dial: %w", err)
	}
	c := &sourceClient{conn: conn, timeout: cfg.Timeout}
	if err := c.auth(cfg.Password); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *sourceClient) auth(password string) error {
	id := c.nextID()
	if err := c.write(id, typeAuth, password); err != nil {
		return err
	}
	// Server may send an empty SERVERDATA_RESPONSE_VALUE first, then the auth
	// response. The auth response carries our id on success, -1 on failure.
	for {
		respID, _, _, err := c.read()
		if err != nil {
			return fmt.Errorf("rcon auth read: %w", err)
		}
		if respID == -1 {
			return fmt.Errorf("rcon auth failed: bad password")
		}
		if respID == id {
			return nil
		}
	}
}

func (c *sourceClient) Execute(command string) (string, error) {
	id := c.nextID()
	if err := c.write(id, typeExecCommand, command); err != nil {
		return "", err
	}
	respID, _, body, err := c.read()
	if err != nil {
		return "", err
	}
	if respID != id {
		// Tolerate interleaved packets by returning what we got.
		return body, nil
	}
	return body, nil
}

func (c *sourceClient) Close() error { return c.conn.Close() }

func (c *sourceClient) nextID() int32 {
	return atomic.AddInt32(&c.reqID, 1)
}

// encodePacket builds a Source RCON packet for the given id, type and body.
func encodePacket(id, typ int32, body string) []byte {
	payload := new(bytes.Buffer)
	binary.Write(payload, binary.LittleEndian, id)
	binary.Write(payload, binary.LittleEndian, typ)
	payload.WriteString(body)
	payload.WriteByte(0) // body terminator
	payload.WriteByte(0) // empty-string terminator

	out := new(bytes.Buffer)
	binary.Write(out, binary.LittleEndian, int32(payload.Len()))
	out.Write(payload.Bytes())
	return out.Bytes()
}

func (c *sourceClient) write(id, typ int32, body string) error {
	c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := c.conn.Write(encodePacket(id, typ, body))
	return err
}

// decodePacket parses one packet's payload (without the leading length field).
func decodePacket(payload []byte) (id, typ int32, body string, err error) {
	if len(payload) < 10 {
		return 0, 0, "", fmt.Errorf("rcon packet too short: %d bytes", len(payload))
	}
	id = int32(binary.LittleEndian.Uint32(payload[0:4]))
	typ = int32(binary.LittleEndian.Uint32(payload[4:8]))
	// body is null-terminated; strip the two trailing nulls.
	body = string(bytes.TrimRight(payload[8:], "\x00"))
	return id, typ, body, nil
}

func (c *sourceClient) read() (id, typ int32, body string, err error) {
	c.conn.SetReadDeadline(time.Now().Add(c.timeout))
	var length int32
	if err := binary.Read(c.conn, binary.LittleEndian, &length); err != nil {
		return 0, 0, "", err
	}
	if length < 10 || length > maxPacketSize {
		return 0, 0, "", fmt.Errorf("rcon invalid packet length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return 0, 0, "", err
	}
	return decodePacket(payload)
}
