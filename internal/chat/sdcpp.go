package chat

import (
	"encoding/json"
	"io"
	"net/http"
)

// The prefix of stable-diffusion.cpp's native paths, which pass through unchanged
const (
	SDCPPPrefix = "/sdcpp/"
	sdcppPrefix = SDCPPPrefix
)

// The stable-diffusion.cpp protocol, which takes no chat and answers errors in the OpenAI shape
type sdcpp struct{}

func notChat() error {
	return bad("this model generates images and video: send images to %s or %s and video to %s", OpenAIImages, OpenAIEdits, OpenAIVideos)
}

func (sdcpp) headers() []string { return nil }

func (sdcpp) Endpoint(string) Kind { return None }

func (sdcpp) Parse(string, []byte) (*Request, error) { return nil, notChat() }

func (sdcpp) Render(*Request) (string, []byte, error) { return "", nil, notChat() }

func (sdcpp) ParseResult(*Request, []byte) (*Result, error) { return nil, notChat() }

func (sdcpp) ParseStream(io.Reader, func(Event) error) error { return notChat() }

func (sdcpp) Answer(w http.ResponseWriter, req *Request, model string) Writer {
	return openai{}.Answer(w, req, model)
}

func (sdcpp) Error(w http.ResponseWriter, f Failure) { openai{}.Error(w, f) }

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

func (sdcpp) Traits() Traits { return Traits{} }
