package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Images and video through stable-diffusion.cpp's job API, offered in the shapes of OpenAI's image
// and video endpoints with the sampler's own knobs as extra fields
//
// An image request waits for its job and answers with the images. A video request answers with a
// video object at once and the job runs on; the object is polled by id and its file fetched once
// done, the way OpenAI's video API works. Every image field takes raw base64 or a data URL.

const (
	imagesPath = "/v1/images/generations"
	editsPath  = "/v1/images/edits"
	videosPath = "/v1/videos"

	// How often a job is asked after, and how long a finished video is kept for its file to be fetched
	jobPoll   = 500 * time.Millisecond
	videoKeep = 24 * time.Hour
	// Finished videos kept in memory at once, the oldest dropped past it
	videoLimit = 32
)

// One request as the client wrote it, the OpenAI fields and the sampler's own
type mediaRequest struct {
	Model          string   `json:"model"`
	Prompt         string   `json:"prompt"`
	NegativePrompt string   `json:"negative_prompt"`
	N              int      `json:"n"`
	Size           string   `json:"size"`
	Width          int      `json:"width"`
	Height         int      `json:"height"`
	Steps          int      `json:"steps"`
	CfgScale       *float64 `json:"cfg_scale"`
	ImgCfgScale    *float64 `json:"img_cfg_scale"`
	Guidance       *float64 `json:"guidance"`
	FlowShift      *float64 `json:"flow_shift"`
	Eta            *float64 `json:"eta"`
	Seed           *int64   `json:"seed"`
	Sampler        string   `json:"sampler"`
	Scheduler      string   `json:"scheduler"`
	ClipSkip       *int     `json:"clip_skip"`
	Strength       *float64 `json:"strength"`
	// Images in, for edits and image to video
	InitImage       string          `json:"init_image"`
	Image           json.RawMessage `json:"image"`
	MaskImage       string          `json:"mask_image"`
	Mask            string          `json:"mask"`
	RefImages       []string        `json:"ref_images"`
	ControlImage    string          `json:"control_image"`
	ControlStrength *float64        `json:"control_strength"`
	EndImage        string          `json:"end_image"`
	ControlFrames   []string        `json:"control_frames"`
	// Video shape
	Frames       int           `json:"frames"`
	VideoFrames  int           `json:"video_frames"`
	FPS          int           `json:"fps"`
	Seconds      float64       `json:"seconds"`
	HighNoise    *sampleParams `json:"high_noise"`
	MoeBoundary  *float64      `json:"moe_boundary"`
	VaceStrength *float64      `json:"vace_strength"`
	// Adapters and memory
	Lora           json.RawMessage `json:"lora"`
	Hires          json.RawMessage `json:"hires"`
	Slg            json.RawMessage `json:"slg"`
	VaeTiling      *bool           `json:"vae_tiling"`
	TemporalTiling *bool           `json:"temporal_tiling"`
	CacheMode      string          `json:"cache_mode"`
	CacheOption    string          `json:"cache_option"`
	// Output
	OutputFormat      string `json:"output_format"`
	OutputCompression *int   `json:"output_compression"`
	ResponseFormat    string `json:"response_format"`
	// Anything else the native schema takes, laid over the fields above
	SDCpp map[string]json.RawMessage `json:"sd_cpp"`
}

// The sampler settings of one stage, as the client may name them for the high noise half
type sampleParams struct {
	Steps     int      `json:"steps"`
	CfgScale  *float64 `json:"cfg_scale"`
	Guidance  *float64 `json:"guidance"`
	FlowShift *float64 `json:"flow_shift"`
	Sampler   string   `json:"sampler"`
	Scheduler string   `json:"scheduler"`
}

// A multipart form, its fields and files by name
type form struct {
	fields map[string][]string
	files  map[string][][]byte
}

