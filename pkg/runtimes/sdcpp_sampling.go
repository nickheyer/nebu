package runtimes

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The sampling a family runs well at: the step count and guidance its docs and model cards state, and
// the sampler, scheduler, and flow shift stable-diffusion.cpp picks for it when a request names none.
// Passing every one at launch means the server reports exactly what a request gets, so a client can
// show the figures rather than a word standing in for them.
type sdSample struct {
	Steps     int
	Cfg       float64
	Guidance  float64
	FlowShift float64
	Sampler   string
	Scheduler string
	// The step count a distilled release of the family takes, at guidance 1
	Distilled int
}

// stable-diffusion.cpp's own figures for a family it has no better word on
var sdSampleFallback = sdSample{Steps: 20, Cfg: 7, Guidance: 3.5, Sampler: "euler", Scheduler: "discrete", Distilled: 4}

// By family id as the diffusion profile names it. Steps and guidance follow the family's docs in
// stable-diffusion.cpp and the model card; the sampler follows its default_sample_method, the scheduler
// its default_scheduler, and the flow shift the value its denoiser setup gives the version.
var sdSamples = map[string]sdSample{
	"sd1":             {Steps: 20, Cfg: 7, Guidance: 3.5, Sampler: "euler_a", Scheduler: "discrete", Distilled: 4},
	"sd2":             {Steps: 20, Cfg: 7, Guidance: 3.5, Sampler: "euler_a", Scheduler: "discrete", Distilled: 4},
	"sdxl":            {Steps: 25, Cfg: 7, Guidance: 3.5, Sampler: "euler_a", Scheduler: "discrete", Distilled: 4},
	"svd":             {Steps: 25, Cfg: 2.5, Guidance: 3.5, Sampler: "euler_a", Scheduler: "discrete", Distilled: 4},
	"sd3":             {Steps: 28, Cfg: 4.5, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 4},
	"flux":            {Steps: 20, Cfg: 1, Guidance: 3.5, FlowShift: 1.15, Sampler: "euler", Scheduler: "flux", Distilled: 4},
	"chroma":          {Steps: 20, Cfg: 4, Guidance: 3.5, FlowShift: 1, Sampler: "euler", Scheduler: "flux", Distilled: 8},
	"chroma_radiance": {Steps: 20, Cfg: 4, Guidance: 3.5, FlowShift: 1, Sampler: "euler", Scheduler: "flux", Distilled: 8},
	"flux2":           {Steps: 20, Cfg: 1, Guidance: 4, FlowShift: 1, Sampler: "euler", Scheduler: "flux2", Distilled: 4},
	"flux2_klein":     {Steps: 4, Cfg: 1, Guidance: 4, FlowShift: 1, Sampler: "euler", Scheduler: "flux2", Distilled: 4},
	"wan":             {Steps: 20, Cfg: 6, Guidance: 3.5, FlowShift: 5, Sampler: "euler", Scheduler: "discrete", Distilled: 4},
	"lingbot_video":   {Steps: 20, Cfg: 3, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 4},
	"qwen_image":      {Steps: 20, Cfg: 2.5, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"hunyuan_video":   {Steps: 20, Cfg: 6, Guidance: 3.5, FlowShift: 7, Sampler: "euler", Scheduler: "discrete", Distilled: 4},
	"anima":           {Steps: 20, Cfg: 6, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"ltx2":            {Steps: 20, Cfg: 3, Guidance: 3.5, FlowShift: 2.37, Sampler: "euler", Scheduler: "ltx2", Distilled: 8},
	"minimax_h3":      {Steps: 20, Cfg: 1, Guidance: 3.5, FlowShift: 12, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"hidream_o1":      {Steps: 20, Cfg: 1, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"z_image":         {Steps: 20, Cfg: 5, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"boogu_image":     {Steps: 20, Cfg: 7, Guidance: 3.5, FlowShift: 3.16, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"ovis_image":      {Steps: 20, Cfg: 5, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"ernie_image":     {Steps: 20, Cfg: 5, Guidance: 3.5, FlowShift: 4, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"lens":            {Steps: 20, Cfg: 5, Guidance: 3.5, FlowShift: 1.83, Sampler: "euler", Scheduler: "discrete", Distilled: 4},
	"minit2i":         {Steps: 100, Cfg: 6, Guidance: 3.5, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"longcat":         {Steps: 20, Cfg: 5, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"pid":             {Steps: 4, Cfg: 1, Guidance: 3.5, FlowShift: 1.5, Sampler: "lcm", Scheduler: "lcm", Distilled: 4},
	"ideogram4":       {Steps: 20, Cfg: 7, Guidance: 3.5, FlowShift: 1, Sampler: "euler", Scheduler: "logit_normal", Distilled: 8},
	"sefi_image":      {Steps: 50, Cfg: 4, Guidance: 3.5, Sampler: "euler", Scheduler: "discrete", Distilled: 4},
	"krea2":           {Steps: 52, Cfg: 3.5, Guidance: 3.5, FlowShift: 1.15, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
	"mage_flow":       {Steps: 20, Cfg: 4, Guidance: 3.5, FlowShift: 6, Sampler: "euler", Scheduler: "discrete", Distilled: 4},
	"sensenova_u1":    {Steps: 50, Cfg: 4, Guidance: 3.5, FlowShift: 3, Sampler: "euler", Scheduler: "discrete", Distilled: 8},
}

// The words a distilled release carries in its name, each with the step count that word usually means; zero
// leaves the count to the family
var sdDistilledWords = map[string]int{
	"turbo": 0, "schnell": 4, "lightning": 4, "hyper": 8, "lcm": 4, "dmd": 4, "distill": 0, "distilled": 0, "nitro": 4, "sdxs": 1,
}

// A step count written into a name, 4step, 8-steps, or 2_step
var sdStepWord = regexp.MustCompile(`(?i)(?:^|[^0-9])([0-9]{1,3})[-_ ]?steps?(?:$|[^a-z])`)

// The words of a name, split on anything that is not a letter or a digit and where letters meet digits,
// so flux2-klein-4B and FLUX.2-klein both read flux, 2, klein
func sdWords(name string) []string {
	var out []string
	digit := func(r rune) bool { return r >= '0' && r <= '9' }
	for _, field := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || digit(r))
	}) {
		start := 0
		runes := []rune(field)
		for i := 1; i <= len(runes); i++ {
			if i == len(runes) || digit(runes[i]) != digit(runes[i-1]) {
				out = append(out, string(runes[start:i]))
				start = i
			}
		}
	}
	return out
}

// Whether a name says the model is distilled, and the step count the name gives when it gives one
func sdDistilled(name string) (bool, int) {
	steps := 0
	if m := sdStepWord.FindStringSubmatch(name); m != nil {
		steps, _ = strconv.Atoi(m[1])
	}
	for _, w := range sdWords(name) {
		if n, ok := sdDistilledWords[w]; ok {
			if steps == 0 {
				steps = n
			}
			return true, steps
		}
	}
	return steps > 0, steps
}

// The family a model belongs to: what its tensors said, else what its name says, else what its header
// claimed, since a converter writes whatever family it was told
func sdFamily(d *v1.Descriptor, repo string) string {
	if f := diffusion.Canonical(d.GetMetadata()[diffusion.KeyFamily]); f != "" && diffusion.Denoiser(f) {
		return f
	}
	if f := sdFamilyNamed(repo + "/" + d.GetGroup()); f != "" {
		return f
	}
	return diffusion.Canonical(d.GetArchitecture())
}

// The family a name spells out, the spelling of the most words winning so flux2 klein beats flux2 and
// that beats flux; empty when the name spells none
func sdFamilyNamed(name string) string {
	words := sdWords(name)
	best, bestParts := "", 0
	for _, spelling := range diffusion.Spellings() {
		parts := sdWords(spelling)
		if len(parts) == 0 || len(parts) < bestParts {
			continue
		}
		for i := 0; i+len(parts) <= len(words); i++ {
			if !slices.Equal(words[i:i+len(parts)], parts) {
				continue
			}
			if len(parts) > bestParts || (len(parts) == bestParts && diffusion.Canonical(spelling) < diffusion.Canonical(best)) {
				best, bestParts = spelling, len(parts)
			}
			break
		}
	}
	if best == "" {
		return ""
	}
	return diffusion.Canonical(best)
}

// Gives every sampling param still at auto the family's figure, a distilled release running at guidance 1 for fewer steps
func sdSampling(s *estimate.Scope) {
	d := s.Descriptor
	family := sdFamily(d, s.Repo)
	sample, ok := sdSamples[family]
	if !ok {
		sample = sdSampleFallback
	}
	if distilled, steps := sdDistilled(s.Repo + "/" + d.GetGroup()); distilled {
		sample.Cfg = 1
		if steps > 0 {
			sample.Steps = steps
		} else {
			sample.Steps = sample.Distilled
		}
		// A FLUX release without the guidance embedding shifts by 1, the way schnell does
		if family == "flux" {
			sample.FlowShift = 1
		}
	}
	if s.Params.IsAuto("steps") {
		s.Params["steps"] = int64(sample.Steps)
	}
	if s.Params.IsAuto("cfg_scale") {
		s.Params["cfg_scale"] = sample.Cfg
	}
	if s.Params.IsAuto("guidance") {
		s.Params["guidance"] = sample.Guidance
	}
	if s.Params.IsAuto("flow_shift") {
		s.Params["flow_shift"] = sample.FlowShift
	}
	if s.Params.IsAuto("sampling_method") {
		s.Params["sampling_method"] = sample.Sampler
	}
	if s.Params.IsAuto("scheduler") {
		s.Params["scheduler"] = sample.Scheduler
	}
}
