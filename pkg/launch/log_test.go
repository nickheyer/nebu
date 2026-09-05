package launch

import (
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestClean(t *testing.T) {
	cases := map[string]string{
		"plain line": "plain line",
		"progress 10%\rprogress 50%\rprogress 100%":  "progress 100%",
		"\x1b[32mgreen\x1b[0m text":                  "green text",
		"\x1b]0;title\x07after title":                "after title",
		"bad \xff\xfe bytes":                         "bad � bytes",
		"tabs\tkept\x00nul dropped\x07bell":          "tabs\tkeptnul droppedbell",
		"llama_model_loader: loaded meta data ✓ 日本語": "llama_model_loader: loaded meta data ✓ 日本語",
		"": "",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
		if !utf8.ValidString(Clean(in)) {
			t.Errorf("Clean(%q) is not valid UTF-8", in)
		}
	}
	// The whole point: a batch of cleaned lines marshals as a proto message
	l := NewLog(4)
	l.Write("ok\xff")
	l.Write("\x1b[1mbold")
	if _, err := proto.Marshal(&v1.LogsResponse{Lines: l.Tail(0)}); err != nil {
		t.Fatalf("marshal cleaned lines: %v", err)
	}
	if _, err := proto.Marshal(&v1.LogsResponse{Lines: []string{"raw\xff"}}); err == nil {
		t.Fatal("raw invalid bytes should still fail, proving the test means something")
	}
}
