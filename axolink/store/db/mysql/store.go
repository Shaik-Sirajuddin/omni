package mysql

import (
	"context"

	"github.com/Shaik-Sirajuddin/memory/mcp/store"
)

type Store struct{}

func New() store.Store {
	return Store{}
}

func (Store) Ping(context.Context) error {
	return nil
}
