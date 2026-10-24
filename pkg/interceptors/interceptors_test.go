package interceptors

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A request stage guard that records whether it ran
type guard struct{ ran *bool }

func (guard) Stage() Stage { return Request }

func (guard) Guard() bool { return true }

func (g guard) Intercept(x *Exchange) (bool, error) {
	*g.ran = true
	return false, nil
}

func turn(role, text string) chat.Message {
	return chat.Message{Role: role, Parts: []chat.Part{{Type: "text", Text: text}}}
}

// Roles and texts of the turns, as role=text joined by commas
func turns(c *chat.Request) string {
	var out []string
	for _, m := range c.Messages {
		out = append(out, m.Role+"="+chat.TextOf(m.Parts))
	}
	return strings.Join(out, ",")
}

func encoded(text string) string { return base64.StdEncoding.EncodeToString([]byte(text)) }

// A pinned system prompt turns off the guards after it, and guards run when nothing pins
func TestPinTurnsGuardsOff(t *testing.T) {
	for name, tc := range map[string]struct {
		header string
		ran    bool
		want   string
	}{
		"pinned":   {encoded("rules"), false, "system=rules,user=hi"},
		"unpinned": {"", true, "system=a,user=hi"},
	} {
		t.Run(name, func(t *testing.T) {
			var ran bool
			x := &Exchange{Header: http.Header{}, Req: &chat.Request{Kind: chat.Chat, Messages: []chat.Message{turn("system", "a"), turn("user", "hi")}}}
			if tc.header != "" {
				x.Header.Set(SystemHeader, tc.header)
			}
			if _, err := (Chain{System{}, guard{&ran}, Fold{}}).Run(Request, x); err != nil {
				t.Fatal(err)
			}
			if ran != tc.ran || x.Pinned == tc.ran {
				t.Fatalf("guard ran %v pinned %v", ran, x.Pinned)
			}
			if got := turns(x.Req); got != tc.want {
				t.Fatalf("turns %s, want %s", got, tc.want)
			}
		})
	}
}

// The fold runs on a pinned request, and an inbound run leaves request interceptors alone
func TestFoldRunsPinned(t *testing.T) {
	x := &Exchange{Header: http.Header{SystemHeader: {encoded("rules")}}, Pinned: true, SystemMode: v1.SystemMessages_SYSTEM_MESSAGES_USER, Req: &chat.Request{Kind: chat.Chat, Messages: []chat.Message{turn("user", "hi"), turn("system", "late")}}}
	if changed, err := Default.Run(Inbound, x); err != nil || changed || turns(x.Req) != "user=hi,system=late" {
		t.Fatalf("inbound run changed %v err %v turns %s", changed, err, turns(x.Req))
	}
	x.Header.Del(SystemHeader)
	if changed, err := Default.Run(Request, x); err != nil || !changed || turns(x.Req) != "user=hi,user=late" {
		t.Fatalf("request run changed %v err %v turns %s", changed, err, turns(x.Req))
	}
}

// Ghost mode turns on for 1 or true, stays off for 0, false, or no header, and refuses anything else
func TestGhost(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "false": false, "FALSE": false, "1": true, "true": true, "True": true} {
		x := &Exchange{Header: http.Header{}}
		x.Header.Set(GhostHeader, value)
		changed, err := Default.Run(Inbound, x)
		if err != nil || changed != want || x.Ghost != want {
			t.Fatalf("%q: changed %v ghost %v err %v", value, changed, x.Ghost, err)
		}
	}
	x := &Exchange{Header: http.Header{GhostHeader: {"maybe"}}}
	if _, err := Default.Run(Inbound, x); err == nil || !strings.Contains(err.Error(), GhostHeader) || x.Ghost {
		t.Fatalf("maybe: ghost %v err %v", x.Ghost, err)
	}
}

