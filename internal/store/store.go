package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/record"
	_ "modernc.org/sqlite"
)

const (
	dataDirPerm os.FileMode = 0o750
	opTimeout               = 5 * time.Second
)

type Store struct {
	db *sql.DB
}

func OpenAll(ctx context.Context, cfg config.Config) (map[string]*Store, error) {
	if err := os.MkdirAll(cfg.DataDir, dataDirPerm); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	stores := make(map[string]*Store, len(cfg.Channels))
	for _, ch := range cfg.Channels {
		st, err := Open(ctx, filepath.Join(cfg.DataDir, ch.Name+".db"))
		if err != nil {
			CloseAll(stores)
			return nil, fmt.Errorf("channel %s: %w", ch.Name, err)
		}
		stores[ch.Name] = st
	}

	return stores, nil
}

func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	var v int
	return s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
}

func CloseAll(stores map[string]*Store) {
	for _, st := range stores {
		_ = st.Close()
	}
}

func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)"
	db, _ := sql.Open("sqlite", dsn)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	if err := migrate(ctx, db, migrations); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}

	return &Store{db: db}, nil
}

func (s *Store) CreateMessage(ctx context.Context, receivedAt time.Time, raw string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	var id int64
	err := s.db.QueryRowContext(ctx,
		"INSERT INTO messages (received_at, raw) VALUES (?, ?) RETURNING id",
		receivedAt.UnixMilli(), raw).Scan(&id)
	return id, err
}

func (s *Store) RecordTransform(ctx context.Context, id int64, xml string, terr record.TransformError) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	_, err := s.db.ExecContext(ctx,
		"UPDATE messages SET xml = NULLIF(?, ''), transform_error = NULLIF(?, '') WHERE id = ?",
		xml, string(terr), id)
	return err
}

func (s *Store) RecordDeliveryAttempt(ctx context.Context, messageID int64, a record.Attempt) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	_, err := s.db.ExecContext(ctx,
		"INSERT INTO delivery_attempts (message_id, attempted_at, outcome, response_status_code, response_body, payload, origin) VALUES (?, ?, ?, ?, ?, ?, ?)",
		messageID, a.AttemptedAt.UnixMilli(), string(a.Outcome), a.ResponseStatusCode, a.ResponseBody, a.Payload, string(a.Origin))
	return err
}

func (s *Store) RecordAck(ctx context.Context, id int64, ack string, sentAt time.Time, sent bool) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	_, err := s.db.ExecContext(ctx,
		"UPDATE messages SET ack = ?, ack_sent_at = ?, ack_outcome = ? WHERE id = ?",
		ack, sentAt.UnixMilli(), string(record.OutcomeOf(sent)), id)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}
