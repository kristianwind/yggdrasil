package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/db"
)

// runResetTwoFactor handles `yggdrasil reset-2fa <username>`: the break-glass
// path for a LOST SECOND FACTOR, which the panel had no answer to.
//
// The gap was real and it bit the owner of this project. reset-password covers a
// forgotten password; losing the TOTP seed — a deleted password-manager entry, a
// wiped phone — had nothing. Disabling 2FA over the API deliberately requires a
// current code (a hijacked session must not be able to strip the second factor),
// so the one person who cannot use that endpoint is the one who needs it. The
// only remaining route was a hand-written UPDATE against the database, which is
// exactly the kind of thing that goes wrong at 23:00 under stress.
//
// Like reset-password, this runs against the database on the host: no running
// panel, no auth beyond shell access to the machine. If you can run this, you
// could already read the database — so it grants nothing new. What it adds is
// that the operation is spelled correctly, is refused when it would be pointless,
// and says out loud that the account is now one factor short.
//
// It clears TOTP only. Passkeys are left alone: they are a second factor in their
// own right, they are the thing most likely to still WORK in this situation, and
// silently removing one because someone asked about TOTP would take away the
// login that was still good.
func runResetTwoFactor(args []string) error {
	fs := flag.NewFlagSet("reset-2fa", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/yggdrasil/config.yaml", "path to config.yaml")

	// Same positional handling as reset-password: Go's flag package stops at the
	// first non-flag token, so accept the username on either side of the flags.
	username := ""
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		username = args[0]
		rest = args[1:]
	}
	fs.Parse(rest) //nolint:errcheck // ExitOnError
	if username == "" {
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: yggdrasil reset-2fa <username>")
		}
		username = fs.Arg(0)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	var id string
	var enabled int
	if err := database.QueryRow(
		"SELECT id, COALESCE(totp_enabled,0) FROM users WHERE username=?", username,
	).Scan(&id, &enabled); err != nil {
		return fmt.Errorf("no such user %q", username)
	}
	if enabled == 0 {
		// Refusing here is the point: "it said OK" on the wrong username is how
		// someone walks away believing they fixed an account they never touched.
		return fmt.Errorf("2FA is not enabled for %q — nothing to reset", username)
	}

	if _, err := database.Exec(
		"UPDATE users SET totp_secret=NULL, totp_enabled=0 WHERE id=?", id); err != nil {
		return fmt.Errorf("clear 2fa: %w", err)
	}

	var passkeys int
	database.QueryRow("SELECT COUNT(*) FROM webauthn_credentials WHERE user_id=?", id).Scan(&passkeys) //nolint:errcheck

	fmt.Printf("\n=== 2FA reset for %q ===\n"+
		"  The old authenticator secret is gone; codes from it no longer work.\n", username)
	if passkeys > 0 {
		fmt.Printf("  %d passkey(s) left in place — that account can still sign in with one.\n", passkeys)
	} else {
		fmt.Printf("  This account now has NO second factor. Set one up at the next sign-in.\n")
	}
	fmt.Printf("  Settings -> Security -> Enable 2FA shows a QR code to scan.\n" +
		"=========================================\n\n")
	return nil
}
