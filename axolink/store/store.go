package store

import "context"

type NopStore struct{}

func (NopStore) Ping(context.Context) error {
	return nil
}
