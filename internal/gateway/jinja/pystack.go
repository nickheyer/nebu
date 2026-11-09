package jinja

// The Python frames a jinja2 compile holds, failing like CPython once a push would pass the recursion limit
type pyStack struct {
	depth int
	// The deepest frame reached so far and the most frames allowed
	peak, limit int
}

// Frames generate.py's compile call holds while Environment._parse and _generate run, module, main, two table helpers, compile_template, transformers' two compile helpers, from_string, compile and the phase itself
const compileBaseFrames = 10

// A stack holding the frames below jinja2's parse and generate phases
func newCompileStack() *pyStack { return &pyStack{depth: compileBaseFrames, limit: recursionLimit} }

// CPython's RecursionError
func recursionError() error { return errorf("RecursionError", "maximum recursion depth exceeded") }

// Fails unless a frame at the given depth fits under the recursion limit
func (s *pyStack) check(at int) error {
	if at > s.limit {
		return recursionError()
	}
	s.peak = max(s.peak, at)
	return nil
}

// Fails unless k frames above the running one fit, for calls that return before the caller goes on
func (s *pyStack) need(k int) error { return s.check(s.depth + k) }

// Pushes the frame of a called Python function
func (s *pyStack) enter() error {
	if err := s.check(s.depth + 1); err != nil {
		return err
	}
	s.depth++
	return nil
}

func (s *pyStack) leave() { s.depth-- }
