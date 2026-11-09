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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Native stable-diffusion.cpp paths
	sdcppCapabilities = "/sdcpp/v1/capabilities"
	sdcppImageJob     = "/sdcpp/v1/img_gen"
	sdcppVideoJob     = "/sdcpp/v1/vid_gen"
	sdcppJobs         = "/sdcpp/v1/jobs/"
	// Job polling interval
	jobPoll = 500 * time.Millisecond
	// How long a cancelled job may keep running before the gateway reports it as stuck
	cancelWait = 2 * time.Minute
	// How long a finished video stays retrievable
	videoKeep = 24 * time.Hour
	// Videos kept at most, the oldest finished ones evicted past it
	videoLimit = 32
	// Bytes of video output held in memory at most, the oldest finished ones evicted past it
	videoBytes = 1 << 30
)

// Media request with OpenAI fields and native sampler settings
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
	// Input images for edits and video generation
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
	// Native schema fields that override the fields above
	SDCpp map[string]json.RawMessage `json:"sd_cpp"`
}

// Sampler settings for one stage, including the high-noise stage
type sampleParams struct {
	Steps     int      `json:"steps"`
	CfgScale  *float64 `json:"cfg_scale"`
	Guidance  *float64 `json:"guidance"`
	FlowShift *float64 `json:"flow_shift"`
	Sampler   string   `json:"sampler"`
	Scheduler string   `json:"scheduler"`
}

// Multipart fields and files by name
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
		return nil, errors.New("not a multipart form")
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

