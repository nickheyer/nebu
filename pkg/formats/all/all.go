// Package all lists every format nebu reads.
package all

import (
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusers"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/formats/gguf"
	"github.com/nickheyer/nebu/pkg/formats/nemo"
	"github.com/nickheyer/nebu/pkg/formats/peft"
	"github.com/nickheyer/nebu/pkg/formats/relay"
	"github.com/nickheyer/nebu/pkg/formats/safetensors"
)

// Every format, the registry ordering them by priority
func Formats() []formats.Format {
	return append([]formats.Format{gguf.Format{}, diffusers.Format{}, peft.Format{}, safetensors.Format{}, nemo.Packed{}, nemo.Directory{}, diffusion.Format{}}, relay.Formats()...)
}

// The registry over every format
func Registry() (*formats.Registry, error) {
	return formats.New(Formats())
}
