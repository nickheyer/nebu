package datalayer

import "errors"

var (
	ErrDuplicateSource    = errors.New("a data source of that type is already registered")
	ErrUnknownEndpoint    = errors.New("unknown endpoint")
	ErrUnknownPool        = errors.New("unknown pool")
	ErrDuplicateCollector = errors.New("a collector of that name is already subscribed")
)