func (f *form) value(name string) string {
	if v := f.fields[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Reads a multipart body into fields and files, refusing anything else
func parseForm(r *http.Request, body []byte) (*form, error) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return nil, fmt.Errorf("not a multipart form")
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	out := &form{fields: map[string][]string{}, files: map[string][][]byte{}}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(part, maxBody))
		part.Close()
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(part.FormName(), "[]")
		if part.FileName() != "" {
			out.files[name] = append(out.files[name], data)
		} else {
			out.fields[name] = append(out.fields[name], string(data))
		}
	}
}

// Reads a request from JSON, or from the form an OpenAI image edit arrives as
func parseMedia(r *http.Request, body []byte) (*mediaRequest, error) {
	req := &mediaRequest{}
	if f, err := parseForm(r, body); err == nil {
		req.Model, req.Prompt, req.NegativePrompt, req.Size, req.OutputFormat, req.ResponseFormat = f.value("model"), f.value("prompt"), f.value("negative_prompt"), f.value("size"), f.value("output_format"), f.value("response_format")
		req.N, _ = strconv.Atoi(f.value("n"))
		req.Steps, _ = strconv.Atoi(f.value("steps"))
		req.Sampler, req.Scheduler = f.value("sampler"), f.value("scheduler")
		for _, name := range []string{"cfg_scale", "guidance", "strength"} {
			if v, err := strconv.ParseFloat(f.value(name), 64); err == nil {
				switch name {
				case "cfg_scale":
					req.CfgScale = &v
				case "guidance":
					req.Guidance = &v
				case "strength":
					req.Strength = &v
				}
			}
		}
		if v, err := strconv.ParseInt(f.value("seed"), 10, 64); err == nil {
			req.Seed = &v
		}
		if n, err := strconv.Atoi(f.value("output_compression")); err == nil {
			req.OutputCompression = &n
		}
		for i, img := range f.files["image"] {
			enc := base64.StdEncoding.EncodeToString(img)
			if i == 0 {
				req.InitImage = enc
			} else {
				req.RefImages = append(req.RefImages, enc)
			}
		}
		if masks := f.files["mask"]; len(masks) > 0 {
			req.MaskImage = base64.StdEncoding.EncodeToString(masks[0])
		}
		return req, nil
	}
	if err := json.Unmarshal(body, req); err != nil {
		return nil, bad("%v", err)
	}
	// OpenAI's edit names its input image, one or several, and its mask by other fields
	if len(req.Image) > 0 {
		var one string
		var many []string
		if json.Unmarshal(req.Image, &one) == nil {
			req.InitImage = firstOf(req.InitImage, one)
		} else if json.Unmarshal(req.Image, &many) == nil && len(many) > 0 {
			req.InitImage = firstOf(req.InitImage, many[0])
			req.RefImages = append(req.RefImages, many[1:]...)
		}
	}
	req.MaskImage = firstOf(req.MaskImage, req.Mask)
	return req, nil
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// The width and height a request names, by its own fields or an OpenAI size of WIDTHxHEIGHT
func (m *mediaRequest) shape() (int, int, error) {
	w, h := m.Width, m.Height
	if m.Size != "" && (w == 0 || h == 0) {
		a, b, ok := strings.Cut(strings.ToLower(m.Size), "x")
		pw, err1 := strconv.Atoi(a)
		ph, err2 := strconv.Atoi(b)
		if !ok || err1 != nil || err2 != nil {
			return 0, 0, bad("size must be WIDTHxHEIGHT, got %q", m.Size)
		}
		w, h = pw, ph
	}
	if w < 0 || h < 0 || w%16 != 0 || h%16 != 0 {
		return 0, 0, bad("width and height must be multiples of 16, got %dx%d", w, h)
	}
	return w, h, nil
}

// The native sampler settings a request's fields fill, only the ones given so the server's defaults keep the rest
func sampling(steps int, cfg, img, guidance, flow, eta *float64, sampler, scheduler string, slg json.RawMessage) map[string]any {
	out := map[string]any{}
	if steps > 0 {
		out["sample_steps"] = steps
	}
	if sampler != "" {
		out["sample_method"] = sampler
	}
	if scheduler != "" {
		out["scheduler"] = scheduler
	}
	if flow != nil {
		out["flow_shift"] = *flow
	}
	if eta != nil {
		out["eta"] = *eta
	}
	g := map[string]any{}
	if cfg != nil {
		g["txt_cfg"] = *cfg
	}
	if img != nil {
		g["img_cfg"] = *img
	}
	if guidance != nil {
		g["distilled_guidance"] = *guidance
	}
	if len(slg) > 0 {
		g["slg"] = slg
	}
	if len(g) > 0 {
		out["guidance"] = g
	}
	return out
}

// The native job body an image request becomes
func (m *mediaRequest) imageJob() (map[string]any, error) {
	if strings.TrimSpace(m.Prompt) == "" {
		return nil, bad("prompt is required")
	}
	if m.ResponseFormat != "" && m.ResponseFormat != "b64_json" {
		return nil, bad("response_format %q is not offered, images come back as b64_json", m.ResponseFormat)
	}
	w, h, err := m.shape()
	if err != nil {
		return nil, err
	}
	job := map[string]any{"prompt": m.Prompt, "negative_prompt": m.NegativePrompt}
	if w > 0 && h > 0 {
		job["width"], job["height"] = w, h
	}
	if m.N > 0 {
		job["batch_count"] = m.N
	}
	if m.Seed != nil {
		job["seed"] = *m.Seed
	}
	if m.ClipSkip != nil {
		job["clip_skip"] = *m.ClipSkip
	}
	if m.Strength != nil {
		job["strength"] = *m.Strength
	}
	if m.InitImage != "" {
		job["init_image"] = m.InitImage
	}
	if m.MaskImage != "" {
		job["mask_image"] = m.MaskImage
	}
	if len(m.RefImages) > 0 {
		job["ref_images"] = m.RefImages
	}
	if m.ControlImage != "" {
		job["control_image"] = m.ControlImage
	}
	if m.ControlStrength != nil {
		job["control_strength"] = *m.ControlStrength
	}
	if s := sampling(m.Steps, m.CfgScale, m.ImgCfgScale, m.Guidance, m.FlowShift, m.Eta, m.Sampler, m.Scheduler, m.Slg); len(s) > 0 {
		job["sample_params"] = s
	}
	if len(m.Lora) > 0 {
		job["lora"] = m.Lora
	}
	if len(m.Hires) > 0 {
		job["hires"] = m.Hires
	}
	if m.VaeTiling != nil {
		job["vae_tiling_params"] = map[string]any{"enabled": *m.VaeTiling}
	}
	if m.CacheMode != "" {
		job["cache_mode"] = m.CacheMode
		job["cache_option"] = m.CacheOption
	}
	if m.OutputFormat != "" {
		job["output_format"] = m.OutputFormat
	}
	if m.OutputCompression != nil {
		job["output_compression"] = *m.OutputCompression
	}
	for k, v := range m.SDCpp {
		job[k] = v
	}
	return job, nil
}

// The frames a video request asks for: its own count, or its seconds at its frame rate, on the 4n+1 grid the sampler keeps
func (m *mediaRequest) frames() (int, int) {
	fps := m.FPS
	frames := firstInt(m.Frames, m.VideoFrames)
	if frames == 0 && m.Seconds > 0 {
		rate := fps
		if rate == 0 {
			rate = 16
		}
		frames = int(math.Round(m.Seconds * float64(rate)))
	}
	// The sampler keeps 4n+1 frames, so a count is raised to the next such length and never cut short
	if frames > 1 {
		frames = ((frames-2)/4+1)*4 + 1
	}
	return frames, fps
}

func firstInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// The native job body a video request becomes
func (m *mediaRequest) videoJob() (map[string]any, error) {
	if strings.TrimSpace(m.Prompt) == "" {
		return nil, bad("prompt is required")
	}
	w, h, err := m.shape()
	if err != nil {
		return nil, err
	}
	job := map[string]any{"prompt": m.Prompt, "negative_prompt": m.NegativePrompt}
	if w > 0 && h > 0 {
		job["width"], job["height"] = w, h
	}
	frames, fps := m.frames()
	if frames > 0 {
		job["video_frames"] = frames
	}
	if fps > 0 {
		job["fps"] = fps
	}
	if m.Seed != nil {
		job["seed"] = *m.Seed
	}
	if m.ClipSkip != nil {
		job["clip_skip"] = *m.ClipSkip
	}
	if m.Strength != nil {
		job["strength"] = *m.Strength
	}
	if m.InitImage != "" {
		job["init_image"] = m.InitImage
	}
	if m.EndImage != "" {
		job["end_image"] = m.EndImage
	}
	if len(m.ControlFrames) > 0 {
		job["control_frames"] = m.ControlFrames
	}
	if m.MoeBoundary != nil {
		job["moe_boundary"] = *m.MoeBoundary
	}
	if m.VaceStrength != nil {
		job["vace_strength"] = *m.VaceStrength
	}
	if s := sampling(m.Steps, m.CfgScale, m.ImgCfgScale, m.Guidance, m.FlowShift, m.Eta, m.Sampler, m.Scheduler, m.Slg); len(s) > 0 {
		job["sample_params"] = s
	}
	if hn := m.HighNoise; hn != nil {
		if s := sampling(hn.Steps, hn.CfgScale, nil, hn.Guidance, hn.FlowShift, nil, hn.Sampler, hn.Scheduler, nil); len(s) > 0 {
			job["high_noise_sample_params"] = s
		}
	}
	if len(m.Lora) > 0 {
		job["lora"] = m.Lora
	}
	if m.VaeTiling != nil || m.TemporalTiling != nil {
		tiling := map[string]any{}
		if m.VaeTiling != nil {
			tiling["enabled"] = *m.VaeTiling
		}
		if m.TemporalTiling != nil {
			tiling["temporal_tiling"] = *m.TemporalTiling
			if m.VaeTiling == nil {
				tiling["enabled"] = *m.TemporalTiling
			}
		}
		job["vae_tiling_params"] = tiling
	}
	if m.CacheMode != "" {
		job["cache_mode"] = m.CacheMode
		job["cache_option"] = m.CacheOption
	}
	if m.OutputFormat != "" {
		job["output_format"] = m.OutputFormat
	}
	if m.OutputCompression != nil {
		job["output_compression"] = *m.OutputCompression
	}
	for k, v := range m.SDCpp {
		job[k] = v
	}
	return job, nil
}

// A job as the server reports it
type sdJob struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	QueuePosition int             `json:"queue_position"`
	PollURL       string          `json:"poll_url"`
	Result        json.RawMessage `json:"result"`
	Error         *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Whether a job has ended, one way or another
func (j *sdJob) done() bool {
	return j.Status == "completed" || j.Status == "failed" || j.Status == "cancelled"
}

// The message a failed or cancelled job carries
func (j *sdJob) failure() string {
	if j.Error != nil && j.Error.Message != "" {
		return j.Error.Message
	}
	return "job " + j.Status
}

// A request to the runtime's native API, under the policy's upstream timeout for the headers
func (g *Gateway) native(ctx context.Context, method string, target *url.URL, path string, body []byte, policy *v1.Policy) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.ResolveReference(&url.URL{Path: path}).String(), reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.transport(policy.GetUpstreamTimeoutMs()).RoundTrip(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, raw, err
}

// Submits a job and returns it as accepted
func (g *Gateway) submit(ctx context.Context, target *url.URL, path string, job map[string]any, policy *v1.Policy) (*sdJob, []byte, error) {
	body, err := json.Marshal(job)
	if err != nil {
		return nil, nil, err
	}
	status, raw, err := g.native(ctx, http.MethodPost, target, path, body, policy)
	if err != nil {
		return nil, body, err
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		message := sdcpp{}.ErrorMessage(raw)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return nil, body, &upstreamRefusal{status: status, message: message}
	}
	var accepted sdJob
	if err := json.Unmarshal(raw, &accepted); err != nil || accepted.ID == "" {
		return nil, body, fmt.Errorf("the runtime accepted the job without naming it: %s", strings.TrimSpace(string(raw)))
	}
	return &accepted, body, nil
}

// A refusal from the runtime, answered to the client with its status
type upstreamRefusal struct {
	status  int
	message string
}

func (e *upstreamRefusal) Error() string { return e.message }

// Asks after a job until it ends, cancelling it upstream when the caller's context ends first; started is called once it leaves the queue
func (g *Gateway) await(ctx context.Context, target *url.URL, id string, policy *v1.Policy, started func()) (*sdJob, error) {
	begun := false
	for {
		status, raw, err := g.native(ctx, http.MethodGet, target, sdcppJobs+id, nil, policy)
		if err != nil {
			if ctx.Err() != nil {
				g.cancelJob(target, id, policy)
				return nil, ctx.Err()
			}
			return nil, err
		}
		if status == http.StatusGone || status == http.StatusNotFound {
			return nil, fmt.Errorf("the runtime forgot job %s before it was read", id)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("job %s answered %d: %s", id, status, strings.TrimSpace(string(raw)))
		}
		var job sdJob
		if err := json.Unmarshal(raw, &job); err != nil {
			return nil, fmt.Errorf("job %s: %w", id, err)
		}
		if !begun && job.Status != "queued" {
			begun = true
			if started != nil {
				started()
			}
		}
		if job.done() {
			return &job, nil
		}
		select {
		case <-ctx.Done():
			g.cancelJob(target, id, policy)
			return nil, ctx.Err()
		case <-time.After(jobPoll):
		}
	}
}

// Cancels a job on its own short clock, since the caller's is already gone
func (g *Gateway) cancelJob(target *url.URL, id string, policy *v1.Policy) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := g.native(ctx, http.MethodPost, target, sdcppJobs+id+"/cancel", nil, policy); err != nil {
		g.log.Debug("gateway cancel job", "job", id, "err", err)
	}
}

