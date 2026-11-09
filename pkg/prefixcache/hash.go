package prefixcache

import (
	"encoding/binary"
	"unicode/utf8"

	"github.com/cespare/xxhash/v2"
)

// Domain tags keeping text chunks and token chunks from ever sharing a hash
const (
	domainText   byte = 't'
	domainTokens byte = 'k'
)

// Chains text chunks of ChunkChars characters or token chunks of ChunkTokens ids, each covering every earlier chunk
func (x *Index) Hash(model, adapter, prompt string, tokenIDs []int) []BlockHash {
	if len(tokenIDs) > 0 {
		return hashTokens(scope(domainTokens, model, adapter), tokenIDs, x.cfg.ChunkTokens, x.cfg.MaxChunks)
	}
	return hashText(scope(domainText, model, adapter), prompt, x.cfg.ChunkChars, x.cfg.MaxChunks)
}

// Digest primed with the domain, model and adapter, each length prefixed so no two scopes collide
func scope(domain byte, model, adapter string) xxhash.Digest {
	d := xxhash.New()
	var buf [1 + 2*binary.MaxVarintLen64]byte
	buf[0] = domain
	n := 1 + binary.PutUvarint(buf[1:], uint64(len(model)))
	_, _ = d.Write(buf[:n])
	_, _ = d.WriteString(model)
	n = binary.PutUvarint(buf[:], uint64(len(adapter)))
	_, _ = d.Write(buf[:n])
	_, _ = d.WriteString(adapter)
	return *d
}

// Sums the scope, the chunk bytes and the previous hash
func chain(base *xxhash.Digest, chunk []byte, prev uint64) uint64 {
	d := *base
	_, _ = d.Write(chunk)
	return finish(&d, prev)
}

// Sums the scope, the chunk text and the previous hash without copying the text
func chainString(base *xxhash.Digest, chunk string, prev uint64) uint64 {
	d := *base
	_, _ = d.WriteString(chunk)
	return finish(&d, prev)
}

// Appends the previous hash and returns the sum
func finish(d *xxhash.Digest, prev uint64) uint64 {
	var p [8]byte
	binary.LittleEndian.PutUint64(p[:], prev)
	_, _ = d.Write(p[:])
	return d.Sum64()
}

// Chain over chunks of size characters, the trailing partial chunk included
func hashText(base xxhash.Digest, prompt string, size, limit int) []BlockHash {
	if prompt == "" {
		return nil
	}
	out := make([]BlockHash, 0, min(limit, (len(prompt)+size-1)/size))
	var prev uint64
	for start := 0; start < len(prompt) && len(out) < limit; {
		end := advance(prompt, start, size)
		prev = chainString(&base, prompt[start:end], prev)
		out = append(out, BlockHash(prev))
		start = end
	}
	return out
}

// Byte offset after n characters from start
func advance(s string, start, n int) int {
	i := start
	for c := 0; c < n && i < len(s); c++ {
		if s[i] < utf8.RuneSelf {
			i++
			continue
		}
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return i
}

// Chain over chunks of size token ids, each id written as eight little endian bytes
func hashTokens(base xxhash.Digest, ids []int, size, limit int) []BlockHash {
	out := make([]BlockHash, 0, min(limit, (len(ids)+size-1)/size))
	buf := make([]byte, 0, size*8)
	var prev uint64
	for start := 0; start < len(ids) && len(out) < limit; start += size {
		buf = buf[:0]
		for _, id := range ids[start:min(start+size, len(ids))] {
			buf = binary.LittleEndian.AppendUint64(buf, uint64(id))
		}
		prev = chain(&base, buf, prev)
		out = append(out, BlockHash(prev))
	}
	return out
}