// Reads a media request from the multipart form parsed on arrival, or from the JSON body when there is none
func parseMedia(f *form, body []byte) (*mediaRequest, error) {
	req := &mediaRequest{}
	if f != nil {
		req.Model, req.Prompt, req.NegativePrompt, req.Size, req.OutputFormat, req.ResponseFormat = f.value("model"), f.value("prompt"), f.value("negative_prompt"), f.value("size"), f.value("output_format"), f.value("response_format")
		req.N, _ = strconv.Atoi(f.value("n"))
		req.Steps, _ = strconv.Atoi(f.value("steps"))
		req.Sampler, req.Scheduler = f.value("sampler"), f.value("scheduler")
		for name, into := range map[string]**float64{"cfg_scale": &req.CfgScale, "guidance": &req.Guidance, "strength": &req.Strength} {
			if v, err := strconv.ParseFloat(f.value(name), 64); err == nil {
				*into = &v
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

// Reads dimensions from width and height fields or a WIDTHxHEIGHT size
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

// Maps explicit request settings to native fields, preserving server defaults
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

// The fields image and video jobs share
func (m *mediaRequest) common(job map[string]any) {
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
	if s := sampling(m.Steps, m.CfgScale, m.ImgCfgScale, m.Guidance, m.FlowShift, m.Eta, m.Sampler, m.Scheduler, m.Slg); len(s) > 0 {
		job["sample_params"] = s
	}
	if len(m.Lora) > 0 {
		job["lora"] = m.Lora
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
}

// Builds the native image job body
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
	m.common(job)
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
	if len(m.Hires) > 0 {
		job["hires"] = m.Hires
	}
	if m.VaeTiling != nil {
		job["vae_tiling_params"] = map[string]any{"enabled": *m.VaeTiling}
	}
	for k, v := range m.SDCpp {
		job[k] = v
	}
	return job, nil
}

// Calculates frames from a count or duration and FPS, rounded up to 4n+1
func (m *mediaRequest) frames() (int, int) {
	fps := m.FPS
	frames := m.Frames
	if frames == 0 {
		frames = m.VideoFrames
	}
	if frames == 0 && m.Seconds > 0 {
		rate := fps
		if rate == 0 {
			rate = 16
		}
		frames = int(math.Round(m.Seconds * float64(rate)))
	}
	if frames > 1 {
		frames = ((frames-2)/4+1)*4 + 1
	}
	return frames, fps
}

// Builds the native video job body
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
	m.common(job)
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
	if hn := m.HighNoise; hn != nil {
		if s := sampling(hn.Steps, hn.CfgScale, nil, hn.Guidance, hn.FlowShift, nil, hn.Sampler, hn.Scheduler, nil); len(s) > 0 {
			job["high_noise_sample_params"] = s
		}
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
	for k, v := range m.SDCpp {
		job[k] = v
	}
	return job, nil
}

// Native job status
type sdJob struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Whether the job is terminal
func (j *sdJob) done() bool {
	return j.Status == "completed" || j.Status == "failed" || j.Status == "cancelled"
}

// Error message for a failed or cancelled job
func (j *sdJob) failure() string {
	if j.Error != nil && j.Error.Message != "" {
		return j.Error.Message
	}
	return "job " + j.Status
}

// Calls a native path on an endpoint over the pooled HTTP transport, answering the status and body
func (g *Gateway) native(ctx context.Context, method string, ep datalayer.Endpoint, path string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+ep.Address().String()+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.transport(datalayer.AppProtocolHTTP, 0, idleUncapped).RoundTrip(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, raw, err
}

// Submits a job down the decision's endpoints, refusals passing it on, and answers it as accepted
func (x *exchange) submit(ctx context.Context, path string, job map[string]any) (*sdJob, error) {
	body, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	x.sent = body
	out := &upstream{method: http.MethodPost, path: path, body: body, header: x.runtimeHeaders()}
	resp, ep, err := x.attempt(ctx, out, x.dec.Endpoints)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := x.received(ctx, resp, ep); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return nil, chat.Refusal(chat.RuntimeOf(v1.ApiFlavor_API_FLAVOR_SDCPP), resp.StatusCode, resp.Header, raw)
	}
	var accepted sdJob
	if err := json.Unmarshal(raw, &accepted); err != nil || accepted.ID == "" {
		return nil, fmt.Errorf("the runtime accepted the job without naming it: %s", strings.TrimSpace(string(raw)))
	}
	return &accepted, nil
}

// Polls until the job ends, calling started when it leaves the queue and cancelling it when the caller goes
func (g *Gateway) await(ctx context.Context, ep datalayer.Endpoint, id string, started func()) (*sdJob, error) {
	begun := false
	for {
		status, raw, err := g.native(ctx, http.MethodGet, ep, sdcppJobs+id, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil, g.abandon(ctx, ep, id)
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
			started()
		}
		if job.done() {
			return &job, nil
		}
		select {
		case <-ctx.Done():
			return nil, g.abandon(ctx, ep, id)
		case <-time.After(jobPoll):
		}
	}
}

// A job the caller left before it ended, with the runtime's answer to the cancel that followed
type abandoned struct {
	cause error
	stop  error
}

func (e *abandoned) Error() string {
	why := "the request was cancelled before the job finished"
	if errors.Is(e.cause, context.DeadlineExceeded) {
		why = "the job ran past the route's request timeout"
	}
	if e.stop != nil {
		return why + ", and the runtime kept generating: " + e.stop.Error()
	}
	return why
}

func (e *abandoned) Unwrap() error { return e.cause }

// Cancels the job after the caller's context ends and reports whether the runtime stopped it
func (g *Gateway) abandon(ctx context.Context, ep datalayer.Endpoint, id string) error {
	left := &abandoned{cause: ctx.Err(), stop: g.cancelJob(ep, id)}
	if left.stop != nil {
		g.log.Warn("gateway cancel job", "job", id, "err", left.stop)
	}
	return left
}

// Cancels the job on its own timeout and waits until the runtime reports it ended
func (g *Gateway) cancelJob(ep datalayer.Endpoint, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cancelWait)
	defer cancel()
	status, raw, err := g.native(ctx, http.MethodPost, ep, sdcppJobs+id+"/cancel", nil)
	if err != nil {
		return fmt.Errorf("cancelling job %s: %w", id, err)
	}
	switch status {
	case http.StatusOK, http.StatusAccepted:
	case http.StatusNotFound, http.StatusGone:
		return nil
	default:
		message := chat.RuntimeOf(v1.ApiFlavor_API_FLAVOR_SDCPP).ErrorMessage(raw)
		return fmt.Errorf("the runtime refused to cancel job %s (%d): %s", id, status, firstOf(message, strings.TrimSpace(string(raw))))
	}
	last := "generating"
	for {
		status, raw, err := g.native(ctx, http.MethodGet, ep, sdcppJobs+id, nil)
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("job %s was still %s %s after the runtime accepted the cancel", id, last, cancelWait)
		case err != nil:
			return fmt.Errorf("job %s after cancel: %w", id, err)
		case status == http.StatusNotFound || status == http.StatusGone:
			return nil
		case status != http.StatusOK:
			return fmt.Errorf("job %s answered %d after cancel: %s", id, status, strings.TrimSpace(string(raw)))
		}
		var job sdJob
		if err := json.Unmarshal(raw, &job); err != nil {
			return fmt.Errorf("job %s after cancel: %w", id, err)
		}
		if job.done() {
			return nil
		}
		last = job.Status
		select {
		case <-ctx.Done():
		case <-time.After(jobPoll):
		}
	}
}

// Generates images or starts a video on the decision's endpoint, the job holding the endpoint until it ends
func (x *exchange) media(ctx context.Context) {
	if x.upstream != v1.ApiFlavor_API_FLAVOR_SDCPP {
		x.refuse(chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: "model " + x.model + " is a language model and generates no images or video"})
		return
	}
	req, err := parseMedia(x.form, x.body)
	if err != nil {
		x.fail(err)
		return
	}
	modes := x.g.instances.Modes(x.dec.Endpoints[0].Labels()[datalayer.LabelInstance])
	if x.kind == chat.Video {
		if !slices.Contains(modes, "vid_gen") {
			x.refuse(chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: "model " + x.model + " generates images, not video: send it to " + chat.OpenAIImages})
			return
		}
		x.startVideo(ctx, req)
		return
	}
	if !slices.Contains(modes, "img_gen") {
		x.refuse(chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: "model " + x.model + " generates video, not images: send it to " + chat.OpenAIVideos})
		return
	}
	x.image(ctx, req)
}

