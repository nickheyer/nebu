package launch

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	// CSI sequences such as colors and cursor moves, and OSC sequences such as window titles
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)
)

// Makes one line of process output safe to store and send
//
// Runtimes print progress with carriage returns, colour with escape codes, and
// sometimes bytes that are not text at all. The API carries lines as proto
// strings, which must be valid UTF-8, so every line passes through here once.
func Clean(line string) string {
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	if strings.IndexByte(line, 0x1b) >= 0 {
		line = ansiRe.ReplaceAllString(line, "")
	}
	if !utf8.ValidString(line) {
		line = strings.ToValidUTF8(line, "�")
	}
	if strings.IndexFunc(line, isControl) < 0 {
		return line
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, line)
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\t') || r == 0x7f
}
