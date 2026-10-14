package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Migrator wraps golang-migrate with an iofs source (the embedded
// migrations/*.sql) and the pgx/v5 database driver. It owns its own
// connection pool; call Close when done.
type Migrator struct {
	m   *migrate.Migrate
	log *slog.Logger
}

// NewMigrator opens a dedicated connection to dsn and prepares migrations from fsys.
func NewMigrator(dsn string, fsys fs.FS, log *slog.Logger) (*Migrator, error) {
	if log == nil {
		log = slog.Default()
	}
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: source: %w", err)
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate: open db: %w", err)
	}
	drv, err := migratepgx.WithInstance(sqlDB, &migratepgx.Config{MigrationsTable: "schema_migrations"})
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate: driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", drv)
	if err != nil {
		_ = drv.Close()
		return nil, fmt.Errorf("migrate: init: %w", err)
	}
	m.Log = migrateLogger{log}
	return &Migrator{m: m, log: log}, nil
}

// Up applies all pending migrations. No pending migrations is not an error.
func (x *Migrator) Up() error { return ignoreNoChange(x.m.Up()) }

// Down rolls back every applied migration.
func (x *Migrator) Down() error { return ignoreNoChange(x.m.Down()) }

// Steps applies n migrations forward (n > 0) or rolls back -n (n < 0).
func (x *Migrator) Steps(n int) error { return ignoreNoChange(x.m.Steps(n)) }

// Force sets the version without running migrations (clears the dirty flag).
func (x *Migrator) Force(version int) error { return x.m.Force(version) }

// Version returns the current version; ok is false when no migration was applied.
func (x *Migrator) Version() (version uint, dirty bool, ok bool, err error) {
	v, d, err := x.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	return v, d, true, nil
}

// Close releases the source and database connection.
func (x *Migrator) Close() error {
	srcErr, dbErr := x.m.Close()
	return errors.Join(srcErr, dbErr)
}

func ignoreNoChange(err error) error {
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

type migrateLogger struct{ l *slog.Logger }

func (m migrateLogger) Printf(format string, v ...any) {
	m.l.Info(strings.TrimSpace(fmt.Sprintf(format, v...)), "component", "migrate")
}
func (m migrateLogger) Verbose() bool { return false }
