package bodyparse

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func TestSplitChunks(t *testing.T) {
	cases := []struct {
		name  string
		size  int
		limit int
		want  []int
	}{
		{"empty", 0, 10, []int{0}},
		{"under", 5, 10, []int{5}},
		{"exact", 10, 10, []int{10}},
		{"over", 25, 10, []int{10, 10, 5}},
		{"default limit", ChunkBytes*2 + 1, 0, []int{ChunkBytes, ChunkBytes, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := bytes.Repeat([]byte{'x'}, c.size)
			got := SplitChunks(body, c.limit)
			if len(got) != len(c.want) {
				t.Fatalf("got %d chunks, want %d", len(got), len(c.want))
			}
			for i, ch := range got {
				if len(ch) != c.want[i] {
					t.Fatalf("chunk %d has %d bytes, want %d", i, len(ch), c.want[i])
				}
			}
			if !bytes.Equal(bytes.Join(got, nil), body) {
				t.Fatal("chunks do not rejoin to the body")
			}
			if len(got) > 1 && cap(got[0]) != len(got[0]) {
				t.Fatal("an append to a chunk could overwrite the next one")
			}
		})
	}
}

func TestReader(t *testing.T) {
	cases := []struct {
		name  string
		size  int
		limit int64
		half  bool
		want  []int
		err   error
	}{
		{"empty", 0, 0, false, []int{0}, nil},
		{"short", 100, 0, false, []int{100}, nil},
		{"exact", ChunkBytes, 0, false, []int{ChunkBytes}, nil},
		{"two exact", 2 * ChunkBytes, 0, false, []int{ChunkBytes, ChunkBytes}, nil},
		{"partial reads", 2*ChunkBytes + 7, 0, true, []int{ChunkBytes, ChunkBytes, 7}, nil},
		{"at limit", 100, 100, false, []int{100}, nil},
		{"over limit", 101, 100, false, nil, ErrTooLarge},
		{"over limit later", 3 * ChunkBytes, 2*ChunkBytes + 1, false, []int{ChunkBytes, ChunkBytes}, ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := bytes.Repeat([]byte{'y'}, c.size)
			var src io.Reader = bytes.NewReader(body)
			if c.half {
				src = iotest.HalfReader(src)
			}
			r := NewReader(src, c.limit)
			var sizes []int
			var joined []byte
			for {
				chunk, last, err := r.Next()
				if err != nil {
					if !errors.Is(err, c.err) {
						t.Fatalf("err %v, want %v", err, c.err)
					}
					break
				}
				sizes = append(sizes, len(chunk))
				joined = append(joined, chunk...)
				if last {
					if c.err != nil {
						t.Fatalf("finished without %v", c.err)
					}
					if _, _, err := r.Next(); err != io.EOF {
						t.Fatalf("after last err %v, want EOF", err)
					}
					if !bytes.Equal(joined, body) {
						t.Fatal("chunks do not rejoin to the body")
					}
					break
				}
			}
			if len(sizes) != len(c.want) {
				t.Fatalf("sizes %v, want %v", sizes, c.want)
			}
			for i := range sizes {
				if sizes[i] != c.want[i] {
					t.Fatalf("sizes %v, want %v", sizes, c.want)
				}
			}
		})
	}
}

func TestReaderSourceError(t *testing.T) {
	boom := errors.New("boom")
	r := NewReader(iotest.ErrReader(boom), 0)
	if _, _, err := r.Next(); !errors.Is(err, boom) {
		t.Fatalf("err %v, want boom", err)
	}
	if _, _, err := r.Next(); err != io.EOF {
		t.Fatalf("after error %v, want EOF", err)
	}
}
