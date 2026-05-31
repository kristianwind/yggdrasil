package rcon

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"net"
	"strconv"
	"time"
)

// BattlEye BERcon (used by DayZ/Arma) is a UDP protocol. Each packet is:
//
//	'B'(0x42) 'E'(0x45) | CRC32(payload) little-endian | 0xFF | payload
//
// where payload is: packet-type byte + type-specific data.
//   - 0x00 login:    0x00 + password           → server replies 0x00 + 0x01 (ok) / 0x00 (fail)
//   - 0x01 command:  0x01 + seq + ascii command → server replies 0x01 + seq + response
//
// This implementation covers login + single-packet commands (sufficient for the
// short admin commands schedules send). Multi-packet response reassembly is a
// later enhancement.
type battlEyeClient struct {
	conn    net.Conn
	timeout time.Duration
	seq     byte
}

func dialBattlEye(cfg Config) (Client, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := net.DialTimeout("udp", addr, cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("battleye dial: %w", err)
	}
	c := &battlEyeClient{conn: conn, timeout: cfg.Timeout}
	if err := c.login(cfg.Password); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// buildPacket frames a BERcon payload with header and CRC32.
func buildPacket(payload []byte) []byte {
	crc := crc32.ChecksumIEEE(payload)
	out := make([]byte, 0, 7+len(payload))
	out = append(out, 'B', 'E')
	var crcBytes [4]byte
	binary.LittleEndian.PutUint32(crcBytes[:], crc)
	out = append(out, crcBytes[:]...)
	out = append(out, 0xFF)
	out = append(out, payload...)
	return out
}

func (c *battlEyeClient) login(password string) error {
	payload := append([]byte{0x00}, []byte(password)...)
	c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	if _, err := c.conn.Write(buildPacket(payload)); err != nil {
		return err
	}
	buf := make([]byte, 16)
	c.conn.SetReadDeadline(time.Now().Add(c.timeout))
	n, err := c.conn.Read(buf)
	if err != nil {
		return fmt.Errorf("battleye login read: %w", err)
	}
	// Response payload starts after the 7-byte header: 0x00 + result(0x01 ok).
	if n < 9 || buf[7] != 0x00 || buf[8] != 0x01 {
		return fmt.Errorf("battleye login failed (bad password?)")
	}
	return nil
}

func (c *battlEyeClient) Execute(command string) (string, error) {
	payload := append([]byte{0x01, c.seq}, []byte(command)...)
	c.seq++
	c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	if _, err := c.conn.Write(buildPacket(payload)); err != nil {
		return "", err
	}
	buf := make([]byte, 4096)
	c.conn.SetReadDeadline(time.Now().Add(c.timeout))
	n, err := c.conn.Read(buf)
	if err != nil {
		return "", err
	}
	// header(7) + 0x01 + seq → response text follows.
	if n < 9 {
		return "", nil
	}
	return string(buf[9:n]), nil
}

func (c *battlEyeClient) Close() error { return c.conn.Close() }
