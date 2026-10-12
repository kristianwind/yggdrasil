package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/migrate"
	"github.com/kristianwind/yggdrasil/internal/panelbackup"
)

// `yggdrasil panel-restore -i <file>` puts a panel back from one of its own
// encrypted backups.
//
// A command and not a button, deliberately. Restoring means replacing the
// database the running panel is reading and the secret key it decrypts
// everything with — there is no way to do that from inside the thing being
// replaced. It also means the one path that matters most works when the panel
// does not start at all, which is when an operator reaches for it.
func runPanelRestore(args []string) error {
	fs := flag.NewFlagSet("panel-restore", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/yggdrasil/config.yaml", "path to config.yaml")
	in := fs.String("i", "", "the .yggpanel.enc archive to restore")
	passFlag := fs.String("passphrase", "", "archive passphrase (omit to be prompted; a flag lands in your shell history)")
	apply := fs.Bool("apply", false, "actually write; without this the archive is only inspected")
	fs.Parse(args)

	if *in == "" {
		return fmt.Errorf("usage: yggdrasil panel-restore -i <file.yggpanel.enc> [--apply]")
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	pass := *passFlag
	if pass == "" {
		fmt.Fprint(os.Stderr, "Passphrase: ")
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("read passphrase: %w", err)
		}
		pass = strings.TrimSpace(string(b))
	}

	plain, err := panelbackup.Open(bytes.NewReader(raw), pass)
	if err != nil {
		if errors.Is(err, panelbackup.ErrWrongPassphrase) {
			return fmt.Errorf("%w — this is the passphrase set under Settings → Panel backup, not a panel login", err)
		}
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if !*apply {
		// Inspect first. An operator restoring at 3am should be able to see what
		// is in the file before it replaces anything.
		man, err := migrate.Inspect(bytes.NewReader(plain))
		if err != nil {
			return err
		}
		fmt.Printf("Archive opened.\n  exported:   %s\n  servers:    %d\n  database:   %s\n  secret key: %s\n",
			man.ExportedAt, len(man.Servers), man.DBFile, keyState(cfg.Auth.SecretKey, man.SecretKey))
		for _, sv := range man.Servers {
			fmt.Printf("    - %s\n", sv.Name)
		}
		fmt.Printf("\nNothing was written. Re-run with --apply to restore into %s.\n", cfg.Database.Path)
		return nil
	}

	// The database is replaced wholesale, so keep what was there. An operator who
	// restores the wrong archive at 3am has one move left, and it should not be
	// "hope".
	if _, err := os.Stat(cfg.Database.Path); err == nil {
		aside := cfg.Database.Path + ".before-restore"
		if err := os.Rename(cfg.Database.Path, aside); err != nil {
			return fmt.Errorf("could not set the current database aside: %w", err)
		}
		fmt.Printf("Current database kept at %s\n", aside)
	}
	man, err := migrate.Import(bytes.NewReader(plain), cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	fmt.Printf("Restored %s into %s\n", filepath.Base(cfg.Database.Path), cfg.Database.Path)

	if man.SecretKey != "" && man.SecretKey != cfg.Auth.SecretKey {
		// Said rather than done. Rewriting config.yaml under a running service is
		// not this command's business, and an operator who pastes it knowingly is
		// in a better position than one who finds it was changed for them.
		fmt.Printf("\n🔴 This archive was made on a panel with a DIFFERENT secret key.\n"+
			"   Every stored credential in the restored database is encrypted with the archive's key,\n"+
			"   so put this into %s under auth.secret_key and restart:\n\n     secret_key: %q\n\n",
			*cfgPath, man.SecretKey)
	}
	return nil
}

func keyState(current, inArchive string) string {
	switch {
	case inArchive == "":
		return "MISSING — stored credentials cannot be decrypted from this archive"
	case current == inArchive:
		return "matches this host's"
	default:
		return "different from this host's (the command will tell you what to change)"
	}
}
