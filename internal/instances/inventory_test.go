package instances

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/instances/fake"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
)

// The runner, conversion and tasks the runtime logs are recorded for every value left to it, and the inventory follows them
func TestInventoryFollowsResolvedRunner(t *testing.T) {
	cases := []struct {
		name                   string
		params                 map[string]string
		runner, convert, tasks string
		modalities             string
		logged                 []string
	}{
		{"generative model left to auto", nil, "generate", "none", "generate", "text", []string{"Supported tasks: ('generate',)"}},
		{"pooling model left to auto", map[string]string{fake.ParamArchitecture: fake.ArchPooling}, "pooling", "none", "embed,token_embed", runtimes.ModalityEmbedding, []string{"Resolved `--runner auto` to `--runner pooling`"}},
		{"classifier left to auto", map[string]string{fake.ParamArchitecture: fake.ArchClassifier}, "pooling", "classify", "classify,token_classify", runtimes.ModalityClassify, []string{"Resolved `--runner auto` to `--runner pooling`", "Resolved `--convert auto` to `--convert classify`"}},
		{"conversion pinned to embed", map[string]string{fake.ParamConvert: "embed"}, "pooling", "embed", "embed,token_embed", runtimes.ModalityEmbedding, []string{"Resolved `--runner auto` to `--runner pooling`"}},
		{"runner pinned to generate", map[string]string{fake.ParamRunner: "generate", fake.ParamArchitecture: fake.ArchPooling}, "generate", "none", "generate", "text", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSeatNode(t, ctx)
			m := n.manager(t, ctx, nil)
			in, _, err := m.Run(ctx, soloRun(c.params))
			if err != nil {
				t.Fatal(err)
			}
			ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			p := ready.GetParams()
			if p["runner"] != c.runner || p["convert"] != c.convert || p[runtimes.VLLMSupportedTasks] != c.tasks {
				t.Fatalf("recorded runner %q convert %q tasks %q", p["runner"], p["convert"], p[runtimes.VLLMSupportedTasks])
			}
			if got := strings.Join(ready.GetInventory().GetModalities(), ","); got != c.modalities {
				t.Fatalf("modalities %s, want %s", got, c.modalities)
			}
			mem, ok := n.registrar.Membership(ownerOf(in.GetId()))
			if !ok || !proto.Equal(mem.Inventory, ready.GetInventory()) {
				t.Fatalf("published inventory %v, recorded %v", mem.Inventory, ready.GetInventory())
			}
			lines := n.launcher.Proc("solo").Log().Tail(0)
			for _, want := range c.logged {
				if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, want) }) {
					t.Fatalf("the runtime log lacks %q: %v", want, lines)
				}
			}
			if pinned := c.params[fake.ParamRunner]; pinned != "" && slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "Resolved `--runner auto`") }) {
				t.Fatalf("a pinned runner was resolved: %v", lines)
			}
			if _, err := m.Stop(ctx, in.GetId()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A record stored before its inventory was kept is adopted with the inventory its launch reports, persisted and published
func TestInventoryRestoredAtAdoption(t *testing.T) {
	cases := []struct {
		name string
		run  func(dir string) *v1.RunRequest
		proc string
	}{
		{"solo", func(string) *v1.RunRequest {
			return soloRun(map[string]string{fake.ParamArchitecture: fake.ArchPooling})
		}, "solo"},
		{"seat", func(dir string) *v1.RunRequest { return seatRun(runtimes.RoleHead, 0, false, dir) }, "chain/head0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSeatNode(t, ctx)
			first := n.manager(t, ctx, nil)
			in, _, err := first.Run(ctx, c.run(n.dir))
			if err != nil {
				t.Fatal(err)
			}
			ready := waitState(t, first, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			want := ready.GetInventory()
			if want == nil {
				t.Fatal("the launch recorded no inventory")
			}
			if c.name == "seat" {
				dropGuard(t, first, in.GetId())
			}
			stored := proto.Clone(ready).(*v1.Instance)
			stored.Inventory = nil
			if err := n.db.PutInstance(ctx, stored); err != nil {
				t.Fatal(err)
			}
			second := n.manager(t, ctx, nil)
			second.Registrar = fake.NewRegistrar()
			if err := second.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			adopted := waitState(t, second, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			if n.launcher.Launches(c.proc) != 1 || adopted.GetPid() != ready.GetPid() {
				t.Fatalf("relaunched: %d launches, pid %d then %d", n.launcher.Launches(c.proc), ready.GetPid(), adopted.GetPid())
			}
			if !proto.Equal(adopted.GetInventory(), want) {
				t.Fatalf("restored inventory %v, the launch reported %v", adopted.GetInventory(), want)
			}
			list, err := n.db.ListInstances(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, rec := range list {
				if rec.GetId() == in.GetId() && !proto.Equal(rec.GetInventory(), want) {
					t.Fatalf("persisted inventory %v", rec.GetInventory())
				}
			}
			if c.name == "solo" {
				waitFor(t, "the adopted instance published with its inventory", func() bool {
					mem, ok := second.Registrar.(*fake.Registrar).Membership(ownerOf(in.GetId()))
					return ok && proto.Equal(mem.Inventory, want)
				})
			}
			if _, err := second.Stop(ctx, in.GetId()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
