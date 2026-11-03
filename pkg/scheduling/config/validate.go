package config

import (
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Builds the configuration against a fresh store, index and predictor, refusing whatever Load would refuse
func Validate(data []byte) error {
	index := prefixcache.New(prefixcache.Config{})
	defer index.Close()
	_, _, err := Load(data, registry.Deps{Store: datalayer.NewStore(), Indexer: index, Predictor: latency.New(latency.Config{})})
	return err
}
