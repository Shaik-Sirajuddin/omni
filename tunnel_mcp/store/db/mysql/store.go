package mysql

import (
	"context"

	"github.com/Shaik-Sirajuddin/omni/mcp/store"
)

type Store struct{}

func New() store.Store {
	return Store{}
}

func (Store) Ping(context.Context) error {
	return nil
}
