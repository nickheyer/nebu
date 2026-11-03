package kvevents

import (
	"errors"
	"fmt"
)

var (
	ErrEventType   = errors.New("event carries a type the subscriber does not read")
	ErrEventTokens = errors.New("stored event token count does not cover its blocks")
	ErrEventLoRA   = errors.New("stored event names its adapter only by the deprecated numeric id")
)

// One batch a runtime published, in publication order
type Batch struct {
	Seq    uint64
	Stored []Stored
	// Hashes removed, in order, each emptied when every block was cleared before it
	Removed []Removed
	// Whether the batch cleared every block
	Cleared bool
	// Events in the order published, each either a Stored index, a Removed index or the clear
	Order []step
}

// One event position in a batch
type step struct {
	kind  byte
	index int
}

const (
	stepStored byte = iota
	stepRemoved
	stepCleared
)

// Blocks the runtime stored as a chain from a parent block
type Stored struct {
	Hashes    []string
	Parent    string
	HasParent bool
	TokenIDs  []int
	BlockSize int
	Adapter   string
}

// Blocks the runtime evicted
type Removed struct {
	Hashes []string
}

// Decodes a batch, an array of timestamp, events and rank, each event a map tagged by its type key
func decodeBatch(seq uint64, payload []byte) (*Batch, error) {
	r := &reader{buf: payload}
	n, err := r.arrayLen()
	if err != nil {
		return nil, err
	}
	if n < 2 {
		return nil, fmt.Errorf("%w: batch of %d fields", ErrType, n)
	}
	if _, err := r.float(); err != nil {
		return nil, fmt.Errorf("timestamp: %w", err)
	}
	count, err := r.arrayLen()
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	b := &Batch{Seq: seq}
	for i := 0; i < count; i++ {
		if err := decodeEvent(r, b); err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
	}
	return b, nil
}

// Decodes one tagged event map into the batch
func decodeEvent(r *reader, b *Batch) error {
	pairs, err := r.mapLen()
	if err != nil {
		return err
	}
	var kind string
	var st Stored
	var rm Removed
	var loraID bool
	for i := 0; i < pairs; i++ {
		key, err := r.str()
		if err != nil {
			return err
		}
		switch key {
		case "type":
			if kind, err = r.str(); err != nil {
				return err
			}
		case "block_hashes":
			if st.Hashes, err = hashes(r); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			rm.Hashes = st.Hashes
		case "parent_block_hash":
			isNil, err := r.nilValue()
			if err != nil {
				return err
			}
			if !isNil {
				if st.Parent, err = r.hash(); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				st.HasParent = true
			}
		case "token_ids":
			if st.TokenIDs, err = ints(r); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		case "block_size":
			n, err := r.int()
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			st.BlockSize = int(n)
		case "lora_name":
			isNil, err := r.nilValue()
			if err != nil {
				return err
			}
			if !isNil {
				if st.Adapter, err = r.str(); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
			}
		case "lora_id":
			isNil, err := r.nilValue()
			if err != nil {
				return err
			}
			if !isNil {
				if _, err := r.int(); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				loraID = true
			}
		default:
			if err := r.skip(); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	switch kind {
	case "BlockStored":
		if st.BlockSize <= 0 || len(st.TokenIDs) > len(st.Hashes)*st.BlockSize || len(st.TokenIDs) <= (len(st.Hashes)-1)*st.BlockSize {
			return fmt.Errorf("%w: %d tokens for %d blocks of %d", ErrEventTokens, len(st.TokenIDs), len(st.Hashes), st.BlockSize)
		}
		if loraID && st.Adapter == "" {
			return ErrEventLoRA
		}
		b.Order = append(b.Order, step{stepStored, len(b.Stored)})
		b.Stored = append(b.Stored, st)
	case "BlockRemoved":
		b.Order = append(b.Order, step{stepRemoved, len(b.Removed)})
		b.Removed = append(b.Removed, rm)
	case "AllBlocksCleared":
		b.Cleared = true
		b.Order = append(b.Order, step{stepCleared, 0})
	default:
		return fmt.Errorf("%w: %q", ErrEventType, kind)
	}
	return nil
}

// An array of block hashes
func hashes(r *reader) ([]string, error) {
	n, err := r.arrayLen()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		h, err := r.hash()
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// An array of token ids
func ints(r *reader) ([]int, error) {
	n, err := r.arrayLen()
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		v, err := r.int()
		if err != nil {
			return nil, err
		}
		out = append(out, int(v))
	}
	return out, nil
}
