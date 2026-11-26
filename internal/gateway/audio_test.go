package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
)

type audioUpstream struct {
	mu          sync.Mutex
	path        string
	contentType string
	body        []byte
}

func (u *audioUpstream) seen() (string, string, []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.path, u.contentType, u.body
}

func (u *audioUpstream) server(t *testing.T, contentType string, answer []byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.path, u.contentType, u.body = r.URL.Path, r.Header.Get("Content-Type"), body
		u.mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		w.Write(answer)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func finishedTrace(t *testing.T, h *harness, id string) *v1.Trace {
	t.Helper()
	var tr *v1.Trace
	waitFor(t, "trace finished", func() bool {
		got, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
		tr = got
		return ok && got.GetFinishedAt() != nil
	})
	if _, err := proto.Marshal(tr); err != nil {
		t.Fatalf("trace does not encode: %v", err)
	}
	return tr
}

func audioForm(t *testing.T, fields [][2]string, file []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		if f[0] == "file" {
			fw, err := mw.CreateFormFile("file", "clip.wav")
			if err != nil {
				t.Fatal(err)
			}
			fw.Write(file)
			continue
		}
		mw.WriteField(f[0], f[1])
	}
	mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func readForm(t *testing.T, contentType string, body []byte) (map[string]string, []byte) {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("content type %q: %v", contentType, err)
	}
	mf, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("upstream form: %v", err)
	}
	fields := map[string]string{}
	for k, v := range mf.Value {
		fields[k] = strings.Join(v, ",")
	}
	if len(mf.File["file"]) != 1 {
		t.Fatalf("upstream form files %v", mf.File)
	}
	f, _ := mf.File["file"][0].Open()
	data, _ := io.ReadAll(f)
	return fields, data
}

func TestSpeechRoutesByModelAndPassesAudio(t *testing.T) {
	h := newHarness(t)
	audio := []byte{0xff, 0xfb, 0x90, 0x44, 0x00, 0xc3, 0x28, 'I', 'D', '3', 0x80, 0x00, 0xfe}
	var up audioUpstream
	h.publish("voice", "tts", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "tts-served"}, up.server(t, "audio/mpeg", audio).URL)
	sent := `{"model":"voice","input":"hello there","voice":"alloy","response_format":"mp3","speed":1.25,"instructions":"calm"}`
	r := h.do(http.MethodPost, chat.OpenAISpeech, sent)
	if r.status != http.StatusOK || !bytes.Equal(r.body, audio) || r.header.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("speech %d %q %q", r.status, r.header.Get("Content-Type"), r.body)
	}
	path, contentType, body := up.seen()
	var got, want map[string]any
	json.Unmarshal(body, &got)
	json.Unmarshal([]byte(sent), &want)
	want["model"] = "tts-served"
	if path != chat.OpenAISpeech || contentType != "application/json" || !jsonEqual(got, want) {
		t.Fatalf("upstream got %s %s %s", path, contentType, body)
	}
	tr := finishedTrace(t, h, r.header.Get(TraceHeader))
	if tr.GetKind() != v1.TraceKind_TRACE_KIND_OTHER || tr.GetPath() != chat.OpenAISpeech || tr.GetRoute() != "voice" || tr.GetStatus() != http.StatusOK || tr.GetResponseBytes() != uint64(len(audio)) || tr.GetResponse() != "(13 bytes, not text)" {
		t.Fatalf("speech trace %v", tr)
	}
}

func jsonEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestTranscriptionRoutesByFormModel(t *testing.T) {
	h := newHarness(t)
	clip := append([]byte("RIFF\x00\x00WAVEfmt "), 0xff, 0x00, 0xc3, 0x28, '\r', '\n', '-', '-', 0x80)
	answer := []byte(`{"text":"hello there"}`)
	var renamed, same audioUpstream
	h.publish("stt", "whisper", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "whisper-served"}, renamed.server(t, "application/json", answer).URL)
	h.publish("same", "same", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "same"}, same.server(t, "application/json", answer).URL)

	body, contentType := audioForm(t, [][2]string{{"file", ""}, {"model", "stt"}, {"language", "en"}, {"prompt", "names: Nebu"}, {"response_format", "json"}, {"temperature", "0.2"}}, clip)
	r := h.do(http.MethodPost, chat.OpenAITranscriptions, string(body), "Content-Type", contentType)
	if r.status != http.StatusOK || !bytes.Equal(r.body, answer) || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("transcription %d %s", r.status, r.body)
	}
	path, upType, upBody := renamed.seen()
	fields, file := readForm(t, upType, upBody)
	want := map[string]string{"model": "whisper-served", "language": "en", "prompt": "names: Nebu", "response_format": "json", "temperature": "0.2"}
	if path != chat.OpenAITranscriptions || !bytes.Equal(file, clip) || len(fields) != len(want) {
		t.Fatalf("upstream got %s fields %v file %q", path, fields, file)
	}
	for k, v := range want {
		if fields[k] != v {
			t.Fatalf("upstream field %s %q, want %q", k, fields[k], v)
		}
	}
	tr := finishedTrace(t, h, r.header.Get(TraceHeader))
	if tr.GetKind() != v1.TraceKind_TRACE_KIND_OTHER || tr.GetRoute() != "stt" || tr.GetRequestBytes() != uint64(len(body)) || tr.GetResponse() != string(answer) || !strings.HasSuffix(tr.GetRequest(), "bytes, not text)") {
		t.Fatalf("transcription trace %v", tr)
	}

	body, contentType = audioForm(t, [][2]string{{"model", "same"}, {"file", ""}}, clip)
	r = h.do(http.MethodPost, chat.OpenAITranslations, string(body), "Content-Type", contentType)
	path, upType, upBody = same.seen()
	if r.status != http.StatusOK || path != chat.OpenAITranslations || upType != contentType || !bytes.Equal(upBody, body) {
		t.Fatalf("translation %d %s, upstream got %s %s", r.status, r.body, path, upType)
	}
}

