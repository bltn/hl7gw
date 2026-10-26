package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bltn/hl7gw/internal/record"
)

var ErrMessageNotFound = errors.New("message not found")

func (s *Store) GetMessage(ctx context.Context, id int64) (record.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return record.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()

	msgs, err := readMessages(ctx, tx, selectMessages+" WHERE id = ?", id)
	if err != nil {
		return record.Message{}, err
	}
	if len(msgs) == 0 {
		return record.Message{}, ErrMessageNotFound
	}

	m := msgs[0]
	err = readAttempts(ctx, tx, id, id, map[int64]*record.Message{id: &m})
	return m, err
}