// Makes an image or a video on a diffusion route, the route held until the job ends
func (g *Gateway) media(w *traceWriter, r *http.Request, body []byte, name string, route *v1.Route, policy *v1.Policy, release func()) {
	t := w.t
	client := flavorOf(clientFlavor(r))
	if route.GetApi() != v1.ApiFlavor_API_FLAVOR_SDCPP {
		release()
		g.refuse(w, client, http.StatusBadRequest, "model "+name+" is a language model and generates no images or video", "invalid_request_error")
		return
	}
	target, err := url.Parse(route.GetEndpoint())
	if err != nil {
		release()
		g.refuse(w, client, http.StatusBadGateway, err.Error(), "server_error")
		return
	}
	req, err := parseMedia(r, body)
	if err != nil {
		release()
		g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if t.GetKind() == v1.TraceKind_TRACE_KIND_VIDEO {
		g.startVideo(w, req, name, route, target, policy, release)
		return
	}
	defer release()
	g.image(w, r, req, name, route, target, policy)
}

// Makes images and answers with them, the client's wait bounded by the policy's request timeout
func (g *Gateway) image(w *traceWriter, r *http.Request, req *mediaRequest, name string, route *v1.Route, target *url.URL, policy *v1.Policy) {
	t := w.t
	client := flavorOf(clientFlavor(r))
	if !hasMode(route, "img_gen") {
		g.refuse(w, client, http.StatusBadRequest, "model "+name+" generates video, not images: send it to "+videosPath, "invalid_request_error")
		return
	}
	job, err := req.imageJob()
	if err != nil {
		g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	ctx := r.Context()
	if d := policy.GetRequestTimeoutMs(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
		defer cancel()
	}
	accepted, sent, err := g.submit(ctx, target, sdcppImageJob, job, policy)
	t.UpstreamRequest = capped(sent)
	if err != nil {
		g.mediaError(w, client, t, name, err)
		return
	}
	t.FirstByteAt = timestamppb.Now()
	done, err := g.await(ctx, target, accepted.ID, policy, func() { t.FirstTokenAt = timestamppb.Now() })
	if err != nil {
		if errors.Is(err, context.Canceled) {
			t.Stop, t.Error = "cancelled", "client went away"
			return
		}
		g.mediaError(w, client, t, name, err)
		return
	}
	if done.Status != "completed" {
		t.Stop = done.Status
		g.refuse(w, client, http.StatusBadGateway, "upstream: "+done.failure(), "upstream_error")
		return
	}
	var result struct {
		OutputFormat string `json:"output_format"`
		Images       []struct {
			Index   int    `json:"index"`
			B64JSON string `json:"b64_json"`
		} `json:"images"`
	}
	if err := json.Unmarshal(done.Result, &result); err != nil || len(result.Images) == 0 {
		g.refuse(w, client, http.StatusBadGateway, "upstream answered with no images", "upstream_error")
		return
	}
	data := make([]map[string]any, 0, len(result.Images))
	for _, img := range result.Images {
		data = append(data, map[string]any{"index": img.Index, "b64_json": img.B64JSON})
	}
	width, height, _ := req.shape()
	answer := map[string]any{"created": time.Now().Unix(), "model": name, "output_format": result.OutputFormat, "data": data}
	if width > 0 && height > 0 {
		answer["size"] = fmt.Sprintf("%dx%d", width, height)
	}
	if req.Seed != nil {
		answer["seed"] = *req.Seed
	}
	t.Stop = "stop"
	summary, _ := json.Marshal(map[string]any{"images": len(data), "output_format": result.OutputFormat, "size": answer["size"]})
	t.Response = string(summary)
	writeJSON(w, http.StatusOK, answer)
}

// Answers a failed exchange with the job API: the runtime's own refusal with its status, a timeout as 504, and anything else as 502
func (g *Gateway) mediaError(w http.ResponseWriter, client Flavor, t *v1.Trace, name string, err error) {
	var refusal *upstreamRefusal
	if errors.As(err, &refusal) {
		t.Error = refusal.message
		client.Error(w, refusal.status, "upstream: "+refusal.message, "upstream_error")
		return
	}
	g.upstreamError(w, t, client, name, err)
}

func hasMode(route *v1.Route, mode string) bool {
	for _, m := range route.GetModes() {
		if m == mode {
			return true
		}
	}
	return false
}

// One video generation, from the request that asked for it to the file it made
type video struct {
	mu         sync.Mutex
	id         string
	model      string
	route      string
	trace      string
	prompt     string
	status     string
	progress   *int
	queue      int
	created    time.Time
	completed  time.Time
	width      int
	height     int
	frames     int
	fps        int
	format     string
	mime       string
	frameCount int
	fpsOut     int
	failure    *struct{ code, message string }
	data       []byte
	cancel     context.CancelFunc
}

// The video as the API shows it
func (v *video) object() map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]any{
		"id":             v.id,
		"object":         "video",
		"model":          v.model,
		"status":         v.status,
		"progress":       v.progress,
		"queue_position": v.queue,
		"created_at":     v.created.Unix(),
		"expires_at":     v.created.Add(videoKeep).Unix(),
		"prompt":         v.prompt,
		"output_format":  v.format,
		"mime_type":      v.mime,
		"trace":          v.trace,
		"error":          nil,
		"completed_at":   nil,
	}
	if v.width > 0 && v.height > 0 {
		out["size"] = fmt.Sprintf("%dx%d", v.width, v.height)
	}
	if v.frames > 0 {
		out["frames"] = v.frames
	}
	if v.fps > 0 {
		out["fps"] = v.fps
	}
	if v.frameCount > 0 {
		out["frames"] = v.frameCount
	}
	if v.fpsOut > 0 {
		out["fps"] = v.fpsOut
	}
	if frames, _ := out["frames"].(int); frames > 0 {
		if fps, _ := out["fps"].(int); fps > 0 {
			out["seconds"] = math.Round(float64(frames)/float64(fps)*100) / 100
		}
	}
	if !v.completed.IsZero() {
		out["completed_at"] = v.completed.Unix()
	}
	if v.failure != nil {
		out["error"] = map[string]any{"code": v.failure.code, "message": v.failure.message}
	}
	if len(v.data) > 0 {
		out["bytes"] = len(v.data)
	}
	return out
}

