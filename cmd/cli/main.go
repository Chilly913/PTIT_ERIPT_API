// Command cli runs operational tasks.
//
//	cli migrate up                 apply all pending migrations
//	cli migrate down [n|all]       roll back n migrations (default 1) or all
//	cli migrate steps <n>          apply (n>0) or roll back (n<0) n migrations
//	cli migrate version            print the current version
//	cli migrate force <version>    set version and clear the dirty flag
//
// Flags (before the subcommand): -dir <path> reads migrations from a directory
// instead of the files embedded at build time.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
	"github.com/Chilly913/PTIT_ERIPT_API/migrations"
)

const usage = `usage: cli [-dir path] <command> [args]

commands:
  migrate up                 apply all pending migrations
  migrate down [n|all]       roll back n migrations (default 1) or all
  migrate steps <n>          apply (n>0) or roll back (n<0) n migrations
  migrate version            print the current schema version
  migrate force <version>    set the version without running SQL (fix dirty state)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cli:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fset := flag.NewFlagSet("cli", flag.ContinueOnError)
	dir := fset.String("dir", "", "read migrations from this directory instead of the embedded files")
	fset.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	if err := fset.Parse(args); err != nil {
		return err
	}
	args = fset.Args()
	if len(args) == 0 {
		fset.Usage()
		return errors.New("missing command")
	}
	switch args[0] {
	case "migrate":
		return migrateCmd(args[1:], *dir)
	default:
		fset.Usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func migrateCmd(args []string, dir string) error {
	if len(args) == 0 {
		return errors.New("migrate: missing subcommand (up|down|steps|version|force)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logx.New(cfg.Log, cfg.App)

	var src fs.FS = migrations.FS
	if dir != "" {
		src = os.DirFS(dir)
	}
	m, err := db.NewMigrator(cfg.DB.DSN(), src, log)
	if err != nil {
		return err
	}
	defer m.Close()

	intArg := func(i int, name string) (int, error) {
		if len(args) <= i {
			return 0, fmt.Errorf("migrate %s: missing argument", name)
		}
		n, err := strconv.Atoi(args[i])
		if err != nil {
			return 0, fmt.Errorf("migrate %s: %q is not an integer", name, args[i])
		}
		return n, nil
	}

	switch args[0] {
	case "up":
		if err := m.Up(); err != nil {
			return err
		}
	case "down":
		switch {
		case len(args) > 1 && args[1] == "all":
			if err := m.Down(); err != nil {
				return err
			}
		default:
			n := 1
			if len(args) > 1 {
				if n, err = intArg(1, "down"); err != nil {
					return err
				}
				if n <= 0 {
					return errors.New("migrate down: n must be > 0")
				}
			}
			if err := m.Steps(-n); err != nil {
				return err
			}
		}
	case "steps":
		n, err := intArg(1, "steps")
		if err != nil {
			return err
		}
		if err := m.Steps(n); err != nil {
			return err
		}
	case "force":
		v, err := intArg(1, "force")
		if err != nil {
			return err
		}
		if err := m.Force(v); err != nil {
			return err
		}
	case "version":
	default:
		return fmt.Errorf("migrate: unknown subcommand %q", args[0])
	}
	return printVersion(m)
}

func printVersion(m *db.Migrator) error {
	v, dirty, ok, err := m.Version()
	if err != nil {
		return err
	}
	switch {
	case !ok:
		fmt.Println("version: none (no migrations applied)")
	case dirty:
		fmt.Printf("version: %d (dirty)\n", v)
	default:
		fmt.Printf("version: %d\n", v)
	}
	return nil
}
