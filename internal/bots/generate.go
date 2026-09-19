package bots

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/nickheyer/nebu/internal/gateway"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	// Maximum attachment size to read.
	attachmentMax = 25 << 20
	// Gateway trace ID header.
	traceHeader = "X-Nebu-Trace"
)

var (
	// Video polling interval.
	videoPoll = 2 * time.Second
	// Interval between Discord message updates while streaming.
	streamEdit = 1500 * time.Millisecond
)

// A file the bot posts
type file struct {
	name string
	mime string
	data []byte
}

// Language model response and gateway trace.
type answer struct {
	text  string
	trace string
}

// Uses the persona's route or the only ready route of the requested kind.
func (r *runner) route(p *v1.Persona, kind string) (string, error) {
	named := map[string]string{"chat": p.GetModel(), "image": p.GetImageModel(), "video": p.GetVideoModel()}[kind]
	if named != "" {
		return named, nil
	}
	var fits []string
	for _, rt := range r.m.Gateway.Table().Ready() {
		diffusion := rt.GetApi() == v1.ApiFlavor_API_FLAVOR_SDCPP
		switch kind {
		case "chat":
			if !diffusion {
				fits = append(fits, rt.GetName())
			}
		case "image":
			if diffusion && hasMode(rt, "img_gen") {
				fits = append(fits, rt.GetName())
			}
		case "video":
			if diffusion && hasMode(rt, "vid_gen") {
				fits = append(fits, rt.GetName())
			}
		}
	}
	sort.Strings(fits)
	switch len(fits) {
	case 0:
		return "", fmt.Errorf("no %s model is running. Use nebu run or choose a model for persona %s", kindWord(kind), p.GetName())
	case 1:
		return fits[0], nil
	}
	return "", fmt.Errorf("%d %s models are running (%s). Choose one for persona %s", len(fits), kindWord(kind), strings.Join(fits, ", "), p.GetName())
}

func kindWord(kind string) string {
	if kind == "chat" {
		return "language"
	}
	return kind
}

func hasMode(rt *v1.Route, mode string) bool {
	for _, m := range rt.GetModes() {
		if m == mode {
			return true
		}
	}
	return false
}

// Sends chat through the gateway and calls onDelta for streamed fragments.
func (r *runner) chat(ctx context.Context, p *v1.Persona, model string, chat *gateway.Chat, onDelta func(string)) (*answer, error) {
	chat.Kind, chat.Model, chat.Stream = "chat", model, onDelta != nil
	s := p.GetSampling()
	chat.Temperature, chat.TopP, chat.TopK, chat.Stop = s.Temperature, s.TopP, nil, s.GetStop()
	if s.TopK != nil {
		k := int(s.GetTopK())
		chat.TopK = &k
	}
	if s.MaxTokens != nil {
		chat.MaxTokens = int(s.GetMaxTokens())
	}
	flavor := gateway.FlavorFor(v1.ApiFlavor_API_FLAVOR_OPENAI)
	path, body, err := flavor.RenderRequest(chat)
	if err != nil {
		return nil, err
	}
	resp, err := r.post(ctx, path, "application/json", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := &answer{trace: resp.Header.Get(traceHeader)}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return out, gatewayError(flavor, resp)
	}
	if !chat.Stream {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, attachmentMax))
		if err != nil {
			return out, err
		}
		res, err := flavor.ParseResult(chat, raw)
		if err != nil {
			return out, err
		}
		out.text = res.Text
		return out, nil
	}
	var text strings.Builder
	var failed error
	err = flavor.ParseStream(resp.Body, func(ev gateway.Event) error {
		switch ev.Kind {
		case "text":
			text.WriteString(ev.Text)
			onDelta(ev.Text)
		case "error":
			failed = errors.New(ev.Text)
		}
		return nil
	})
	out.text = text.String()
	if err != nil {
		return out, err
	}
	return out, failed
}

