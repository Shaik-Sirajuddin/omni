package engine

import "context"

type Engine interface {
	Run(context.Context) error
}
