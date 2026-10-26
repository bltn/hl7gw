package store

import (
	"context"
	"time"
)

const (
	pruneBatch            = 1000
	deleteExpiredMessages = "DELETE FROM messages WHERE id IN (SELECT id FROM messages WHERE received_at < ? LIMIT ?)"
)

func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	var deleted int64
	for {
		n, err := s.pruneBatch(ctx, before.UnixMilli())
		deleted += n
		if err != nil || n < pruneBatch {
			return deleted, err
		}
	}
}

func (s *Store) pruneBatch(ctx context.Context, before int64) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	res, err := s.db.ExecContext(ctx, deleteExpiredMessages, before, pruneBatch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
