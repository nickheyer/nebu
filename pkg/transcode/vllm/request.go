package vllm

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/nickheyer/nebu/pkg/transcode"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

// Host decided request id, priority, session, LoRA adapter, tool parser and detokenizer for one request
type Call struct {
	RequestID   string
	Priority    int32
	SessionID   string
	Adapter     string
	Tools       ToolParser
	Detokenizer Detokenizer
	// When set, prompts travel as token ids and answer text is built by the host from the ids the engine returns
	Tokenizer Tokenizer
}

// The incremental detokenizer tokens in tokens out mode needs, refused when the call lacks one
func (c Call) tokensOut() (StreamDetokenizer, error) {
	if c.Tokenizer == nil {
		return nil, nil
	}
	sd, ok := c.Detokenizer.(StreamDetokenizer)
	if !ok {
		return nil, transcode.NewError(http.StatusInternalServerError, "tokens in tokens out mode needs a detokenizer that decodes incrementally")
	}
	return sd, nil
}

// Returns the host request id or a fresh random one
func (c Call) requestID() string {
	if c.RequestID != "" {
		return c.RequestID
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type streamOptions struct {
	IncludeUsage         bool `json:"include_usage"`
	ContinuousUsageStats bool `json:"continuous_usage_stats"`
}

type structuredOutputs struct {
	JSON          json.RawMessage `json:"json"`
	Regex         *string         `json:"regex"`
	Choice        []string        `json:"choice"`
	Grammar       *string         `json:"grammar"`
	JSONObject    *bool           `json:"json_object"`
	StructuralTag *string         `json:"structural_tag"`
}

// The OpenAI stop field given as a single string or a list
type stopStrings []string

// Decodes a string, list of strings or null into stop strings
func (s *stopStrings) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case string(b) == "null":
		*s = nil
		return nil
	case len(b) > 0 && b[0] == '"':
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = stopStrings{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// Sampling, stopping and engine fields chat and completions share
type params struct {
	Model                  string             `json:"model"`
	N                      *int64             `json:"n"`
	BestOf                 *int64             `json:"best_of"`
	UseBeamSearch          bool               `json:"use_beam_search"`
	Temperature            *float32           `json:"temperature"`
	TopP                   *float32           `json:"top_p"`
	TopK                   *int64             `json:"top_k"`
	MinP                   *float32           `json:"min_p"`
	Seed                   *int64             `json:"seed"`
	Stop                   stopStrings        `json:"stop"`
	StopTokenIDs           []uint32           `json:"stop_token_ids"`
	IncludeStopStr         bool               `json:"include_stop_str_in_output"`
	IgnoreEOS              bool               `json:"ignore_eos"`
	MinTokens              uint32             `json:"min_tokens"`
	PresencePenalty        float32            `json:"presence_penalty"`
	FrequencyPenalty       float32            `json:"frequency_penalty"`
	RepetitionPenalty      float32            `json:"repetition_penalty"`
	LogitBias              map[string]float32 `json:"logit_bias"`
	AllowedTokenIDs        []uint32           `json:"allowed_token_ids"`
	BadWords               []string           `json:"bad_words"`
	SkipSpecialTokens      *bool              `json:"skip_special_tokens"`
	TruncatePromptTokens   *int64             `json:"truncate_prompt_tokens"`
	CacheSalt              string             `json:"cache_salt"`
	KVTransferParams       map[string]any     `json:"kv_transfer_params"`
	StructuredOutputs      *structuredOutputs `json:"structured_outputs"`
	ResponseFormat         json.RawMessage    `json:"response_format"`
	StreamOptions          *streamOptions     `json:"stream_options"`
	ReturnTokensAsTokenIDs bool               `json:"return_tokens_as_token_ids"`
	ReturnTokenIDs         bool               `json:"return_token_ids"`
	PromptLogprobs         *int64             `json:"prompt_logprobs"`
}

// Per endpoint choices that complete a generate request
type shape struct {
	maxTokens      uint32
	logprobs       bool
	topN           uint32
	promptLogprobs bool
	toolSchema     string
	// Whether the client asked for token ids beside the text
	tokenIDs bool
	// Whether the host decodes the answer text itself, the engine then skipping detokenization
	tokensOut bool
}

// Decodes a JSON object request body into v, reporting malformed bodies as 400
func decode(body []byte, v any) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return transcode.BadRequest("request body must be a JSON object")
	}
	if err := json.Unmarshal(trimmed, v); err != nil {
		return transcode.BadRequest("invalid request body: %v", err)
	}
	return nil
}

// Refuses fields the gRPC path cannot honor and options that conflict with the stream mode
func (p *params) validate(stream bool) error {
	switch {
	case p.N != nil && *p.N < 1:
		return transcode.BadRequest("n must be at least 1")
	case p.N != nil && *p.N > 1:
		return transcode.BadRequest("n above 1 is refused, the vLLM gRPC service generates one sequence")
	case p.BestOf != nil && *p.BestOf > 1:
		return transcode.BadRequest("best_of above 1 is refused, the vLLM gRPC service generates one sequence")
	case p.UseBeamSearch:
		return transcode.BadRequest("use_beam_search is refused, the vLLM gRPC service has no beam search")
	case p.PromptLogprobs != nil:
		return transcode.BadRequest("prompt_logprobs is refused, use echo with logprobs on completions")
	case len(p.BadWords) > 0:
		return transcode.BadRequest("bad_words is refused, the vLLM gRPC service takes bad words as token ids only")
	case p.StreamOptions != nil && !stream:
		return transcode.BadRequest("stream_options is only allowed when stream is true")
	}
	return nil
}

// Builds the generate request from the shared fields, the host call and the endpoint shape
func (p *params) generate(call Call, rid string, stream bool, s shape) (*vllmpb.GenerateRequest, error) {
	if err := p.validate(stream); err != nil {
		return nil, err
	}
	req := &vllmpb.GenerateRequest{
		RequestId:   rid,
		Model:       p.Model,
		Temperature: p.Temperature,
		Priority:    call.Priority,
	}
	if call.Adapter != "" {
		req.Model = ""
		req.LoraName = call.Adapter
	}
	if call.SessionID != "" {
		session := call.SessionID
		req.SessionId = &session
	}
	sampling, err := p.sampling()
	if err != nil {
		return nil, err
	}
	req.Sampling = sampling
	if req.Decoding, err = p.decoding(s.toolSchema); err != nil {
		return nil, err
	}
	req.Stopping = &vllmpb.StoppingCriteria{
		MaxNewTokens:       s.maxTokens,
		MinNewTokens:       p.MinTokens,
		StopTokenIds:       p.StopTokenIDs,
		StopStrings:        p.Stop,
		IncludeStopStrings: p.IncludeStopStr,
		IgnoreEos:          p.IgnoreEOS,
	}
	req.Response = &vllmpb.ResponseOptions{
		SkipSpecialTokens: p.SkipSpecialTokens,
		OutputLogprobs:    s.logprobs,
		OutputTokenIds:    s.logprobs || s.tokenIDs || s.tokensOut,
		PromptLogprobs:    s.promptLogprobs,
		PromptTokenIds:    s.promptLogprobs || s.tokenIDs,
	}
	if s.tokensOut && len(p.Stop) == 0 {
		noText := false
		req.Response.OutputText = &noText
	}
	if s.logprobs && s.topN > 0 {
		req.Response.OutputCandidates = &vllmpb.CandidateTokens{Select: &vllmpb.CandidateTokens_TopN{TopN: s.topN}}
	}
	if s.promptLogprobs && s.topN > 0 {
		req.Response.PromptCandidates = &vllmpb.CandidateTokens{Select: &vllmpb.CandidateTokens_TopN{TopN: s.topN}}
	}
	if req.TruncatePromptTokens, err = p.truncation(); err != nil {
		return nil, err
	}
	if p.CacheSalt != "" || p.KVTransferParams != nil {
		req.Kv = &vllmpb.KVCacheParameters{CacheSalt: p.CacheSalt}
		if p.KVTransferParams != nil {
			if req.Kv.KvTransferParams, err = structpb.NewStruct(p.KVTransferParams); err != nil {
				return nil, transcode.BadRequest("kv_transfer_params: %v", err)
			}
		}
	}
	return req, nil
}

// Maps top_k, top_p, min_p and seed, treating a top_k of zero or minus one as disabled
func (p *params) sampling() (*vllmpb.RandomSampling, error) {
	s := &vllmpb.RandomSampling{TopP: p.TopP, MinP: p.MinP, Seed: p.Seed}
	if p.TopK != nil {
		switch k := *p.TopK; {
		case k == -1 || k == 0:
			s.TopK = new(uint32)
		case k > 0 && k <= math.MaxUint32:
			v := uint32(k)
			s.TopK = &v
		default:
			return nil, transcode.BadRequest("top_k must be -1, 0 or a positive token count")
		}
	}
	return s, nil
}

// Maps penalties, logit bias, allowed tokens and the single structured output constraint
func (p *params) decoding(toolSchema string) (*vllmpb.DecodingParameters, error) {
	d := &vllmpb.DecodingParameters{
		PresencePenalty:   p.PresencePenalty,
		FrequencyPenalty:  p.FrequencyPenalty,
		RepetitionPenalty: p.RepetitionPenalty,
		AllowedTokenIds:   p.AllowedTokenIDs,
	}
	if len(p.LogitBias) > 0 {
		d.LogitBias = make(map[uint32]float32, len(p.LogitBias))
		for key, bias := range p.LogitBias {
			id, err := strconv.ParseUint(key, 10, 32)
			if err != nil {
				return nil, transcode.BadRequest("logit_bias key %q is not a token id", key)
			}
			d.LogitBias[uint32(id)] = bias
		}
	}
	constraints := 0
	if toolSchema != "" {
		d.StructuredOutput = &vllmpb.DecodingParameters_Json{Json: toolSchema}
		constraints++
	}
	set, err := applyResponseFormat(p.ResponseFormat, d)
	if err != nil {
		return nil, err
	}
	if set {
		constraints++
	}
	if set, err = applyStructuredOutputs(p.StructuredOutputs, d); err != nil {
		return nil, err
	}
	if set {
		constraints++
	}
	if constraints > 1 {
		return nil, transcode.BadRequest("tool_choice, response_format and structured_outputs each constrain output, use only one")
	}
	return d, nil
}

// Applies an OpenAI response_format as a structured output constraint and reports whether one was set
func applyResponseFormat(raw json.RawMessage, d *vllmpb.DecodingParameters) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var rf struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &rf); err != nil {
		return false, transcode.BadRequest("response_format: %v", err)
	}
	switch rf.Type {
	case "text":
		return false, nil
	case "json_object":
		d.StructuredOutput = &vllmpb.DecodingParameters_JsonObject{JsonObject: true}
	case "json_schema":
		if rf.JSONSchema == nil || len(rf.JSONSchema.Schema) == 0 {
			return false, transcode.BadRequest("response_format json_schema needs json_schema.schema")
		}
		d.StructuredOutput = &vllmpb.DecodingParameters_Json{Json: compact(rf.JSONSchema.Schema)}
	case "structural_tag":
		d.StructuredOutput = &vllmpb.DecodingParameters_StructuralTag{StructuralTag: compact(raw)}
	default:
		return false, transcode.BadRequest("response_format type %q is unknown", rf.Type)
	}
	return true, nil
}

