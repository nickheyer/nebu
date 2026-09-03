// Package all wires every source implementation by kind.
package all

import (
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/sources/huggingface"
	"github.com/nickheyer/nebu/pkg/sources/local"
)

// Returns constructors for every supported source kind
func Constructors() sources.Constructors {
	return sources.Constructors{
		v1.SourceKind_SOURCE_KIND_HUGGINGFACE: huggingface.New,
		v1.SourceKind_SOURCE_KIND_LOCAL:       local.New,
	}
}
