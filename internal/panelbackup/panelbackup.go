// Package panelbackup makes and reads an encrypted backup of the panel itself:
// the database and the one secret in config.yaml that every stored credential
// is encrypted with.
//
// Why encrypted, and not optional. The archive has to carry auth.secret_key or
// it cannot be restored — every integration password, API token and TOTP secret
// in the database is unreadable without it. That makes the archive a master key
// to everything the panel holds, and a panel backup is written automatically,
// repeatedly, to a NAS or an SFTP server whose own credentials are a different
// and usually weaker thing. So the file is encrypted with a passphrase the
// operator sets and keeps, and the blast radius of the backup target stops at
// "an opaque blob".
//
// The passphrase is stored (encrypted) in the panel's own database so the
// scheduled job can run unattended. That is not circular: an attacker holding
// only the archive does not have the database, and an attacker holding the
// database already has everything the archive would give them. It does mean the
// operator must know the passphrase themselves — a restore happens on a host
// that has no database to read it from.
package panelbackup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"

	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/migrate"
)

// Magic identifies the format and its version. A reader that finds something
// else says so, rather than failing later with a decryption error that reads
// like a wrong passphrase.
var magic = []byte("YGGPB1\n")

const (
	saltLen  = 16
	argonMem = 64 * 1024 // 64 MiB
	argonIt  = 3
	argonPar = 4
	keyLen   = 32
)

// ErrWrongPassphrase is what a reader gets for a passphrase that does not open
// the archive. It is a named error because "cipher: message authentication
// failed" is the same message a corrupted file produces, and those two send an
// operator to very different places.
var ErrWrongPassphrase = errors.New("wrong passphrase (or the file is damaged)")

// ErrNotPanelBackup means the file is not one of ours at all.
var ErrNotPanelBackup = errors.New("not a Yggdrasil panel backup")

// ErrNoPassphrase refuses to write an archive anybody can open. The whole point
// of the format is that the secret key inside it is not readable from the
// backup target.
var ErrNoPassphrase = errors.New("a passphrase is required: the archive carries the panel's encryption key")

func deriveKey(pass string, salt []byte) []byte {
	return argon2.IDKey([]byte(pass), salt, argonIt, argonMem, argonPar, keyLen)
}

// Create writes an encrypted panel backup to w: a consistent snapshot of the
// database (VACUUM INTO, so a live WAL cannot produce a torn copy) plus the
// manifest carrying auth.secret_key.
//
// Server data directories are deliberately NOT included. They have their own
// backups, with their own schedules and retention, and putting 16 GB of worlds
// into the file that exists to restore the panel's settings makes the one job
// that must stay quick and frequent into the one that does not run.
func Create(cfg *config.Config, db *sql.DB, passphrase string, w io.Writer) error {
	if passphrase == "" {
		return ErrNoPassphrase
	}
	// Buffered rather than streamed: a settings-only bundle is ~13 MB, and AES-GCM
	// over one buffer is a shape that is obviously right. A streaming version
	// needs chunked framing with per-chunk authentication, and getting that
	// subtly wrong is indistinguishable from getting it right until someone
	// restores.
	var plain bytes.Buffer
	if err := migrate.Export(cfg, db, &plain); err != nil {
		return fmt.Errorf("build bundle: %w", err)
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	block, err := aes.NewCipher(deriveKey(passphrase, salt))
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	// The header travels as additional authenticated data. Belt and braces rather
	// than the only guard, and the comment says so because a plant that removed
	// it changed no observable behaviour: the magic is checked on read, the salt
	// is an input to the key, and the nonce is covered by GCM, so every header
	// byte is already protected by something else. Kept because it costs nothing
	// and is correct -- not because it carries the format on its own.
	header := append(append(append([]byte{}, magic...), salt...), nonce...)
	sealed := aead.Seal(nil, nonce, plain.Bytes(), header)

	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err = w.Write(sealed)
	return err
}

// Open decrypts an archive and returns the plain bundle (the same tar.gz that
// `yggdrasil migrate export` produces), ready for migrate.Import.
func Open(r io.Reader, passphrase string) ([]byte, error) {
	all, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	head := len(magic) + saltLen + 12 // GCM standard nonce
	if len(all) < head || !bytes.Equal(all[:len(magic)], magic) {
		return nil, ErrNotPanelBackup
	}
	salt := all[len(magic) : len(magic)+saltLen]
	nonce := all[len(magic)+saltLen : head]
	block, err := aes.NewCipher(deriveKey(passphrase, salt))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, all[head:], all[:head])
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return plain, nil
}
