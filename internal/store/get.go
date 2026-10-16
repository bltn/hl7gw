package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bltn/hl7gw/internal/journal"
)

var ErrMessageNotFound = errors.New("message not found")

func (s *Store) GetMessage(ctx context.Context, id int64) (journal.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return journal.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()

	msgs, err := readMessages(ctx, tx, selectMessages+" WHERE id = ?", id)
	if err != nil {
		return journal.Message{}, err
	}
	if len(msgs) == 0 {
		return journal.Message{}, ErrMessageNotFound
	}

	m := msgs[0]
	err = readAttempts(ctx, tx, id, id, map[int64]*journal.Message{id: &m})
	return m, err
}
