// Package all wires every format reader by id.
package all

import (
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/gguf"
	"github.com/nickheyer/nebu/pkg/formats/safetensors"
)

// Returns constructors for every supported format
func Constructors() formats.Constructors {
	return formats.Constructors{
		"gguf":        gguf.New,
		"safetensors": safetensors.New,
	}
}