// Generates images using the persona's model and returns files.
func (r *runner) images(ctx context.Context, p *v1.Persona, model, prompt, initImage string, count int) ([]file, string, error) {
	media := r.spec.GetMedia()
	if style := strings.TrimSpace(p.GetImageStyle()); style != "" {
		prompt = strings.TrimSpace(prompt) + ", " + style
	}
	if count <= 0 {
		count = int(media.GetImageCount())
	}
	req := map[string]any{"model": model, "prompt": prompt, "n": count, "size": media.GetImageSize(), "response_format": "b64_json"}
	if neg := p.GetNegativePrompt(); neg != "" {
		req["negative_prompt"] = neg
	}
	if media.GetImageSteps() > 0 {
		req["steps"] = media.GetImageSteps()
	}
	if initImage != "" {
		req["init_image"] = initImage
	}
	body, _ := json.Marshal(req)
	resp, err := r.post(ctx, "/v1/images/generations", "application/json", body)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	trace := resp.Header.Get(traceHeader)
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, trace, gatewayError(gateway.FlavorFor(v1.ApiFlavor_API_FLAVOR_OPENAI), resp)
	}
	var res struct {
		OutputFormat string `json:"output_format"`
		Data         []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, attachmentMax*4)).Decode(&res); err != nil {
		return nil, trace, fmt.Errorf("the gateway answered in a shape the bot could not read: %w", err)
	}
	format := strings.ToLower(res.OutputFormat)
	if format == "" || format == "jpg" {
		format = "png"
	}
	var out []file
	for i, d := range res.Data {
		data, err := base64.StdEncoding.DecodeString(d.B64JSON)
		if err != nil {
			return nil, trace, fmt.Errorf("image %d could not be decoded: %w", i+1, err)
		}
		out = append(out, file{name: fmt.Sprintf("image-%d.%s", i+1, format), mime: "image/" + format, data: data})
	}
	if len(out) == 0 {
		return nil, trace, errors.New("the image model answered with no images")
	}
	return out, trace, nil
}

// Generates a video and returns the completed file.
func (r *runner) video(ctx context.Context, p *v1.Persona, model, prompt, initImage string, controlFrames []string) (*file, string, error) {
	media := r.spec.GetMedia()
	req := map[string]any{"model": model, "prompt": prompt}
	if neg := p.GetNegativePrompt(); neg != "" {
		req["negative_prompt"] = neg
	}
	if media.GetVideoSize() != "" {
		req["size"] = media.GetVideoSize()
	}
	if media.GetVideoSeconds() > 0 {
		req["seconds"] = media.GetVideoSeconds()
	}
	if media.GetVideoFps() > 0 {
		req["fps"] = media.GetVideoFps()
	}
	if initImage != "" {
		req["init_image"] = initImage
	}
	if len(controlFrames) > 0 {
		req["control_frames"] = controlFrames
	}
	body, _ := json.Marshal(req)
	resp, err := r.post(ctx, "/v1/videos", "application/json", body)
	if err != nil {
		return nil, "", err
	}
	trace := resp.Header.Get(traceHeader)
	flavor := gateway.FlavorFor(v1.ApiFlavor_API_FLAVOR_OPENAI)
	if resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		return nil, trace, gatewayError(flavor, resp)
	}
	var started struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&started)
	resp.Body.Close()
	if err != nil || started.ID == "" {
		return nil, trace, errors.New("the gateway accepted the video without naming it")
	}
	for {
		select {
		case <-ctx.Done():
			r.delete(started.ID)
			return nil, trace, ctx.Err()
		case <-time.After(videoPoll):
		}
		status, err := r.get(ctx, "/v1/videos/"+started.ID)
		if err != nil {
			return nil, trace, err
		}
		var v struct {
			Status       string `json:"status"`
			OutputFormat string `json:"output_format"`
			MimeType     string `json:"mime_type"`
			Error        *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		raw, _ := io.ReadAll(io.LimitReader(status.Body, 1<<20))
		status.Body.Close()
		if status.StatusCode >= http.StatusMultipleChoices {
			return nil, trace, errors.New(firstNonEmpty(flavor.ErrorMessage(raw), strings.TrimSpace(string(raw))))
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, trace, err
		}
		switch v.Status {
		case "failed":
			message := "the video failed"
			if v.Error != nil && v.Error.Message != "" {
				message = v.Error.Message
			}
			return nil, trace, errors.New(message)
		case "completed":
			content, err := r.get(ctx, "/v1/videos/"+started.ID+"/content")
			if err != nil {
				return nil, trace, err
			}
			data, err := io.ReadAll(io.LimitReader(content.Body, int64(media.GetMaxUploadBytes())+1))
			content.Body.Close()
			if err != nil {
				return nil, trace, err
			}
			r.delete(started.ID)
			format := firstNonEmpty(v.OutputFormat, "webm")
			return &file{name: "video." + format, mime: firstNonEmpty(v.MimeType, content.Header.Get("Content-Type"), "video/webm"), data: data}, trace, nil
		}
	}
}

func (r *runner) post(ctx context.Context, path, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.m.Gateway.LocalBase()+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return r.client.Do(req)
}

func (r *runner) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.m.Gateway.LocalBase()+path, nil)
	if err != nil {
		return nil, err
	}
	return r.client.Do(req)
}

