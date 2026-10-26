package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bltn/hl7gw/internal/record"
)

type MessagesPage struct {
	Messages []record.Message
	Total    int
}

func (s *Store) ListMessages(ctx context.Context, pageNumber, pageSize int) (MessagesPage, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MessagesPage{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var page MessagesPage
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM messages").Scan(&page.Total); err != nil {
		return MessagesPage{}, err
	}

	page.Messages, err = readMessages(ctx, tx, selectMessages+" ORDER BY id LIMIT ? OFFSET ?",
		pageSize, (pageNumber-1)*pageSize)
	if err != nil || len(page.Messages) == 0 {
		return page, err
	}

	byID := make(map[int64]*record.Message, len(page.Messages))
	for i := range page.Messages {
		byID[page.Messages[i].ID] = &page.Messages[i]
	}
	err = readAttempts(ctx, tx, page.Messages[0].ID, page.Messages[len(page.Messages)-1].ID, byID)
	return page, err
}

const selectMessages = "SELECT id, received_at, created_at, updated_at, raw, xml, transform_error, ack, ack_outcome FROM messages"

func readMessages(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]record.Message, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []record.Message
	for rows.Next() {
		var m record.Message
		var receivedAt, createdAt, updatedAt int64
		var xml, terr, ack, ackOutcome sql.NullString
		if err := rows.Scan(&m.ID, &receivedAt, &createdAt, &updatedAt, &m.Raw, &xml, &terr, &ack, &ackOutcome); err != nil {
			return nil, err
		}
		m.ReceivedAt = time.UnixMilli(receivedAt)
		m.CreatedAt = time.Unix(createdAt, 0)
		m.UpdatedAt = time.Unix(updatedAt, 0)
		m.XML = nullable(xml)
		m.TransformError = nullable(terr)
		m.Ack = nullable(ack)
		m.AckOutcome = nullable(ackOutcome)
		m.Attempts = []record.Attempt{}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func readAttempts(ctx context.Context, tx *sql.Tx, firstID, lastID int64, byID map[int64]*record.Message) error {
	rows, err := tx.QueryContext(ctx,
		"SELECT message_id, attempted_at, created_at, updated_at, outcome, response_status_code, response_body, payload, origin FROM delivery_attempts WHERE message_id BETWEEN ? AND ? ORDER BY id",
		firstID, lastID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var messageID, attemptedAt, createdAt, updatedAt int64
		var status sql.NullInt64
		var body sql.NullString
		var a record.Attempt
		if err := rows.Scan(&messageID, &attemptedAt, &createdAt, &updatedAt, &a.Outcome, &status, &body, &a.Payload, &a.Origin); err != nil {
			return err
		}
		a.AttemptedAt = time.UnixMilli(attemptedAt)
		a.CreatedAt = time.Unix(createdAt, 0)
		a.UpdatedAt = time.Unix(updatedAt, 0)
		if status.Valid {
			a.ResponseStatusCode = &status.Int64
		}
		a.ResponseBody = nullable(body)
		m := byID[messageID]
		m.Attempts = append(m.Attempts, a)
	}
	return rows.Err()
}

func nullable(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