// Videos by id, the newest kept and the oldest finished ones dropped past the limit
type videoStore struct {
	mu   sync.Mutex
	byID map[string]*video
}

func newVideoStore() *videoStore { return &videoStore{byID: map[string]*video{}} }

func (s *videoStore) add(v *video) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[v.id] = v
	s.pruneLocked()
}

func (s *videoStore) get(id string) (*video, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.byID[id]
	return v, ok
}

func (s *videoStore) remove(id string) (*video, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.byID[id]
	delete(s.byID, id)
	return v, ok
}

// Every video, newest first
func (s *videoStore) list() []*video {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	out := make([]*video, 0, len(s.byID))
	for _, v := range s.byID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].created.After(out[j].created) })
	return out
}

// Drops finished videos past their keep time, then the oldest finished past the limit
func (s *videoStore) pruneLocked() {
	var finished []*video
	for id, v := range s.byID {
		v.mu.Lock()
		done := v.status == "completed" || v.status == "failed"
		old := done && time.Since(v.created) > videoKeep
		v.mu.Unlock()
		if old {
			delete(s.byID, id)
			continue
		}
		if done {
			finished = append(finished, v)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].created.Before(finished[j].created) })
	for len(s.byID) > videoLimit && len(finished) > 0 {
		delete(s.byID, finished[0].id)
		finished = finished[1:]
	}
}