// Deletes completed or abandoned videos with a separate timeout.
func (r *runner) delete(videoID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, r.m.Gateway.LocalBase()+"/v1/videos/"+videoID, nil)
	if err != nil {
		return
	}
	if resp, err := r.client.Do(req); err == nil {
		resp.Body.Close()
	}
}

// Extracts the gateway error message.
func gatewayError(flavor gateway.Flavor, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	message := flavor.ErrorMessage(raw)
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%s", message)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Reads an attachment within the size limit.
func (r *runner) download(ctx context.Context, url string, size int) ([]byte, error) {
	if size > attachmentMax {
		return nil, fmt.Errorf("attachment is %d bytes, over the %d the bot reads", size, attachmentMax)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.web.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("attachment answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, attachmentMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > attachmentMax {
		return nil, fmt.Errorf("attachment is over the %d bytes the bot reads", attachmentMax)
	}
	return data, nil
}

func isImage(a *discordgo.MessageAttachment) bool {
	return strings.HasPrefix(a.ContentType, "image/")
}

func isVideo(a *discordgo.MessageAttachment) bool {
	return strings.HasPrefix(a.ContentType, "video/")
}

// Converts attachments into chat parts and samples configured video frames.
// Videos that cannot be sampled are described in text.
func (r *runner) attachmentParts(ctx context.Context, m *discordgo.Message) ([]gateway.Part, []string, error) {
	var parts []gateway.Part
	var notes []string
	for _, a := range m.Attachments {
		switch {
		case isImage(a):
			data, err := r.download(ctx, a.URL, a.Size)
			if err != nil {
				return nil, nil, fmt.Errorf("image %s: %w", a.Filename, err)
			}
			parts = append(parts, gateway.Part{Type: "image", MediaType: strings.Split(a.ContentType, ";")[0], Data: base64.StdEncoding.EncodeToString(data)})
		case isVideo(a):
			frames := int(r.spec.GetMedia().GetVideoFrames())
			if frames == 0 {
				notes = append(notes, "[video attachment: "+a.Filename+"]")
				continue
			}
			data, err := r.download(ctx, a.URL, a.Size)
			if err != nil {
				return nil, nil, fmt.Errorf("video %s: %w", a.Filename, err)
			}
			sampled, err := r.frames(ctx, data, a.Filename, frames)
			if err != nil {
				return nil, nil, fmt.Errorf("video %s: %w", a.Filename, err)
			}
			notes = append(notes, fmt.Sprintf("[video attachment %s, %d frames follow in order]", a.Filename, len(sampled)))
			for _, f := range sampled {
				parts = append(parts, gateway.Part{Type: "image", MediaType: "image/png", Data: f})
			}
		default:
			notes = append(notes, "[attachment: "+a.Filename+"]")
		}
	}
	return parts, notes, nil
}

// Samples evenly spaced video frames with ffmpeg and returns base64 PNGs.
func (r *runner) frames(ctx context.Context, data []byte, name string, want int) ([]string, error) {
	binary := r.m.FFmpeg
	if binary == "" {
		binary = "ffmpeg"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found. Install it or set discord.ffmpeg to sample video")
	}
	dir, err := os.MkdirTemp("", "nebu-frames-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "input"+filepath.Ext(name))
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return nil, err
	}
	// Sample at 1 fps, up to twice the requested count, then thin long clips.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-loglevel", "error", "-i", in, "-vf", "fps=1,scale='min(768,iw)':-2", "-frames:v", fmt.Sprint(want*4), filepath.Join(dir, "frame-%04d.png"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %s", strings.TrimSpace(firstNonEmpty(string(out), err.Error())))
	}
	names, err := filepath.Glob(filepath.Join(dir, "frame-*.png"))
	if err != nil || len(names) == 0 {
		return nil, errors.New("ffmpeg produced no frames")
	}
	sort.Strings(names)
	chosen := thin(names, want)
	out := make([]string, 0, len(chosen))
	for _, n := range chosen {
		raw, err := os.ReadFile(n)
		if err != nil {
			return nil, err
		}
		out = append(out, base64.StdEncoding.EncodeToString(raw))
	}
	return out, nil
}

// Selects evenly spaced items, keeping all if the list is shorter than want.
func thin(items []string, want int) []string {
	if want <= 0 || len(items) <= want {
		return items
	}
	if want == 1 {
		return items[len(items)/2 : len(items)/2+1]
	}
	out := make([]string, 0, want)
	for i := 0; i < want; i++ {
		out = append(out, items[i*(len(items)-1)/(want-1)])
	}
	return out
}
