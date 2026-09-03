package runtime

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func devicesManifest() *v1.RuntimeManifest {
	return &v1.RuntimeManifest{
		Id: "rt",
		Launch: &v1.Launch{
			Command: "{{.install.path}}",
			Args:    []string{"--port", "{{.port}}"},
			Env: map[string]string{
				"VISIBLE": `{{range $i, $d := .devices}}{{if $i}},{{end}}{{index $d.facts "index"}}{{end}}`,
				"ALWAYS":  "1",
			},
		},
	}
}

func TestRenderDevicesEnv(t *testing.T) {
	reg, err := New([]*v1.RuntimeManifest{devicesManifest()})
	if err != nil {
		t.Fatal(err)
	}
	rt, _ := reg.Get("rt")
	in := RenderInput{Name: "n", Params: map[string]any{}, Host: "127.0.0.1", Port: 1, Install: map[string]string{"path": "/bin/x"}}
	out, err := rt.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Env["VISIBLE"]; ok {
		t.Fatalf("empty env should be dropped: %v", out.Env)
	}
	if out.Env["ALWAYS"] != "1" {
		t.Fatal("non empty env kept")
	}
	in.Devices = []map[string]any{{"facts": map[string]any{"index": "2"}}, {"facts": map[string]any{"index": "0"}}}
	out, err = rt.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Env["VISIBLE"] != "2,0" {
		t.Fatalf("devices env %q", out.Env["VISIBLE"])
	}
}