// Starts a video job, answers with the video object at once, and follows the job to its end
func (g *Gateway) startVideo(w *traceWriter, req *mediaRequest, name string, route *v1.Route, target *url.URL, policy *v1.Policy, release func()) {
	t := w.t
	client := flavorOf(v1.ApiFlavor_API_FLAVOR_OPENAI)
	if !hasMode(route, "vid_gen") {
		release()
		g.refuse(w, client, http.StatusBadRequest, "model "+name+" generates images, not video: send it to "+imagesPath, "invalid_request_error")
		return
	}
	job, err := req.videoJob()
	if err != nil {
		release()
		g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	// The job runs on its own clock, the policy's request timeout when there is one, and never the client's
	ctx, cancel := context.WithCancel(context.Background())
	if d := policy.GetRequestTimeoutMs(); d > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(d)*time.Millisecond)
	}
	accepted, sent, err := g.submit(ctx, target, sdcppVideoJob, job, policy)
	t.UpstreamRequest = capped(sent)
	if err != nil {
		cancel()
		release()
		g.mediaError(w, client, t, name, err)
		return
	}
	width, height, _ := req.shape()
	frames, fps := req.frames()
	format := firstOf(req.OutputFormat, "webm")
	v := &video{id: "video_" + db.NewID(), model: name, route: name, trace: t.GetId(), prompt: req.Prompt, status: "queued", created: time.Now(), width: width, height: height, frames: frames, fps: fps, format: format, mime: videoMime(format), cancel: cancel}
	g.videos.add(v)
	t.FirstByteAt = timestamppb.Now()
	// The trace stays open until the job ends, so the request's length is the video's
	w.detached = true
	writeJSON(w, http.StatusAccepted, v.object())
	go g.followVideo(ctx, cancel, release, v, t, target, accepted.ID, policy)
}

