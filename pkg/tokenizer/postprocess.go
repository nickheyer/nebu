package tokenizer

// Wraps a single sequence in the ids its template adds
type postProcessor interface {
	process(ids []uint32) []uint32
}

type postSequence []postProcessor

func (s postSequence) process(ids []uint32) []uint32 {
	for _, p := range s {
		ids = p.process(ids)
	}
	return ids
}

// A template piece, either the sequence or special token ids
type templatePiece struct {
	sequence bool
	ids      []uint32
}

type templateProcessor struct {
	single []templatePiece
}

func (t templateProcessor) process(ids []uint32) []uint32 {
	out := make([]uint32, 0, len(ids)+4)
	for _, p := range t.single {
		if p.sequence {
			out = append(out, ids...)
			continue
		}
		out = append(out, p.ids...)
	}
	return out
}

// The ids the single template of a BERT or RoBERTa processor wraps a sequence in
func bertTemplate(cls, sep uint32) templateProcessor {
	return templateProcessor{single: []templatePiece{{ids: []uint32{cls}}, {sequence: true}, {ids: []uint32{sep}}}}
}

type noProcessor struct{}

func (noProcessor) process(ids []uint32) []uint32 { return ids }
