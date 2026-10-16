package harness

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

type MessageRow struct {
	ID             int64
	ReceivedAt     int64
	Raw            string
	XML            *string
	TransformError *string
	AckSentAt      *int64
	Ack            *string
	AckOutcome     *string
	CreatedAt      int64
	UpdatedAt      int64
}

type DeliveryAttemptRow struct {
	ID                 int64
	MessageID          int64
	AttemptedAt        int64
	Outcome            string
	ResponseStatusCode *int64
	ResponseBody       *string
	Payload            string
	Origin             string
	CreatedAt          int64
	UpdatedAt          int64
}

func ReadMessages(dbPath string) ([]MessageRow, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(context.Background(), "SELECT id, received_at, raw, xml, transform_error, ack_sent_at, ack, ack_outcome, created_at, updated_at FROM messages ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []MessageRow
	for rows.Next() {
		var m MessageRow
		if err := rows.Scan(&m.ID, &m.ReceivedAt, &m.Raw, &m.XML, &m.TransformError, &m.AckSentAt, &m.Ack, &m.AckOutcome, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func ReadDeliveryAttempts(dbPath string) ([]DeliveryAttemptRow, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(context.Background(), "SELECT id, message_id, attempted_at, outcome, response_status_code, response_body, payload, origin, created_at, updated_at FROM delivery_attempts ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var attempts []DeliveryAttemptRow
	for rows.Next() {
		var a DeliveryAttemptRow
		if err := rows.Scan(&a.ID, &a.MessageID, &a.AttemptedAt, &a.Outcome, &a.ResponseStatusCode, &a.ResponseBody, &a.Payload, &a.Origin, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}

func ExecSQL(dbPath string, stmts ...string) error {
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	for _, stmt := range stmts {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt, err)
		}
	}
	return nil
}

func HoldWriteLock(t *testing.T, dbPath string) (func(), error) {
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, err
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			_ = conn.Close()
			_ = db.Close()
		})
	}
	t.Cleanup(release)
	return release, nil
}
