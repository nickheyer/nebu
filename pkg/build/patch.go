package build

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Returned when a hunk matches neither old nor new text
var ErrHunk = errors.New("hunk does not apply")

type hunkLine struct {
	kind byte
	text string
}

type hunk struct {
	oldStart, oldCount int
	newStart, newCount int
	lines              []hunkLine
}

type filePatch struct {
	oldPath  string
	newPath  string
	isNew    bool
	isDelete bool
	hunks    []hunk
}

// Parses a unified diff into per file patches
func parseUnified(data []byte) ([]filePatch, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	var out []filePatch
	var cur *filePatch
	var h *hunk
	var remainOld, remainNew int
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if h != nil && (remainOld > 0 || remainNew > 0) {
			if strings.HasPrefix(line, `\ No newline`) {
				if n := len(h.lines); n > 0 {
					h.lines[n-1].text = strings.TrimSuffix(h.lines[n-1].text, "\n")
				}
				continue
			}
			if line == "" {
				line = " "
			}
			kind, text := line[0], line[1:]+"\n"
			switch kind {
			case ' ':
				remainOld--
				remainNew--
			case '-':
				remainOld--
			case '+':
				remainNew--
			default:
				return nil, fmt.Errorf("line %d: unexpected %q inside hunk", lineNo, line)
			}
			h.lines = append(h.lines, hunkLine{kind: kind, text: text})
			continue
		}
		if strings.HasPrefix(line, `\ No newline`) {
			if h != nil {
				if n := len(h.lines); n > 0 {
					h.lines[n-1].text = strings.TrimSuffix(h.lines[n-1].text, "\n")
				}
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "--- "):
			out = append(out, filePatch{oldPath: diffPath(line[4:])})
			cur = &out[len(out)-1]
			h = nil
		case strings.HasPrefix(line, "+++ "):
			if cur == nil {
				return nil, fmt.Errorf("line %d: +++ without ---", lineNo)
			}
			cur.newPath = diffPath(line[4:])
			cur.isNew = cur.oldPath == "/dev/null"
			cur.isDelete = cur.newPath == "/dev/null"
		case strings.HasPrefix(line, "@@ "):
			if cur == nil || cur.newPath == "" {
				return nil, fmt.Errorf("line %d: hunk before file header", lineNo)
			}
			parsed, err := parseHunkHeader(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			cur.hunks = append(cur.hunks, parsed)
			h = &cur.hunks[len(cur.hunks)-1]
			remainOld, remainNew = h.oldCount, h.newCount
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no file headers in patch")
	}
	return out, nil
}

func diffPath(s string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
		if unquoted, err := strconv.Unquote(s); err == nil {
			s = unquoted
		}
	}
	return s
}

func parseHunkHeader(line string) (hunk, error) {
	var h hunk
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return h, fmt.Errorf("bad hunk header %q", line)
	}
	var err error
	if h.oldStart, h.oldCount, err = parseRange(fields[1][1:]); err != nil {
		return h, err
	}
	if h.newStart, h.newCount, err = parseRange(fields[2][1:]); err != nil {
		return h, err
	}
	return h, nil
}

func parseRange(s string) (int, int, error) {
	start, count := s, "1"
	if i := strings.IndexByte(s, ','); i >= 0 {
		start, count = s[:i], s[i+1:]
	}
	a, err := strconv.Atoi(start)
	if err != nil {
		return 0, 0, err
	}
	b, err := strconv.Atoi(count)
	if err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

// Strips leading path components the way patch -p does
func stripPath(p string, strip int) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	if strip >= len(parts) {
		return parts[len(parts)-1]
	}
	return strings.Join(parts[strip:], "/")
}

// Applies parsed patches under root, returning the files touched
func applyPatches(root string, patches []filePatch, strip int) ([]string, error) {
	var touched []string
	for _, fp := range patches {
		name := fp.newPath
		if fp.isDelete {
			name = fp.oldPath
		}
		rel := stripPath(name, strip)
		target := filepath.Join(root, filepath.FromSlash(rel))
		if r, err := filepath.Rel(root, target); err != nil || strings.HasPrefix(r, "..") {
			return nil, fmt.Errorf("patch path %q escapes the tree", name)
		}
		if err := applyFile(target, fp); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		touched = append(touched, rel)
	}
	return touched, nil
}

// Applies one file patch, tolerating a patch already applied
func applyFile(path string, fp filePatch) error {
	var lines []string
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		lines = splitLines(data)
	case errors.Is(err, os.ErrNotExist) && fp.isNew:
	default:
		return err
	}
	if fp.isDelete {
		if err != nil {
			return nil
		}
		return os.Remove(path)
	}
	if fp.isNew && err == nil {
		var want []string
		for _, h := range fp.hunks {
			for _, l := range h.lines {
				if l.kind != '-' {
					want = append(want, l.text)
				}
			}
		}
		// Upstream merged the file, so there is nothing to add
		if strings.Join(lines, "") == strings.Join(want, "") {
			return nil
		}
	}
	delta, drift := 0, 0
	for i, h := range fp.hunks {
		var oldLines, newLines []string
		for _, l := range h.lines {
			if l.kind != '+' {
				oldLines = append(oldLines, l.text)
			}
			if l.kind != '-' {
				newLines = append(newLines, l.text)
			}
		}
		expected := max(h.oldStart-1, 0) + delta + drift
		if pos, ok := locate(lines, oldLines, expected); ok {
			lines = append(append(append([]string(nil), lines[:pos]...), newLines...), lines[pos+len(oldLines):]...)
			drift = pos - expected
			delta += len(newLines) - len(oldLines)
			continue
		}
		if _, ok := locate(lines, newLines, expected); ok && len(newLines) > 0 {
			delta += len(newLines) - len(oldLines)
			continue
		}
		return fmt.Errorf("%w: hunk %d", ErrHunk, i+1)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644)
}

// Finds where pattern sits in lines, searching outward from expected
func locate(lines, pattern []string, expected int) (int, bool) {
	if len(pattern) == 0 {
		return min(max(expected, 0), len(lines)), true
	}
	limit := len(lines) - len(pattern)
	if limit < 0 {
		return 0, false
	}
	matches := func(pos int) bool {
		for i, want := range pattern {
			if lines[pos+i] != want {
				return false
			}
		}
		return true
	}
	for d := 0; d <= limit; d++ {
		for _, pos := range []int{expected - d, expected + d} {
			if pos >= 0 && pos <= limit && matches(pos) {
				return pos, true
			}
		}
		if expected-d < 0 && expected+d > limit {
			break
		}
	}
	return 0, false
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var out []string
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			out = append(out, string(data))
			break
		}
		out = append(out, string(data[:i+1]))
		data = data[i+1:]
	}
	return out
}
