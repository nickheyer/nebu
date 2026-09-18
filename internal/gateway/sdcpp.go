package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// The native API of stable-diffusion.cpp's server, passed through for a client that speaks it
	sdcppPrefix       = "/sdcpp/"
	sdcppCapabilities = "/sdcpp/v1/capabilities"
	sdcppImageJob     = "/sdcpp/v1/img_gen"
	sdcppVideoJob     = "/sdcpp/v1/vid_gen"
	sdcppJobs         = "/sdcpp/v1/jobs/"
)

// stable-diffusion.cpp's server, which makes images and video and answers no chat, completion, or embedding request
//
// Such requests reach a diffusion route through the OpenAI paths, so translation refuses them in words
// that name the endpoints the route does answer. The native paths pass through unread.
type sdcpp struct{}

func notChat() error {
	return bad("this model generates images and video: send images to %s or %s and video to %s", imagesPath, editsPath, videosPath)
}

func (sdcpp) ParseRequest(string, []byte) (*Chat, error)  { return nil, notChat() }
func (sdcpp) RenderRequest(*Chat) (string, []byte, error) { return "", nil, notChat() }
func (sdcpp) ParseResult(*Chat, []byte) (*Result, error)  { return nil, notChat() }
func (sdcpp) RenderResult(*Chat, *Result) ([]byte, error) { return nil, notChat() }
func (sdcpp) ParseStream(io.Reader, func(Event) error) error {
	return notChat()
}
func (sdcpp) Stream(w http.ResponseWriter, c *Chat) StreamWriter { return openai{}.Stream(w, c) }
func (sdcpp) ErrorMessage(body []byte) string {
	if m := errorField(body); m != "" {
		return m
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return ""
}
func (sdcpp) Error(w http.ResponseWriter, status int, message, kind string) {
	openai{}.Error(w, status, message, kind)
}
func (sdcpp) InlineImages() bool { return false }
func (sdcpp) CountsImages() bool { return false }

// Capabilities asks a running sd-server what it generates, img_gen and vid_gen, as its capabilities endpoint lists them
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
		return nil, fmt.Errorf("capabilities list no generation mode")
	}
	return caps.SupportedModes, nil
}
