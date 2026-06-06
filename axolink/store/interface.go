package store

import "context"

type Store interface {
	Ping(context.Context) error
}