// Applies vLLM structured_outputs as a constraint, refusing more than one kind, and reports whether one was set
func applyStructuredOutputs(so *structuredOutputs, d *vllmpb.DecodingParameters) (bool, error) {
	if so == nil {
		return false, nil
	}
	kinds := 0
	if len(so.JSON) > 0 && string(so.JSON) != "null" {
		schema := compact(so.JSON)
		var text string
		if json.Unmarshal(so.JSON, &text) == nil {
			schema = text
		}
		d.StructuredOutput = &vllmpb.DecodingParameters_Json{Json: schema}
		kinds++
	}
	if so.Regex != nil {
		d.StructuredOutput = &vllmpb.DecodingParameters_Regex{Regex: *so.Regex}
		kinds++
	}
	if so.Choice != nil {
		d.StructuredOutput = &vllmpb.DecodingParameters_Choice{Choice: &vllmpb.DecodingParameters_StringChoices{Choices: so.Choice}}
		kinds++
	}
	if so.Grammar != nil {
		d.StructuredOutput = &vllmpb.DecodingParameters_Grammar{Grammar: *so.Grammar}
		kinds++
	}
	if so.JSONObject != nil && *so.JSONObject {
		d.StructuredOutput = &vllmpb.DecodingParameters_JsonObject{JsonObject: true}
		kinds++
	}
	if so.StructuralTag != nil {
		d.StructuredOutput = &vllmpb.DecodingParameters_StructuralTag{StructuralTag: *so.StructuralTag}
		kinds++
	}
	if kinds > 1 {
		return false, transcode.BadRequest("structured_outputs allows one constraint kind")
	}
	return kinds == 1, nil
}

// Maps truncate_prompt_tokens onto the left truncation length the proto carries
func (p *params) truncation() (uint32, error) {
	if p.TruncatePromptTokens == nil {
		return 0, nil
	}
	n := *p.TruncatePromptTokens
	if n < 1 || n > math.MaxUint32 {
		return 0, transcode.BadRequest("truncate_prompt_tokens must be a positive token count")
	}
	return uint32(n), nil
}

// Validates a max token count from the request against the proto range
func maxTokens(field string, n int64) (uint32, error) {
	if n < 1 || n > math.MaxUint32 {
		return 0, transcode.BadRequest("%s must be a positive token count", field)
	}
	return uint32(n), nil
}

// Returns decoder validated JSON text without insignificant whitespace
func compact(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		panic(err)
	}
	return buf.String()
}