func TestAudioRefusesLikeChat(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.keys = []string{"k"} })
	h.publish("starting", "starting", "vllm", datalayer.AppProtocolHTTP, false, pools.RouteSpec{}, "127.0.0.1:1")
	auth := []string{"Authorization", "Bearer k"}
	form := func(model string) (string, []string) {
		body, ct := audioForm(t, [][2]string{{"model", model}, {"file", ""}}, []byte("RIFF"))
		return string(body), append([]string{"Content-Type", ct}, auth...)
	}
	for _, model := range []string{"nope", "starting"} {
		want := h.do(http.MethodPost, chat.OpenAIChat, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`, auth...)
		speech := h.do(http.MethodPost, chat.OpenAISpeech, `{"model":"`+model+`","input":"hi"}`, auth...)
		body, header := form(model)
		transcription := h.do(http.MethodPost, chat.OpenAITranscriptions, body, header...)
		for _, r := range []reply{speech, transcription} {
			got, exp := r.json(t)["error"].(map[string]any), want.json(t)["error"].(map[string]any)
			if r.status != want.status || got["code"] != exp["code"] || got["type"] != exp["type"] || (r.header.Get("Retry-After") == "") != (want.header.Get("Retry-After") == "") {
				t.Fatalf("%s refused %d %s, chat %d %s", model, r.status, r.body, want.status, want.body)
			}
		}
	}
	if r := h.do(http.MethodPost, chat.OpenAISpeech, `{"model":"nope","input":"hi"}`); r.status != http.StatusUnauthorized {
		t.Fatalf("speech without a key %d %s", r.status, r.body)
	}
	body, header := form("nope")
	if r := h.do(http.MethodPost, chat.OpenAITranscriptions, body, header[:2]...); r.status != http.StatusUnauthorized {
		t.Fatalf("transcription without a key %d %s", r.status, r.body)
	}
	r := h.do(http.MethodOptions, chat.OpenAITranscriptions, "", "Origin", "http://ui.example", "Access-Control-Request-Method", "POST")
	if r.status != http.StatusNoContent || r.header.Get("Access-Control-Allow-Origin") != "http://ui.example" {
		t.Fatalf("preflight %d %v", r.status, r.header)
	}
}

func TestModelsListAudioCapabilities(t *testing.T) {
	h := newHarness(t)
	launches := map[string]map[string]string{
		"chat":    {"n_ctx": "8192", runtimes.VLLMSupportedTasks: "generate"},
		"whisper": {"n_ctx": "448", runtimes.VLLMSupportedTasks: "transcription"},
		"voxtral": {"n_ctx": "8192", runtimes.VLLMSupportedTasks: "generate,transcription"},
	}
	h.inst.mu.Lock()
	h.inst.vllm = launches
	h.inst.mu.Unlock()
	port := 18021
	for _, name := range []string{"chat", "whisper", "voxtral"} {
		publishLaunch(t, h, name, launches[name], port)
		port++
	}
	waitFor(t, "every route listed", func() bool {
		for name := range launches {
			if _, ok := h.g.pools.Route(name); !ok {
				return false
			}
		}
		return true
	})
	var listed struct {
		Data []struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
			Inventory    struct {
				Modalities []string `json:"modalities"`
			} `json:"inventory"`
		} `json:"data"`
	}
	r := h.do(http.MethodGet, "/v1/models", "")
	if err := json.Unmarshal(r.body, &listed); r.status != http.StatusOK || err != nil || len(listed.Data) != len(launches) {
		t.Fatalf("models %d %s %v", r.status, r.body, err)
	}
	want := map[string][]string{
		"chat":    {"completion", "tools"},
		"whisper": {runtimes.CapabilityTranscription},
		"voxtral": {"completion", "tools", runtimes.CapabilityTranscription},
	}
	for _, m := range listed.Data {
		if !slices.Equal(m.Capabilities, want[m.ID]) || !slices.Equal(m.Inventory.Modalities, []string{"text"}) {
			t.Fatalf("model %s listed as %+v", m.ID, m)
		}
	}
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	r = h.do(http.MethodPost, "/api/show", `{"model":"whisper"}`)
	if err := json.Unmarshal(r.body, &show); err != nil || !slices.Equal(show.Capabilities, want["whisper"]) {
		t.Fatalf("ollama show %d %s", r.status, r.body)
	}
}

func TestCappedKeepsBinaryOutOfTraces(t *testing.T) {
	long := strings.Repeat("é", traceBodyCap)
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"text", []byte(`{"text":"héllo"}`), `{"text":"héllo"}`},
		{"binary", []byte{0xff, 0xfb, 'a', 0xc3}, "(4 bytes, not text)"},
		{"long text cut at a character", []byte(long), long[:traceBodyCap]},
		{"text cut before bytes past the cap", append([]byte(long[:traceBodyCap-1]), bytes.Repeat([]byte{0xff}, 64)...), long[:traceBodyCap-2]},
		{"binary past the cap", bytes.Repeat([]byte{0x80, 'a'}, traceBodyCap), "(2097152 bytes, not text)"},
	}
	for _, c := range cases {
		got := capped(c.body)
		if got != c.want || !utf8.ValidString(got) {
			t.Fatalf("%s: capped to %d bytes %.40q", c.name, len(got), got)
		}
	}
}
