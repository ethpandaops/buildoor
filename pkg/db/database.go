// Package db provides optional SQLite-backed persistence for buildoor runtime
// state: settings overrides, won blocks, validator registrations, proposer
// preferences and an audit log. It mirrors the database patterns used by the
// sibling spamoor project (glebarez/go-sqlite + sqlx + goose migrations).
//
// Persistence is opt-in: when the configured file path is empty the Database
// runs in a disabled mode where every method is a no-op (reads return empty,
// writes are dropped). This keeps callers free of nil-checks while preserving
// the original in-memory-only behaviour when --state-db is not set.
package db

import (
	"embed"
	"errors"
	"fmt"
	"sync"

	_ "github.com/glebarez/go-sqlite" // pure-Go sqlite driver (no cgo)
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	"github.com/sirupsen/logrus"
)

//go:embed schema/*.sql
var embedSchema embed.FS

// Config configures the SQLite database connection.
type Config struct {
	// File is the path to the SQLite database file. When empty the database
	// is disabled and all operations become no-ops.
	File string
	// MaxOpenConns and MaxIdleConns bound the connection pool. Zero values
	// fall back to sensible defaults.
	MaxOpenConns int
	MaxIdleConns int
}

// Database wraps a SQLite connection and exposes the buildoor persistence
// repositories. A single connection is shared for reads and writes; writes are
// serialised by writerMutex to avoid "database is locked" under WAL.
type Database struct {
	config      *Config
	logger      logrus.FieldLogger
	enabled     bool
	readerDB    *sqlx.DB
	writerDB    *sqlx.DB
	writerMutex sync.Mutex
}

// NewDatabase creates a Database. When config.File is empty the database is
// disabled and Init/repository methods become no-ops.
func NewDatabase(config *Config, logger logrus.FieldLogger) *Database {
	return &Database{
		config:  config,
		logger:  logger.WithField("module", "db"),
		enabled: config.File != "",
	}
}

// Enabled reports whether persistence is active (a file path was configured).
func (d *Database) Enabled() bool {
	return d != nil && d.enabled
}

// Init opens the SQLite connection (WAL mode) and applies embedded migrations.
// It is a no-op when the database is disabled.
func (d *Database) Init() error {
	if !d.enabled {
		d.logger.Info("state-db disabled (no --state-db path configured); runtime changes will not persist")
		return nil
	}

	if d.config.MaxOpenConns == 0 {
		d.config.MaxOpenConns = 50
	}

	if d.config.MaxIdleConns == 0 {
		d.config.MaxIdleConns = 10
	}

	if d.config.MaxOpenConns < d.config.MaxIdleConns {
		d.config.MaxIdleConns = d.config.MaxOpenConns
	}

	d.logger.WithField("file", d.config.File).Info("initializing state-db (sqlite)")

	dbConn, err := sqlx.Open("sqlite", fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", d.config.File))
	if err != nil {
		return fmt.Errorf("error opening sqlite database: %w", err)
	}

	dbConn.SetConnMaxIdleTime(0)
	dbConn.SetConnMaxLifetime(0)
	dbConn.SetMaxOpenConns(d.config.MaxOpenConns)
	dbConn.SetMaxIdleConns(d.config.MaxIdleConns)

	dbConn.MustExec("PRAGMA journal_mode = WAL")

	d.readerDB = dbConn
	d.writerDB = dbConn

	if err := d.applyEmbeddedSchema(); err != nil {
		return fmt.Errorf("error applying state-db schema: %w", err)
	}

	return nil
}

// Close closes the database connection. No-op when disabled.
func (d *Database) Close() error {
	if !d.enabled || d.writerDB == nil {
		return nil
	}

	if err := d.writerDB.Close(); err != nil {
		return fmt.Errorf("error closing state-db: %w", err)
	}

	return nil
}

// RunDBTransaction runs handler inside a write transaction, serialised against
// other writers. Returns an error when the database is disabled.
func (d *Database) RunDBTransaction(handler func(tx *sqlx.Tx) error) error {
	if !d.enabled {
		return errors.New("state-db is disabled")
	}

	d.writerMutex.Lock()
	defer d.writerMutex.Unlock()

	tx, err := d.writerDB.Beginx()
	if err != nil {
		return fmt.Errorf("error starting db transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := handler(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error committing db transaction: %w", err)
	}

	return nil
}

// applyEmbeddedSchema runs all pending goose migrations from the embedded
// schema directory.
func (d *Database) applyEmbeddedSchema() error {
	goose.SetLogger(&gooseLogger{logger: d.logger})
	goose.SetBaseFS(embedSchema)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}

	return goose.Up(d.writerDB.DB, "schema", goose.WithAllowMissing())
}

// gooseLogger adapts logrus to goose's logger interface.
type gooseLogger struct {
	logger logrus.FieldLogger
}

func (l *gooseLogger) Printf(format string, v ...any) {
	l.logger.Infof(format, v...)
}

func (l *gooseLogger) Fatalf(format string, v ...any) {
	l.logger.Fatalf(format, v...)
}
