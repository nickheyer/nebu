package instances

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A launch records the efforts its template takes and publishes them on its endpoints from the first publish on
func TestPublishedEndpointsCarryTemplateReasoning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	text, err := os.ReadFile("../../pkg/chattemplate/testdata/effort_xhigh_medium_low.jinja")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.dir, "model", "chat_template.jinja"), text, 0o644); err != nil {
		t.Fatal(err)
	}
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if tpl := ready.GetTemplate(); !slices.Equal(tpl.GetEfforts(), []string{"low", "medium", "xhigh"}) || tpl.GetThinking() != v1.Thinking_THINKING_SWITCHED || tpl.GetThinkingSwitch() != "enable_thinking" {
		t.Fatalf("recorded template %v", tpl)
	}
	eps := n.registrar.Endpoints(ownerOf(in.GetId()))
	if len(eps) != 1 {
		t.Fatalf("published %+v", eps)
	}
	labels := eps[0].Labels
	if labels[chattemplate.LabelEfforts] != "low,medium,xhigh" || labels[chattemplate.LabelThinking] != "switched" || labels[chattemplate.LabelThinkingSwitch] != "enable_thinking" {
		t.Fatalf("labels %v", labels)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A record from before templates were probed is probed from the stored model when published and keeps the probe
func TestChatTemplateProbesUnprobedRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	rt, err := m.Runtimes.Get(fake.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	in := m.newInstance(&v1.Instance{Id: "old", Name: "old", SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, CreatedAt: timestamppb.Now(), Template: &v1.ChatTemplate{Source: "chat_template.jinja", Tools: v1.ToolSupport_TOOL_SUPPORT_NATIVE}}, rt)
	if tpl := m.chatTemplate(in); tpl.GetThinking() != v1.Thinking_THINKING_NONE || tpl.GetReasoningError() != "" || tpl.GetTools() != v1.ToolSupport_TOOL_SUPPORT_NATIVE {
		t.Fatalf("probed %v", tpl)
	}
	if kept := in.snapshot().GetTemplate(); kept.GetThinking() != v1.Thinking_THINKING_NONE {
		t.Fatalf("the record kept %v", kept)
	}
	unread := m.newInstance(&v1.Instance{Id: "unread", Name: "unread", RuntimeId: fake.RuntimeID, CreatedAt: timestamppb.Now(), Template: &v1.ChatTemplate{Error: "manifest: gone"}}, rt)
	if tpl := m.chatTemplate(unread); tpl.GetError() != "manifest: gone" || chattemplate.Probed(tpl) {
		t.Fatalf("an unread template stays unread: %v", tpl)
	}
}