// Follows a video job to its end, keeping the file it made
func (g *Gateway) followVideo(ctx context.Context, cancel context.CancelFunc, release func(), v *video, t *v1.Trace, target *url.URL, jobID string, policy *v1.Policy) {
	defer cancel()
	defer release()
	defer g.traces.Finish(t)
	done, err := g.await(ctx, target, jobID, policy, func() {
		v.mu.Lock()
		v.status = "in_progress"
		v.mu.Unlock()
		t.FirstTokenAt = timestamppb.Now()
	})
	v.mu.Lock()
	defer v.mu.Unlock()
	v.completed = time.Now()
	if err != nil {
		v.status = "failed"
		message := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			message = "the job ran past the route's request timeout"
		} else if errors.Is(err, context.Canceled) {
			message = "the job was cancelled"
		}
		v.failure = &struct{ code, message string }{"upstream_error", message}
		t.Error, t.Stop, t.Status = message, "failed", http.StatusBadGateway
		return
	}
	if done.Status != "completed" {
		v.status = "failed"
		v.failure = &struct{ code, message string }{done.Status, done.failure()}
		t.Error, t.Stop, t.Status = done.failure(), done.Status, http.StatusBadGateway
		return
	}
	var result struct {
		OutputFormat string `json:"output_format"`
		MimeType     string `json:"mime_type"`
		FPS          int    `json:"fps"`
		FrameCount   int    `json:"frame_count"`
		B64JSON      string `json:"b64_json"`
	}
	if err := json.Unmarshal(done.Result, &result); err != nil || result.B64JSON == "" {
		v.status = "failed"
		v.failure = &struct{ code, message string }{"upstream_error", "the runtime finished the job without a video"}
		t.Error, t.Stop, t.Status = v.failure.message, "failed", http.StatusBadGateway
		return
	}
	data, err := base64.StdEncoding.DecodeString(result.B64JSON)
	if err != nil {
		v.status = "failed"
		v.failure = &struct{ code, message string }{"upstream_error", "the video's bytes could not be decoded: " + err.Error()}
		t.Error, t.Stop, t.Status = v.failure.message, "failed", http.StatusBadGateway
		return
	}
	v.status, v.data, v.format, v.mime, v.fpsOut, v.frameCount = "completed", data, firstOf(result.OutputFormat, v.format), firstOf(result.MimeType, videoMime(result.OutputFormat), v.mime), result.FPS, result.FrameCount
	progress := 100
	v.progress = &progress
	t.Stop, t.Status, t.ResponseBytes = "stop", http.StatusOK, uint64(len(data))
	summary, _ := json.Marshal(map[string]any{"video": v.id, "output_format": v.format, "frames": v.frameCount, "fps": v.fpsOut, "bytes": len(data)})
	t.Response = string(summary)
}

