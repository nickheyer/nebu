package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Fake sd-server with capabilities, media jobs, and native API paths.
type fakeSD struct {
	mu       sync.Mutex
	jobs     map[string]map[string]any
	requests map[string]map[string]any
	polls    map[string]int
	// Poll counts for queued and generating states before completion.
	slow      int
	cancelled []string
	modes     []string
}

func newFakeSD() *fakeSD {
	return &fakeSD{jobs: map[string]map[string]any{}, requests: map[string]map[string]any{}, polls: map[string]int{}, slow: 2, modes: []string{"img_gen", "vid_gen"}}
}

func (f *fakeSD) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sdcpp/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"supported_modes": f.modes, "samplers": []string{"euler", "euler_a"}, "schedulers": []string{"discrete"}, "limits": map[string]any{"max_width": 2048}})
	})
	submit := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil || body["prompt"] == "" {
				writeJSON(w, 400, map[string]any{"error": "invalid request"})
				return
			}
			if body["prompt"] == "refuse me" {
				writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "loaded model does not support this"}})
				return
			}
			f.mu.Lock()
			id := "job_" + kind + "_" + string(rune('a'+len(f.jobs)))
			f.jobs[id] = map[string]any{"id": id, "kind": kind, "status": "queued", "queue_position": 1}
			f.requests[id] = body
			f.mu.Unlock()
			writeJSON(w, 202, map[string]any{"id": id, "kind": kind, "status": "queued", "poll_url": "/sdcpp/v1/jobs/" + id})
		}
	}
	mux.HandleFunc("/sdcpp/v1/img_gen", submit("img_gen"))
	mux.HandleFunc("/sdcpp/v1/vid_gen", submit("vid_gen"))
	mux.HandleFunc("/sdcpp/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/sdcpp/v1/jobs/")
		id, sub, _ := strings.Cut(rest, "/")
		f.mu.Lock()
		defer f.mu.Unlock()
		job, ok := f.jobs[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if sub == "cancel" {
			f.cancelled = append(f.cancelled, id)
			job["status"] = "cancelled"
			job["error"] = map[string]any{"code": "cancelled", "message": "job cancelled by client"}
			writeJSON(w, 200, job)
			return
		}
		f.polls[id]++
		req := f.requests[id]
		switch {
		case req["prompt"] == "fail me":
			job["status"] = "failed"
			job["error"] = map[string]any{"code": "generation_failed", "message": "generate_image returned empty results"}
		case f.polls[id] == 1:
			job["status"] = "queued"
		case f.polls[id] <= f.slow:
			job["status"] = "generating"
			job["queue_position"] = 0
		default:
			if job["status"] != "cancelled" {
				job["status"] = "completed"
				if job["kind"] == "img_gen" {
					n, _ := req["batch_count"].(float64)
					var images []map[string]any
					for i := 0; i < max(int(n), 1); i++ {
						images = append(images, map[string]any{"index": i, "b64_json": base64.StdEncoding.EncodeToString([]byte("png" + string(rune('0'+i))))})
					}
					job["result"] = map[string]any{"output_format": "png", "images": images}
				} else {
					job["result"] = map[string]any{"output_format": "webm", "mime_type": "video/webm", "fps": 16, "frame_count": 33, "b64_json": base64.StdEncoding.EncodeToString([]byte("webm-bytes"))}
				}
			}
		}
		writeJSON(w, 200, job)
	})
	return mux
}