// Bare pins for 1 or true so guards stay off, leaves 0, false or none alone and refuses the rest
func TestBare(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "TRUE": true} {
		x := &Exchange{Header: http.Header{}, Req: &chat.Request{Kind: chat.Chat, Messages: []chat.Message{turn("system", "mine"), turn("user", "hi")}}}
		x.Header.Set(BareHeader, value)
		changed, err := Default.Run(Inbound, x)
		if err != nil || changed != want || x.Bare != want || x.Pinned != want {
			t.Fatalf("%q: changed %v bare %v pinned %v err %v", value, changed, x.Bare, x.Pinned, err)
		}
		var ran bool
		if _, err := (Chain{guard{&ran}}).Run(Request, x); err != nil || ran == want || turns(x.Req) != "system=mine,user=hi" {
			t.Fatalf("%q: guard ran %v turns %s", value, ran, turns(x.Req))
		}
	}
	x := &Exchange{Header: http.Header{BareHeader: {"yes"}}}
	if _, err := Default.Run(Inbound, x); err == nil || !strings.Contains(err.Error(), BareHeader) || x.Bare || x.Pinned {
		t.Fatalf("yes: bare %v pinned %v err %v", x.Bare, x.Pinned, err)
	}
}

// The header's prompt replaces every system and developer turn, leaves embeddings alone, and must be base64 UTF-8
func TestSystem(t *testing.T) {
	chatReq := &chat.Request{Kind: chat.Chat, Messages: []chat.Message{turn("system", "a"), turn("developer", "b"), turn("user", "hi"), turn("system", "c"), turn("user", "go")}}
	x := &Exchange{Header: http.Header{SystemHeader: {encoded("one\ntwo")}}, Req: chatReq}
	if changed, err := (System{}).Intercept(x); err != nil || !changed || !x.Pinned {
		t.Fatalf("changed %v pinned %v err %v", changed, x.Pinned, err)
	}
	if got := turns(chatReq); got != "system=one\ntwo,user=hi,user=go" {
		t.Fatalf("turns %q", got)
	}
	embed := &Exchange{Header: http.Header{SystemHeader: {encoded("rules")}}, Req: &chat.Request{Kind: chat.Embed, Inputs: []string{"x"}}}
	if changed, err := (System{}).Intercept(embed); err != nil || changed || embed.Pinned || len(embed.Req.Messages) != 0 {
		t.Fatalf("embed changed %v pinned %v err %v", changed, embed.Pinned, err)
	}
	for name, value := range map[string]string{"not base64": "rules!", "not UTF-8": base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe})} {
		x := &Exchange{Header: http.Header{SystemHeader: {value}}, Req: &chat.Request{Kind: chat.Chat, Messages: []chat.Message{turn("system", "a")}}}
		if _, err := (System{}).Intercept(x); err == nil || !strings.Contains(err.Error(), SystemHeader) || x.Pinned || turns(x.Req) != "system=a" {
			t.Fatalf("%s: pinned %v err %v turns %s", name, x.Pinned, err, turns(x.Req))
		}
	}
}

// Merging late system turns with no leading one creates it, and keep mode changes nothing
func TestFold(t *testing.T) {
	x := &Exchange{SystemMode: v1.SystemMessages_SYSTEM_MESSAGES_MERGE, Req: &chat.Request{Messages: []chat.Message{turn("user", "hi"), turn("system", "a"), turn("system", "b")}}}
	if changed, _ := (Fold{}).Intercept(x); !changed || turns(x.Req) != "system=a\n\nb,user=hi" {
		t.Fatalf("merge changed %v turns %q", changed, turns(x.Req))
	}
	x = &Exchange{SystemMode: v1.SystemMessages_SYSTEM_MESSAGES_KEEP, Req: &chat.Request{Messages: []chat.Message{turn("user", "hi"), turn("system", "a")}}}
	if changed, _ := (Fold{}).Intercept(x); changed || turns(x.Req) != "user=hi,system=a" {
		t.Fatalf("keep changed %v turns %q", changed, turns(x.Req))
	}
}