// Answers the media failure, runtime refusals by their status and the rest as upstream failures
func (x *exchange) mediaError(err error) {
	x.resp.Err = err
	if refusal, ok := errors.AsType[*chat.Failure](err); ok {
		x.t.Error = refusal.Message
		x.refuse(upstreamFailure(x.model, &chat.Failure{Status: refusal.Status, Code: chat.CodeUpstream, Message: "upstream: " + refusal.Message, RetryAfter: refusal.RetryAfter}))
		return
	}
	x.refuse(upstreamFailure(x.model, err))
}

// Returns generated images within the request's timeout
func (x *exchange) image(ctx context.Context, req *mediaRequest) {
	job, err := req.imageJob()
	if err != nil {
		x.fail(err)
		return
	}
	accepted, err := x.submit(ctx, sdcppImageJob, job)
	if err != nil {
		x.mediaError(err)
		return
	}
	done, err := x.g.await(ctx, x.served, accepted.ID, func() { x.resp.FirstTokenAt = x.g.clock.Now(); x.t.FirstTokenAt = timestamppb.New(x.resp.FirstTokenAt) })
	if err != nil {
		if left, ok := errors.AsType[*abandoned](err); ok && errors.Is(err, context.Canceled) {
			x.t.Error, x.t.Stop, x.resp.Err = left.Error(), "cancelled", err
			return
		}
		x.mediaError(err)
		return
	}
	if done.Status != "completed" {
		x.t.Stop = done.Status
		x.mediaError(&chat.Failure{Status: http.StatusBadGateway, Message: done.failure()})
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
		x.mediaError(&chat.Failure{Status: http.StatusBadGateway, Message: "the runtime answered with no images"})
		return
	}
	data := make([]map[string]any, 0, len(result.Images))
	for _, img := range result.Images {
		data = append(data, map[string]any{"index": img.Index, "b64_json": img.B64JSON})
	}
	width, height, _ := req.shape()
	answer := map[string]any{"created": time.Now().Unix(), "model": x.model, "output_format": result.OutputFormat, "data": data}
	if width > 0 && height > 0 {
		answer["size"] = fmt.Sprintf("%dx%d", width, height)
	}
	if req.Seed != nil {
		answer["seed"] = *req.Seed
	}
	x.t.Stop = "stop"
	summary, _ := json.Marshal(map[string]any{"images": len(data), "output_format": result.OutputFormat, "size": answer["size"]})
	x.t.Response = string(summary)
	chat.WriteJSON(x.w, http.StatusOK, answer)
}

// A video job, its state and its output
type video struct {
	mu         sync.Mutex
	id         string
	owner      string
	model      string
	trace      string
	prompt     string
	status     string
	progress   *int
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
	failure    *videoFailure
	data       []byte
	cancel     context.CancelFunc
	// Whether the job stays out of listings and keeps no trace
	ghost bool
}

type videoFailure struct{ code, message string }

