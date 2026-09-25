package config

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Mesh settings are checked at load: exposure names a mode, listen is host:port, advertise a host
func TestCheckMesh(t *testing.T) {
	good := []*v1.MeshConfig{
		{Exposure: "guard"},
		{Exposure: "Direct", Listen: "0.0.0.0:8485", Advertise: "10.0.0.5"},
		{Exposure: "guard", Listen: "[::]:8485", Advertise: "[fd00::5]:8485"},
		{Exposure: "guard", Advertise: "box.local:8485"},
	}
	for _, m := range good {
		if err := checkMesh(m); err != nil {
			t.Fatalf("%v: %v", m, err)
		}
	}
	if m := (&v1.MeshConfig{Exposure: "Direct"}); checkMesh(m) != nil || m.GetExposure() != "direct" {
		t.Fatalf("exposure is lowered: %v", m)
	}
	bad := []*v1.MeshConfig{
		{Exposure: "open"},
		{Exposure: "guard", Listen: "8485"},
		{Exposure: "guard", Listen: "0.0.0.0"},
		{Exposure: "guard", Advertise: "10.0.0.5 8485"},
		{Exposure: "guard", Advertise: "http://box"},
	}
	for _, m := range bad {
		if err := checkMesh(m); err == nil {
			t.Fatalf("%v accepted", m)
		}
	}
}
