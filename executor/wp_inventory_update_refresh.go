package executor

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const wpInventoryUpdateRefreshTimeout = 5 * time.Second

type wpInventoryRefreshRequester interface {
	Request(context.Context, int, time.Time) error
}

type wpInventoryUpdateRefreshRequester struct {
	store *wpInventoryStore
}

func newWPInventoryUpdateRefreshRequester(db *sql.DB) (*wpInventoryUpdateRefreshRequester, error) {
	store, err := newWPInventoryStoreWithDB(db)
	if err != nil {
		return nil, err
	}
	return &wpInventoryUpdateRefreshRequester{store: store}, nil
}

func (r *wpInventoryUpdateRefreshRequester) Request(ctx context.Context, siteID int, requestedAt time.Time) error {
	if r == nil || r.store == nil || siteID <= 0 || requestedAt.IsZero() {
		return errors.New("invalid wordpress inventory refresh request")
	}
	_, _, err := r.store.enqueueEligibleUpdateFollowup(ctx, siteID, requestedAt.UTC())
	return err
}