// The video as the API answers it
func (v *video) object() map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]any{
		"id":            v.id,
		"object":        "video",
		"model":         v.model,
		"status":        v.status,
		"progress":      v.progress,
		"created_at":    v.created.Unix(),
		"expires_at":    v.created.Add(videoKeep).Unix(),
		"prompt":        v.prompt,
		"output_format": v.format,
		"mime_type":     v.mime,
		"trace":         v.trace,
		"error":         nil,
		"completed_at":  nil,
	}
	if v.width > 0 && v.height > 0 {
		out["size"] = fmt.Sprintf("%dx%d", v.width, v.height)
	}
	frames, fps := v.frames, v.fps
	if v.frameCount > 0 {
		frames = v.frameCount
	}
	if v.fpsOut > 0 {
		fps = v.fpsOut
	}
	if frames > 0 {
		out["frames"] = frames
	}
	if fps > 0 {
		out["fps"] = fps
	}
	if frames > 0 && fps > 0 {
		out["seconds"] = math.Round(float64(frames)/float64(fps)*100) / 100
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

// Videos by id, the oldest finished ones evicted past the count and byte bounds
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

// The video of an id the viewer may reach
func (s *videoStore) get(id string, viewer Viewer) (*video, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.byID[id]
	if !ok || !viewer.sees(v.owner) {
		return nil, false
	}
	return v, true
}

func (s *videoStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

// The viewer's videos newest first, ghost jobs left out
func (s *videoStore) list(viewer Viewer) []*video {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	out := make([]*video, 0, len(s.byID))
	for _, v := range s.byID {
		if !v.ghost && viewer.sees(v.owner) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].created.After(out[j].created) })
	return out
}

// Evicts expired videos, then the oldest finished videos above the count or byte limit
func (s *videoStore) pruneLocked() {
	var finished []*video
	held := 0
	for id, v := range s.byID {
		v.mu.Lock()
		done := v.status == "completed" || v.status == "failed"
		old := done && time.Since(v.created) > videoKeep
		size := len(v.data)
		v.mu.Unlock()
		if old {
			delete(s.byID, id)
			continue
		}
		held += size
		if done {
			finished = append(finished, v)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].created.Before(finished[j].created) })
	for (len(s.byID) > videoLimit || held > videoBytes) && len(finished) > 0 {
		delete(s.byID, finished[0].id)
		finished[0].mu.Lock()
		held -= len(finished[0].data)
		finished[0].mu.Unlock()
		finished = finished[1:]
	}
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

// Starts a video job, answers it as accepted and follows it in the background, the request ending with the job
func (x *exchange) startVideo(ctx context.Context, req *mediaRequest) {
	job, err := req.videoJob()
	if err != nil {
		x.fail(err)
		return
	}
	jctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if d := x.policy.GetRequestTimeoutMs(); d > 0 {
		jctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), time.Duration(d)*time.Millisecond)
	}
	accepted, err := x.submit(jctx, sdcppVideoJob, job)
	if err != nil {
		cancel()
		x.mediaError(err)
		return
	}
	width, height, _ := req.shape()
	frames, fps := req.frames()
	format := firstOf(req.OutputFormat, "webm")
	trace := x.t.GetId()
	if x.modes.ghost {
		trace = ""
	}
	v := &video{id: "video_" + db.NewID(), owner: x.who.owner, model: x.model, trace: trace, prompt: req.Prompt, status: "queued", created: time.Now(), width: width, height: height, frames: frames, fps: fps, format: format, mime: videoMime(format), cancel: cancel, ghost: x.modes.ghost}
	x.g.videos.add(v)
	x.detached = true
	chat.WriteJSON(x.w, http.StatusAccepted, v.object())
	go x.followVideo(jctx, cancel, v, accepted.ID)
}

// Tracks a video job and keeps its output, completing the request and its trace once it ends
func (x *exchange) followVideo(ctx context.Context, cancel context.CancelFunc, v *video, jobID string) {
	defer cancel()
	defer x.finish()
	defer x.complete(ctx)
	t := x.t
	done, err := x.g.await(ctx, x.served, jobID, func() {
		v.mu.Lock()
		v.status = "in_progress"
		v.mu.Unlock()
		x.resp.FirstTokenAt = x.g.clock.Now()
		t.FirstTokenAt = timestamppb.New(x.resp.FirstTokenAt)
	})
	v.mu.Lock()
	defer v.mu.Unlock()
	v.completed = time.Now()
	fail := func(code, message string) {
		v.status, v.failure = "failed", &videoFailure{code, message}
		t.Error, t.Stop, x.w.status = message, "failed", http.StatusBadGateway
		x.resp.Err = errors.New(message)
	}
	if err != nil {
		fail(chat.CodeUpstream, err.Error())
		return
	}
	if done.Status != "completed" {
		fail(done.Status, done.failure())
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
		fail(chat.CodeUpstream, "the runtime finished the job without a video")
		return
	}
	data, err := base64.StdEncoding.DecodeString(result.B64JSON)
	if err != nil {
		fail(chat.CodeUpstream, "the video's bytes could not be decoded: "+err.Error())
		return
	}
	v.status, v.data, v.format, v.mime, v.fpsOut, v.frameCount = "completed", data, firstOf(result.OutputFormat, v.format), firstOf(result.MimeType, videoMime(result.OutputFormat), v.mime), result.FPS, result.FrameCount
	progress := 100
	v.progress = &progress
	t.Stop, x.w.status = "stop", http.StatusOK
	summary, _ := json.Marshal(map[string]any{"video": v.id, "output_format": v.format, "frames": v.frameCount, "fps": v.fpsOut, "bytes": len(data)})
	t.Response = string(summary)
}