func videoMime(format string) string {
	switch strings.ToLower(format) {
	case "webp":
		return "image/webp"
	case "avi":
		return "video/x-msvideo"
	case "mp4":
		return "video/mp4"
	}
	return "video/webm"
}

// Answers for videos by id: the list, one video's state, its file, or its removal
func (g *Gateway) video(w http.ResponseWriter, r *http.Request, client Flavor) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, videosPath), "/")
	if rest == "" {
		if r.Method != http.MethodGet {
			client.Error(w, http.StatusMethodNotAllowed, "videos are listed with GET and made with POST", "invalid_request_error")
			return
		}
		list := g.videos.list()
		data := make([]map[string]any, 0, len(list))
		for _, v := range list {
			data = append(data, v.object())
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
		return
	}
	id, sub, _ := strings.Cut(rest, "/")
	v, ok := g.videos.get(id)
	if !ok {
		client.Error(w, http.StatusNotFound, "video "+id+" is not known; finished videos are kept for a day", "not_found_error")
		return
	}
	switch {
	case r.Method == http.MethodDelete && sub == "":
		v.cancel()
		g.videos.remove(id)
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "video", "deleted": true})
	case r.Method == http.MethodGet && sub == "":
		writeJSON(w, http.StatusOK, v.object())
	case r.Method == http.MethodGet && sub == "content":
		v.mu.Lock()
		status, data, mimeType, format, failure := v.status, v.data, v.mime, v.format, v.failure
		v.mu.Unlock()
		switch status {
		case "completed":
			w.Header().Set("Content-Type", mimeType)
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Content-Disposition", `inline; filename="`+id+"."+format+`"`)
			w.WriteHeader(http.StatusOK)
			w.Write(data)
		case "failed":
			client.Error(w, http.StatusBadGateway, "video "+id+" failed: "+failure.message, failure.code)
		default:
			w.Header().Set("Retry-After", retryAfter)
			writeJSON(w, http.StatusAccepted, v.object())
		}
	default:
		client.Error(w, http.StatusNotFound, "no such video endpoint", "not_found_error")
	}
}
