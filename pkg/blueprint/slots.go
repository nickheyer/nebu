// Package blueprint defines model families, required components, and their source repositories.
package blueprint

// Blueprint slot IDs.
const (
	SlotDenoiser          = "denoiser"
	SlotDenoiserHighNoise = "denoiser.high_noise"
	SlotDenoiserUncond    = "denoiser.uncond"
	SlotRefiner           = "refiner"
	SlotStagePrior        = "stage.prior"
	SlotStageDecoder      = "stage.decoder"
	SlotStageUpscaler     = "stage.upscaler"
	SlotVAE               = "vae"
	SlotVAEAudio          = "vae.audio"
	SlotVAETiny           = "vae.tiny"
	SlotTextEncoderClipL  = "text_encoder.clip_l"
	SlotTextEncoderClipG  = "text_encoder.clip_g"
	SlotTextEncoderClipH  = "text_encoder.clip_h"
	SlotTextEncoderT5     = "text_encoder.t5"
	SlotTextEncoderLLM    = "text_encoder.llm"
	SlotTextEncoderVision = "text_encoder.llm.vision"
	SlotTextEncoderGlyph  = "text_encoder.glyph"
	SlotImageClipVision   = "image_encoder.clip_vision"
	SlotImageDino         = "image_encoder.dino"
	SlotAudioEncoder      = "audio_encoder"
	SlotFaceEncoder       = "face_encoder"
	SlotTokenizer         = "tokenizer"
	SlotConnector         = "connector"
	SlotWeights           = "weights"
	SlotConfig            = "config"
	SlotProjector         = "projector"
	SlotDraft             = "draft"
	SlotCodec             = "codec"
	SlotSpeaker           = "speaker"
	SlotPreprocessor      = "preprocessor"
	SlotDetector          = "detector"
	SlotUpscaler          = "upscaler"
	SlotSafety            = "safety"
)

// Slot labels in blueprint order.
var slotLabels = []struct{ id, label string }{
	{SlotDenoiser, "denoiser"},
	{SlotDenoiserHighNoise, "high noise denoiser"},
	{SlotDenoiserUncond, "unconditional denoiser"},
	{SlotRefiner, "refiner"},
	{SlotStagePrior, "prior stage"},
	{SlotStageDecoder, "decoder stage"},
	{SlotStageUpscaler, "upscaler stage"},
	{SlotVAE, "VAE"},
	{SlotVAEAudio, "audio VAE"},
	{SlotVAETiny, "tiny VAE"},
	{SlotTextEncoderClipL, "CLIP-L text encoder"},
	{SlotTextEncoderClipG, "CLIP-G text encoder"},
	{SlotTextEncoderClipH, "CLIP-H text encoder"},
	{SlotTextEncoderT5, "T5 text encoder"},
	{SlotTextEncoderLLM, "language model text encoder"},
	{SlotTextEncoderVision, "text encoder vision tower"},
	{SlotTextEncoderGlyph, "glyph text encoder"},
	{SlotImageClipVision, "CLIP vision encoder"},
	{SlotImageDino, "DINO vision encoder"},
	{SlotAudioEncoder, "audio encoder"},
	{SlotFaceEncoder, "face encoder"},
	{SlotTokenizer, "tokenizer"},
	{SlotConnector, "embeddings connectors"},
	{SlotWeights, "weights"},
	{SlotConfig, "config"},
	{SlotProjector, "projector"},
	{SlotDraft, "draft model"},
	{SlotCodec, "codec"},
	{SlotSpeaker, "speaker"},
	{SlotPreprocessor, "preprocessor"},
	{SlotDetector, "detector"},
	{SlotUpscaler, "upscaler"},
	{SlotSafety, "safety checker"},
}

// Slots returns slot IDs in blueprint order.
func Slots() []string {
	out := make([]string, 0, len(slotLabels))
	for _, s := range slotLabels {
		out = append(out, s.id)
	}
	return out
}

// Label returns the slot label, or the ID if unknown.
func Label(slot string) string {
	for _, s := range slotLabels {
		if s.id == slot {
			return s.label
		}
	}
	return slot
}

// IsSlot reports whether the slot ID is known.
func IsSlot(id string) bool {
	for _, s := range slotLabels {
		if s.id == id {
			return true
		}
	}
	return false
}