// Lists the caller's videos with GET and starts one with POST
func (g *Gateway) videoRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		g.serve(w, r)
	case http.MethodGet:
		list := g.videos.list(viewerOf(identityOf(r)))
		data := make([]map[string]any, 0, len(list))
		for _, v := range list {
			data = append(data, v.object())
		}
		chat.WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	default:
		chat.ClientOf(v1.ApiFlavor_API_FLAVOR_OPENAI).Error(w, chat.Failure{Status: http.StatusMethodNotAllowed, Code: chat.CodeInvalidRequest, Message: "videos are listed with GET and made with POST"})
	}
}

// The viewer a request's identity makes, admins and open gateways seeing every account's jobs
func viewerOf(id identity) Viewer { return Viewer{Owner: id.owner, All: id.admin} }

// Answers one video's status, content or deletion, reaching the member running a forwarded job
func (g *Gateway) video(w http.ResponseWriter, r *http.Request) {
	client := chat.ClientOf(v1.ApiFlavor_API_FLAVOR_OPENAI)
	viewer := viewerOf(identityOf(r))
	id, sub, _ := strings.Cut(strings.Trim(strings.TrimPrefix(r.URL.Path, chat.OpenAIVideos), "/"), "/")
	v, ok := g.videos.get(id, viewer)
	if !ok {
		if node, forwarded := g.jobs.node(id, viewer); forwarded {
			g.forwardVideo(w, r, client, id, node)
			return
		}
		client.Error(w, chat.Failure{Status: http.StatusNotFound, Code: chat.CodeNotFound, Message: "video " + id + " not found. Finished videos are kept for one day"})
		return
	}
	switch {
	case r.Method == http.MethodDelete && sub == "":
		v.cancel()
		g.videos.remove(id)
		chat.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "object": "video", "deleted": true})
	case r.Method == http.MethodGet && sub == "":
		chat.WriteJSON(w, http.StatusOK, v.object())
	case r.Method == http.MethodGet && sub == "content":
		v.mu.Lock()
		status, data, mimeType, format, failed := v.status, v.data, v.mime, v.format, v.failure
		v.mu.Unlock()
		switch status {
		case "completed":
			w.Header().Set("Content-Type", mimeType)
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Content-Disposition", `inline; filename="`+id+"."+format+`"`)
			w.WriteHeader(http.StatusOK)
			w.Write(data)
		case "failed":
			client.Error(w, chat.Failure{Status: http.StatusBadGateway, Code: failed.code, Message: "video " + id + " failed: " + failed.message})
		default:
			w.Header().Set("Retry-After", strconv.Itoa(int(seconds(g.cfg.Retry.Unavailable)/time.Second)))
			chat.WriteJSON(w, http.StatusAccepted, v.object())
		}
	default:
		client.Error(w, chat.Failure{Status: http.StatusNotFound, Code: chat.CodeNotFound, Message: "no such video endpoint"})
	}
}

// Reads img_gen and vid_gen support from a stable-diffusion.cpp server
func Capabilities(ctx context.Context, client *http.Client, endpoint string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(endpoint, "/")+sdcppCapabilities, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("capabilities answered %d", resp.StatusCode)
	}
	var caps struct {
		SupportedModes []string `json:"supported_modes"`
	}
	if err := json.Unmarshal(raw, &caps); err != nil {
		return nil, fmt.Errorf("capabilities: %w", err)
	}
	if len(caps.SupportedModes) == 0 {
		return nil, errors.New("capabilities list no generation mode")
	}
	return caps.SupportedModes, nil
}