func mediaGateway(t *testing.T) (*Gateway, *fakeSD, *httptest.Server) {
	t.Helper()
	sd := newFakeSD()
	upstream := httptest.NewServer(sd.handler())
	t.Cleanup(upstream.Close)
	table := tableOf(t, map[string]string{"llm": upstream.URL})
	table.SetModes("inst-sdxl", []string{"img_gen"})
	table.SetModes("inst-wan", []string{"img_gen", "vid_gen"})
	table.Set("sdxl", "inst-sdxl", "", upstream.URL, "repo:sdxl", "", v1.ApiFlavor_API_FLAVOR_SDCPP, nil, nil)
	table.Set("wan", "inst-wan", "", upstream.URL, "repo:wan", "", v1.ApiFlavor_API_FLAVOR_SDCPP, nil, nil)
	g := New(table, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	return g, sd, srv
}

func postJSON(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

// OpenAI image requests return images from completed native jobs.
func TestImagesThroughJobs(t *testing.T) {
	g, sd, srv := mediaGateway(t)
	resp, raw := postJSON(t, srv.URL+imagesPath, `{"model":"sdxl","prompt":"a cat","n":2,"size":"1024x768","steps":8,"cfg_scale":4.5,"seed":7,"sampler":"euler","negative_prompt":"blurry","vae_tiling":true,"sd_cpp":{"cache_mode":"easycache"}}`)
	if resp.StatusCode != 200 {
		t.Fatalf("images %d %s", resp.StatusCode, raw)
	}
	var answer struct {
		Created      int64  `json:"created"`
		Model        string `json:"model"`
		OutputFormat string `json:"output_format"`
		Size         string `json:"size"`
		Seed         int64  `json:"seed"`
		Data         []struct {
			Index   int    `json:"index"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || len(answer.Data) != 2 || answer.Model != "sdxl" || answer.OutputFormat != "png" || answer.Size != "1024x768" || answer.Seed != 7 || answer.Data[1].Index != 1 {
		t.Fatalf("answer %s %v", raw, err)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(answer.Data[0].B64JSON); string(decoded) != "png0" {
		t.Fatalf("image bytes %q", decoded)
	}
	sd.mu.Lock()
	sent := sd.requests["job_img_gen_a"]
	sd.mu.Unlock()
	sample := sent["sample_params"].(map[string]any)
	guidance := sample["guidance"].(map[string]any)
	if sent["width"] != 1024.0 || sent["height"] != 768.0 || sent["batch_count"] != 2.0 || sent["seed"] != 7.0 || sent["negative_prompt"] != "blurry" || sample["sample_steps"] != 8.0 || sample["sample_method"] != "euler" || guidance["txt_cfg"] != 4.5 || sent["cache_mode"] != "easycache" {
		t.Fatalf("native job %v", sent)
	}
	if tiling := sent["vae_tiling_params"].(map[string]any); tiling["enabled"] != true {
		t.Fatalf("tiling %v", tiling)
	}
	// Traces contain request kind, job input, and a summary of the output.
	traces := g.Traces().List("sdxl", 0)
	if len(traces) != 1 || traces[0].GetKind() != v1.TraceKind_TRACE_KIND_IMAGE || traces[0].GetStop() != "stop" || traces[0].GetFirstTokenAt() == nil || traces[0].GetFinishedAt() == nil {
		t.Fatalf("trace %v", traces)
	}
	full, _ := g.Traces().Get(traces[0].GetId())
	if !strings.Contains(full.GetResponse(), `"images":2`) || strings.Contains(full.GetResponse(), "b64") || !strings.Contains(full.GetUpstreamRequest(), `"batch_count":2`) {
		t.Fatalf("trace bodies %q %q", full.GetResponse(), full.GetUpstreamRequest())
	}
	// Runtime, job, and validation failures return error messages.
	resp, raw = postJSON(t, srv.URL+imagesPath, `{"model":"sdxl","prompt":"refuse me"}`)
	if resp.StatusCode != 400 || !strings.Contains(string(raw), "loaded model does not support") {
		t.Fatalf("refusal %d %s", resp.StatusCode, raw)
	}
	resp, raw = postJSON(t, srv.URL+imagesPath, `{"model":"sdxl","prompt":"fail me"}`)
	if resp.StatusCode != 502 || !strings.Contains(string(raw), "empty results") {
		t.Fatalf("failed job %d %s", resp.StatusCode, raw)
	}
	resp, raw = postJSON(t, srv.URL+imagesPath, `{"model":"sdxl","prompt":"x","size":"1000x1000"}`)
	if resp.StatusCode != 400 || !strings.Contains(string(raw), "multiples of 16") {
		t.Fatalf("bad size %d %s", resp.StatusCode, raw)
	}
	resp, raw = postJSON(t, srv.URL+imagesPath, `{"model":"sdxl","prompt":"x","response_format":"url"}`)
	if resp.StatusCode != 400 || !strings.Contains(string(raw), "b64_json") {
		t.Fatalf("url format %d %s", resp.StatusCode, raw)
	}
	// Reject requests unsupported by the route's model kind.
	resp, raw = postJSON(t, srv.URL+imagesPath, `{"model":"llm","prompt":"x"}`)
	if resp.StatusCode != 400 || !strings.Contains(string(raw), "language model") {
		t.Fatalf("language route %d %s", resp.StatusCode, raw)
	}
	resp, raw = postJSON(t, srv.URL+"/v1/chat/completions", `{"model":"sdxl","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 400 || !strings.Contains(string(raw), videosPath) {
		t.Fatalf("chat on a diffusion route %d %s", resp.StatusCode, raw)
	}
	// Video requests require video capability.
	resp, raw = postJSON(t, srv.URL+videosPath, `{"model":"sdxl","prompt":"x"}`)
	if resp.StatusCode != 400 || !strings.Contains(string(raw), "not video") {
		t.Fatalf("no vid_gen %d %s", resp.StatusCode, raw)
	}
}

// Multipart image edits map files to init and reference images.
func TestImageEditsFromForm(t *testing.T) {
	_, sd, srv := mediaGateway(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "sdxl")
	mw.WriteField("prompt", "make it night")
	mw.WriteField("n", "1")
	mw.WriteField("strength", "0.6")
	for _, name := range []string{"first", "second"} {
		fw, _ := mw.CreateFormFile("image[]", name+".png")
		fw.Write([]byte(name))
	}
	fw, _ := mw.CreateFormFile("mask", "mask.png")
	fw.Write([]byte("mask"))
	mw.Close()
	resp, err := http.Post(srv.URL+editsPath, mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "b64_json") {
		t.Fatalf("edit %d %s", resp.StatusCode, raw)
	}
	sd.mu.Lock()
	sent := sd.requests["job_img_gen_a"]
	sd.mu.Unlock()
	if sent["init_image"] != base64.StdEncoding.EncodeToString([]byte("first")) || sent["mask_image"] != base64.StdEncoding.EncodeToString([]byte("mask")) || sent["strength"] != 0.6 || sent["prompt"] != "make it night" {
		t.Fatalf("edit job %v", sent)
	}
	if refs, _ := sent["ref_images"].([]any); len(refs) != 1 || refs[0] != base64.StdEncoding.EncodeToString([]byte("second")) {
		t.Fatalf("ref images %v", sent["ref_images"])
	}
	// JSON edits accept an image list.
	resp2, raw2 := postJSON(t, srv.URL+editsPath, `{"model":"sdxl","prompt":"json edit","image":["aW1n","cmVm"],"mask":"bWFzaw=="}`)
	if resp2.StatusCode != 200 {
		t.Fatalf("json edit %d %s", resp2.StatusCode, raw2)
	}
	sd.mu.Lock()
	sent = sd.requests["job_img_gen_b"]
	sd.mu.Unlock()
	if sent["init_image"] != "aW1n" || sent["mask_image"] != "bWFzaw==" {
		t.Fatalf("json edit job %v", sent)
	}
}

// Video requests return a job ID for polling and downloading.
func TestVideosAreJobs(t *testing.T) {
	g, sd, srv := mediaGateway(t)
	resp, raw := postJSON(t, srv.URL+videosPath, `{"model":"wan","prompt":"a cat walking","size":"832x480","seconds":2,"fps":16,"steps":10,"high_noise":{"steps":8,"cfg_scale":3.5},"init_image":"data:image/png;base64,aW1n"}`)
	if resp.StatusCode != 202 {
		t.Fatalf("video %d %s", resp.StatusCode, raw)
	}
	var vid struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
		Frames int    `json:"frames"`
		FPS    int    `json:"fps"`
		Size   string `json:"size"`
	}
	if err := json.Unmarshal(raw, &vid); err != nil || vid.Object != "video" || vid.Status != "queued" || vid.Frames != 33 || vid.FPS != 16 || vid.Size != "832x480" || !strings.HasPrefix(vid.ID, "video_") {
		t.Fatalf("video object %s %v", raw, err)
	}
	sd.mu.Lock()
	sent := sd.requests["job_vid_gen_a"]
	sd.mu.Unlock()
	high := sent["high_noise_sample_params"].(map[string]any)
	if sent["video_frames"] != 33.0 || sent["fps"] != 16.0 || sent["init_image"] != "data:image/png;base64,aW1n" || high["sample_steps"] != 8.0 || high["guidance"].(map[string]any)["txt_cfg"] != 3.5 {
		t.Fatalf("video job %v", sent)
	}
	// Files become available after job completion.
	var content *http.Response
	deadline := time.Now().Add(10 * time.Second)
	for {
		content, _ = http.Get(srv.URL + videosPath + "/" + vid.ID + "/content")
		if content.StatusCode == 200 || time.Now().After(deadline) {
			break
		}
		if content.StatusCode != 202 {
			t.Fatalf("content while running %d", content.StatusCode)
		}
		content.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := io.ReadAll(content.Body)
	content.Body.Close()
	if content.StatusCode != 200 || string(data) != "webm-bytes" || content.Header.Get("Content-Type") != "video/webm" || !strings.Contains(content.Header.Get("Content-Disposition"), ".webm") {
		t.Fatalf("content %d %q %v", content.StatusCode, data, content.Header)
	}
	status, _ := http.Get(srv.URL + videosPath + "/" + vid.ID)
	raw, _ = io.ReadAll(status.Body)
	if !strings.Contains(string(raw), `"status":"completed"`) || !strings.Contains(string(raw), `"frames":33`) || !strings.Contains(string(raw), `"seconds":2.06`) {
		t.Fatalf("status %s", raw)
	}
	list, _ := http.Get(srv.URL + videosPath)
	raw, _ = io.ReadAll(list.Body)
	if !strings.Contains(string(raw), vid.ID) || !strings.Contains(string(raw), `"object":"list"`) {
		t.Fatalf("list %s", raw)
	}
	// Trace duration matches job duration.
	deadline = time.Now().Add(5 * time.Second)
	for {
		traces := g.Traces().List("wan", 0)
		if len(traces) == 1 && traces[0].GetFinishedAt() != nil {
			full, _ := g.Traces().Get(traces[0].GetId())
			if traces[0].GetKind() != v1.TraceKind_TRACE_KIND_VIDEO || traces[0].GetStop() != "stop" || traces[0].GetStatus() != 200 || !strings.Contains(full.GetResponse(), vid.ID) {
				t.Fatalf("trace %v %q", traces[0], full.GetResponse())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trace never finished: %v", traces)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Deleting a running video cancels its job upstream
	sd.mu.Lock()
	sd.slow = 1000
	sd.mu.Unlock()
	resp, raw = postJSON(t, srv.URL+videosPath, `{"model":"wan","prompt":"long one","frames":80}`)
	if !strings.Contains(string(raw), `"frames":81`) {
		t.Fatalf("frames rise to the grid: %s", raw)
	}
	if resp.StatusCode != 202 {
		t.Fatalf("second video %d %s", resp.StatusCode, raw)
	}
	json.Unmarshal(raw, &vid)
	del, _ := http.NewRequest(http.MethodDelete, srv.URL+videosPath+"/"+vid.ID, nil)
	resp, err := http.DefaultClient.Do(del)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("delete %v %v", resp, err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		sd.mu.Lock()
		n := len(sd.cancelled)
		sd.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job was never cancelled upstream")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if resp, _ := http.Get(srv.URL + videosPath + "/" + vid.ID); resp.StatusCode != 404 {
		t.Fatalf("a deleted video is gone: %d", resp.StatusCode)
	}
	if resp, _ := http.Get(srv.URL + videosPath + "/video_nope"); resp.StatusCode != 404 {
		t.Fatalf("unknown video %d", resp.StatusCode)
	}
}

// Native media requests pass through. Model listings include route capabilities.
func TestNativePassthroughAndCapabilities(t *testing.T) {
	_, _, srv := mediaGateway(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+sdcppCapabilities, nil)
	req.Header.Set(modelHeader, "wan")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "vid_gen") {
		t.Fatalf("passthrough %d %s", resp.StatusCode, raw)
	}
	modes, err := Capabilities(t.Context(), http.DefaultClient, srv.URL+"/nope")
	if err == nil {
		t.Fatalf("capabilities off a bad endpoint should fail: %v", modes)
	}
	list, _ := http.Get(srv.URL + "/v1/models")
	raw, _ = io.ReadAll(list.Body)
	var models struct {
		Data []struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	json.Unmarshal(raw, &models)
	caps := map[string][]string{}
	for _, m := range models.Data {
		caps[m.ID] = m.Capabilities
	}
	if strings.Join(caps["wan"], ",") != "images,videos" || strings.Join(caps["sdxl"], ",") != "images" || strings.Join(caps["llm"], ",") != "completion,tools" {
		t.Fatalf("capabilities %v", caps)
	}
}

// Capabilities are shared across an instance's routes and cleared on removal.
func TestTableModes(t *testing.T) {
	table := tableOf(t, nil)
	table.SetModes("inst-1", []string{"img_gen"})
	r := table.Set("a", "inst-1", "", "http://x", "m", "", v1.ApiFlavor_API_FLAVOR_SDCPP, nil, nil)
	if strings.Join(r.GetModes(), ",") != "img_gen" {
		t.Fatalf("modes on set %v", r.GetModes())
	}
	table.Set("b", "inst-1", "slot-1", "http://x", "m", "", v1.ApiFlavor_API_FLAVOR_SDCPP, nil, nil)
	table.SetModes("inst-1", []string{"img_gen", "vid_gen"})
	for _, name := range []string{"a", "b"} {
		r, _ := table.Lookup(name)
		if strings.Join(r.GetModes(), ",") != "img_gen,vid_gen" {
			t.Fatalf("%s modes %v", name, r.GetModes())
		}
	}
	table.RemoveInstance("inst-1")
	if r, ok := table.Lookup("b"); !ok || len(r.GetModes()) != 0 || r.GetState() != v1.RouteState_ROUTE_STATE_PENDING {
		t.Fatalf("slot route keeps its name without modes: %v %v", r, ok)
	}
	if kindOf(v1.ApiFlavor_API_FLAVOR_OPENAI, imagesPath) != v1.TraceKind_TRACE_KIND_IMAGE || kindOf(v1.ApiFlavor_API_FLAVOR_OPENAI, videosPath) != v1.TraceKind_TRACE_KIND_VIDEO || kindOf(v1.ApiFlavor_API_FLAVOR_OPENAI, editsPath) != v1.TraceKind_TRACE_KIND_IMAGE {
		t.Fatal("trace kinds")
	}
}
