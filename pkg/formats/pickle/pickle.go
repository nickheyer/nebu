// Package pickle decodes the subset of Python's pickle protocol that PyTorch checkpoints use.
//
// Nothing is executed. Callables named by GLOBAL are kept as values, and
// applying one through REDUCE or NEWOBJ yields an Object recording the
// callable and its arguments, which is enough to read tensor names, shapes,
// and storage types out of a checkpoint without PyTorch.
package pickle

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

const (
	maxMemo   = 1 << 24
	maxStack  = 1 << 22
	maxString = 256 << 20
)

// Callable named by module and attribute
type Global struct {
	Module string
	Name   string
}

func (g Global) String() string { return g.Module + "." + g.Name }

// Result of applying a callable, with the state BUILD attached
type Object struct {
	Class Global
	Args  []any
	KW    map[string]any
	State any
}

// Reference the unpickler was asked to resolve out of band
type Persistent struct {
	ID any
}

// Python tuple
type Tuple []any

// Python list, a pointer so appends after memoization are seen through references
type List struct {
	Items []any
}

// One dictionary entry, keys kept as decoded since they need not be strings
type Pair struct {
	Key   any
	Value any
}

// Python dict as an ordered pair list
type Dict struct {
	Pairs []Pair
}

// Returns the value stored under a string key
func (d *Dict) Get(key string) (any, bool) {
	if d == nil {
		return nil, false
	}
	for _, p := range d.Pairs {
		if s, ok := p.Key.(string); ok && s == key {
			return p.Value, true
		}
	}
	return nil, false
}

func (d *Dict) set(key, value any) {
	for i, p := range d.Pairs {
		if keyEqual(p.Key, key) {
			d.Pairs[i].Value = value
			return
		}
	}
	d.Pairs = append(d.Pairs, Pair{Key: key, Value: value})
}

func keyEqual(a, b any) bool {
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case int64:
		y, ok := b.(int64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	}
	return false
}

type mark struct{}

// Decodes one pickle stream
type Decoder struct {
	r     *bufio.Reader
	stack []any
	memo  map[int]any
	// Reduce replaces the default object construction for a callable; return false to fall back
	Reduce func(g Global, args []any) (any, bool)
}

// Wraps a reader
func New(r io.Reader) *Decoder {
	return &Decoder{r: bufio.NewReaderSize(r, 1<<16), memo: map[int]any{}}
}

// Decodes the first object in a stream
func Decode(r io.Reader) (any, error) {
	return New(r).Load()
}

// Decodes the next object
func (d *Decoder) Load() (any, error) {
	for {
		op, err := d.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		done, err := d.step(op)
		if err != nil {
			return nil, err
		}
		if done {
			return d.pop()
		}
	}
}

func (d *Decoder) push(v any) error {
	if len(d.stack) >= maxStack {
		return errors.New("pickle: stack too deep")
	}
	d.stack = append(d.stack, v)
	return nil
}

func (d *Decoder) pop() (any, error) {
	if len(d.stack) == 0 {
		return nil, errors.New("pickle: stack underflow")
	}
	v := d.stack[len(d.stack)-1]
	d.stack = d.stack[:len(d.stack)-1]
	return v, nil
}

func (d *Decoder) top() (any, error) {
	if len(d.stack) == 0 {
		return nil, errors.New("pickle: stack underflow")
	}
	return d.stack[len(d.stack)-1], nil
}

// Pops everything above the last mark
func (d *Decoder) popMark() ([]any, error) {
	for i := len(d.stack) - 1; i >= 0; i-- {
		if _, ok := d.stack[i].(mark); ok {
			items := append([]any(nil), d.stack[i+1:]...)
			d.stack = d.stack[:i]
			return items, nil
		}
	}
	return nil, errors.New("pickle: no mark")
}

func (d *Decoder) memoize(key int, v any) error {
	if len(d.memo) >= maxMemo {
		return errors.New("pickle: memo too large")
	}
	d.memo[key] = v
	return nil
}

func (d *Decoder) step(op byte) (bool, error) {
	switch op {
	case '\x80': // PROTO
		if _, err := d.r.ReadByte(); err != nil {
			return false, err
		}
	case '\x95': // FRAME
		if _, err := d.u64(); err != nil {
			return false, err
		}
	case '.': // STOP
		return true, nil
	case '(': // MARK
		return false, d.push(mark{})
	case '0': // POP
		_, err := d.pop()
		return false, err
	case '1': // POP_MARK
		_, err := d.popMark()
		return false, err
	case '2': // DUP
		v, err := d.top()
		if err != nil {
			return false, err
		}
		return false, d.push(v)
	case 'N':
		return false, d.push(nil)
	case '\x88':
		return false, d.push(true)
	case '\x89':
		return false, d.push(false)
	case 'I', 'L': // INT, LONG as text
		line, err := d.line()
		if err != nil {
			return false, err
		}
		line = strings.TrimSuffix(line, "L")
		switch line {
		case "00":
			return false, d.push(false)
		case "01":
			return false, d.push(true)
		}
		n, ok := new(big.Int).SetString(line, 10)
		if !ok {
			return false, fmt.Errorf("pickle: bad integer %q", line)
		}
		return false, d.push(bigValue(n))
	case 'J': // BININT
		v, err := d.u32()
		if err != nil {
			return false, err
		}
		return false, d.push(int64(int32(v)))
	case 'K': // BININT1
		b, err := d.r.ReadByte()
		if err != nil {
			return false, err
		}
		return false, d.push(int64(b))
	case 'M': // BININT2
		v, err := d.u16()
		if err != nil {
			return false, err
		}
		return false, d.push(int64(v))
	case '\x8a': // LONG1
		n, err := d.r.ReadByte()
		if err != nil {
			return false, err
		}
		return false, d.pushLong(int(n))
	case '\x8b': // LONG4
		n, err := d.u32()
		if err != nil {
			return false, err
		}
		if n > maxString {
			return false, errors.New("pickle: long too large")
		}
		return false, d.pushLong(int(n))
	case 'F': // FLOAT text
		line, err := d.line()
		if err != nil {
			return false, err
		}
		f, err := strconv.ParseFloat(line, 64)
		if err != nil {
			return false, err
		}
		return false, d.push(f)
	case 'G': // BINFLOAT
		var buf [8]byte
		if _, err := io.ReadFull(d.r, buf[:]); err != nil {
			return false, err
		}
		return false, d.push(math.Float64frombits(binary.BigEndian.Uint64(buf[:])))
	case 'S', 'V': // STRING, UNICODE text
		line, err := d.line()
		if err != nil {
			return false, err
		}
		if op == 'S' {
			if s, err := strconv.Unquote(line); err == nil {
				line = s
			}
		}
		return false, d.push(line)
	case 'T', 'X': // BINSTRING, BINUNICODE
		n, err := d.u32()
		if err != nil {
			return false, err
		}
		s, err := d.bytes(int(n))
		if err != nil {
			return false, err
		}
		return false, d.push(string(s))
	case 'U', '\x8c': // SHORT_BINSTRING, SHORT_BINUNICODE
		n, err := d.r.ReadByte()
		if err != nil {
			return false, err
		}
		s, err := d.bytes(int(n))
		if err != nil {
			return false, err
		}
		return false, d.push(string(s))
	case '\x8d': // BINUNICODE8
		n, err := d.u64()
		if err != nil {
			return false, err
		}
		s, err := d.bytes(int(n))
		if err != nil {
			return false, err
		}
		return false, d.push(string(s))
	case 'B': // BINBYTES
		n, err := d.u32()
		if err != nil {
			return false, err
		}
		s, err := d.bytes(int(n))
		if err != nil {
			return false, err
		}
		return false, d.push(s)
	case 'C': // SHORT_BINBYTES
		n, err := d.r.ReadByte()
		if err != nil {
			return false, err
		}
		s, err := d.bytes(int(n))
		if err != nil {
			return false, err
		}
		return false, d.push(s)
	case '\x8e', '\x96': // BINBYTES8, BYTEARRAY8
		n, err := d.u64()
		if err != nil {
			return false, err
		}
		s, err := d.bytes(int(n))
		if err != nil {
			return false, err
		}
		return false, d.push(s)
	case '}': // EMPTY_DICT
		return false, d.push(&Dict{})
	case 'd': // DICT
		items, err := d.popMark()
		if err != nil {
			return false, err
		}
		dict := &Dict{}
		for i := 0; i+1 < len(items); i += 2 {
			dict.set(items[i], items[i+1])
		}
		return false, d.push(dict)
	case 's': // SETITEM
		v, err := d.pop()
		if err != nil {
			return false, err
		}
		k, err := d.pop()
		if err != nil {
			return false, err
		}
		return false, d.setItems([]any{k, v})
	case 'u': // SETITEMS
		items, err := d.popMark()
		if err != nil {
			return false, err
		}
		return false, d.setItems(items)
	case ']': // EMPTY_LIST
		return false, d.push(&List{})
	case 'l': // LIST
		items, err := d.popMark()
		if err != nil {
			return false, err
		}
		return false, d.push(&List{Items: items})
	case 'a': // APPEND
		v, err := d.pop()
		if err != nil {
			return false, err
		}
		return false, d.appendItems([]any{v})
	case 'e': // APPENDS
		items, err := d.popMark()
		if err != nil {
			return false, err
		}
		return false, d.appendItems(items)
	case ')': // EMPTY_TUPLE
		return false, d.push(Tuple{})
	case 't': // TUPLE
		items, err := d.popMark()
		if err != nil {
			return false, err
		}
		return false, d.push(Tuple(items))
	case '\x85', '\x86', '\x87': // TUPLE1..3
		n := int(op - '\x84')
		if len(d.stack) < n {
			return false, errors.New("pickle: stack underflow")
		}
		items := Tuple(append([]any(nil), d.stack[len(d.stack)-n:]...))
		d.stack = d.stack[:len(d.stack)-n]
		return false, d.push(items)
	case '\x8f': // EMPTY_SET
		return false, d.push(&List{})
	case '\x90': // ADDITEMS
		items, err := d.popMark()
		if err != nil {
			return false, err
		}
		return false, d.appendItems(items)
	case '\x91': // FROZENSET
		items, err := d.popMark()
		if err != nil {
			return false, err
		}
		return false, d.push(Tuple(items))
	case 'c': // GLOBAL
		module, err := d.line()
		if err != nil {
			return false, err
		}
		name, err := d.line()
		if err != nil {
			return false, err
		}
		return false, d.push(Global{Module: module, Name: name})
	case '\x93': // STACK_GLOBAL
		name, err := d.pop()
		if err != nil {
			return false, err
		}
		module, err := d.pop()
		if err != nil {
			return false, err
		}
		ms, ok1 := module.(string)
		ns, ok2 := name.(string)
		if !ok1 || !ok2 {
			return false, errors.New("pickle: STACK_GLOBAL needs two strings")
		}
		return false, d.push(Global{Module: ms, Name: ns})
	case 'R': // REDUCE
		args, err := d.pop()
		if err != nil {
			return false, err
		}
		fn, err := d.pop()
		if err != nil {
			return false, err
		}
		v, err := d.apply(fn, args, nil)
		if err != nil {
			return false, err
		}
		return false, d.push(v)
	case '\x81': // NEWOBJ
		args, err := d.pop()
		if err != nil {
			return false, err
		}
		cls, err := d.pop()
		if err != nil {
			return false, err
		}
		v, err := d.apply(cls, args, nil)
		if err != nil {
			return false, err
		}
		return false, d.push(v)
	case '\x92': // NEWOBJ_EX
		kw, err := d.pop()
		if err != nil {
			return false, err
		}
		args, err := d.pop()
		if err != nil {
			return false, err
		}
		cls, err := d.pop()
		if err != nil {
			return false, err
		}
		v, err := d.apply(cls, args, kw)
		if err != nil {
			return false, err
		}
		return false, d.push(v)
	case 'b': // BUILD
		state, err := d.pop()
		if err != nil {
			return false, err
		}
		obj, err := d.top()
		if err != nil {
			return false, err
		}
		switch o := obj.(type) {
		case *Object:
			o.State = state
		case *Dict:
			if s, ok := state.(*Dict); ok {
				for _, p := range s.Pairs {
					o.set(p.Key, p.Value)
				}
			}
		}
	case 'p': // PUT text
		line, err := d.line()
		if err != nil {
			return false, err
		}
		n, err := strconv.Atoi(line)
		if err != nil {
			return false, err
		}
		v, err := d.top()
		if err != nil {
			return false, err
		}
		return false, d.memoize(n, v)
	case 'q': // BINPUT
		n, err := d.r.ReadByte()
		if err != nil {
			return false, err
		}
		v, err := d.top()
		if err != nil {
			return false, err
		}
		return false, d.memoize(int(n), v)
	case 'r': // LONG_BINPUT
		n, err := d.u32()
		if err != nil {
			return false, err
		}
		v, err := d.top()
		if err != nil {
			return false, err
		}
		return false, d.memoize(int(n), v)
	case '\x94': // MEMOIZE
		v, err := d.top()
		if err != nil {
			return false, err
		}
		return false, d.memoize(len(d.memo), v)
	case 'g': // GET text
		line, err := d.line()
		if err != nil {
			return false, err
		}
		n, err := strconv.Atoi(line)
		if err != nil {
			return false, err
		}
		return false, d.get(n)
	case 'h': // BINGET
		n, err := d.r.ReadByte()
		if err != nil {
			return false, err
		}
		return false, d.get(int(n))
	case 'j': // LONG_BINGET
		n, err := d.u32()
		if err != nil {
			return false, err
		}
		return false, d.get(int(n))
	case 'P': // PERSID text
		line, err := d.line()
		if err != nil {
			return false, err
		}
		return false, d.push(Persistent{ID: line})
	case 'Q': // BINPERSID
		id, err := d.pop()
		if err != nil {
			return false, err
		}
		return false, d.push(Persistent{ID: id})
	default:
		return false, fmt.Errorf("pickle: unsupported opcode %#x", op)
	}
	return false, nil
}

// Applies a callable, through the hook first, else by recording it
func (d *Decoder) apply(fn, args, kw any) (any, error) {
	g, ok := fn.(Global)
	if !ok {
		if o, isObj := fn.(*Object); isObj {
			g = o.Class
		} else {
			return nil, fmt.Errorf("pickle: cannot apply %T", fn)
		}
	}
	var argv []any
	switch a := args.(type) {
	case Tuple:
		argv = []any(a)
	case *List:
		argv = a.Items
	case nil:
	default:
		argv = []any{a}
	}
	if v, ok := builtin(g, argv); ok {
		return v, nil
	}
	if d.Reduce != nil {
		if v, ok := d.Reduce(g, argv); ok {
			return v, nil
		}
	}
	obj := &Object{Class: g, Args: argv}
	if kwd, ok := kw.(*Dict); ok && kwd != nil {
		obj.KW = map[string]any{}
		for _, p := range kwd.Pairs {
			if k, ok := p.Key.(string); ok {
				obj.KW[k] = p.Value
			}
		}
	}
	return obj, nil
}

// Constructs the few standard library callables whose value matters
func builtin(g Global, args []any) (any, bool) {
	switch g.String() {
	case "collections.OrderedDict", "builtins.dict":
		return &Dict{}, true
	case "builtins.list", "builtins.set", "builtins.frozenset":
		if len(args) == 1 {
			switch a := args[0].(type) {
			case Tuple:
				return &List{Items: append([]any(nil), a...)}, true
			case *List:
				return &List{Items: append([]any(nil), a.Items...)}, true
			}
		}
		return &List{}, true
	case "builtins.tuple":
		if len(args) == 1 {
			if l, ok := args[0].(*List); ok {
				return Tuple(append([]any(nil), l.Items...)), true
			}
		}
		return Tuple{}, true
	case "torch.Size":
		if len(args) == 1 {
			switch a := args[0].(type) {
			case Tuple:
				return a, true
			case *List:
				return Tuple(append([]any(nil), a.Items...)), true
			}
		}
		return Tuple{}, true
	case "_codecs.encode":
		if len(args) >= 1 {
			if s, ok := args[0].(string); ok {
				return []byte(s), true
			}
		}
	case "builtins.getattr":
		if len(args) == 2 {
			if g, ok := args[0].(Global); ok {
				if name, ok := args[1].(string); ok {
					return Global{Module: g.String(), Name: name}, true
				}
			}
		}
	}
	return nil, false
}

func (d *Decoder) setItems(items []any) error {
	top, err := d.top()
	if err != nil {
		return err
	}
	dict, ok := top.(*Dict)
	if !ok {
		// A SETITEMS on an object updates its dict, kept as state
		if o, isObj := top.(*Object); isObj {
			if dict, ok = o.State.(*Dict); !ok {
				dict = &Dict{}
				o.State = dict
			}
		} else {
			return fmt.Errorf("pickle: SETITEMS on %T", top)
		}
	}
	for i := 0; i+1 < len(items); i += 2 {
		dict.set(items[i], items[i+1])
	}
	return nil
}

func (d *Decoder) appendItems(items []any) error {
	top, err := d.top()
	if err != nil {
		return err
	}
	l, ok := top.(*List)
	if !ok {
		return fmt.Errorf("pickle: APPEND on %T", top)
	}
	l.Items = append(l.Items, items...)
	return nil
}

func (d *Decoder) get(n int) error {
	v, ok := d.memo[n]
	if !ok {
		return fmt.Errorf("pickle: memo %d unset", n)
	}
	return d.push(v)
}

func (d *Decoder) pushLong(n int) error {
	buf, err := d.bytes(n)
	if err != nil {
		return err
	}
	if n == 0 {
		return d.push(int64(0))
	}
	// Little endian two's complement
	neg := buf[n-1]&0x80 != 0
	be := make([]byte, n)
	for i := range buf {
		be[n-1-i] = buf[i]
		if neg {
			be[n-1-i] = ^buf[i]
		}
	}
	v := new(big.Int).SetBytes(be)
	if neg {
		v.Add(v, big.NewInt(1))
		v.Neg(v)
	}
	return d.push(bigValue(v))
}

// Narrows an integer to int64 when it fits, else float64
func bigValue(v *big.Int) any {
	if v.IsInt64() {
		return v.Int64()
	}
	f, _ := new(big.Float).SetInt(v).Float64()
	return f
}

func (d *Decoder) line() (string, error) {
	s, err := d.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func (d *Decoder) bytes(n int) ([]byte, error) {
	if n < 0 || n > maxString {
		return nil, fmt.Errorf("pickle: string of %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (d *Decoder) u16() (uint16, error) {
	var buf [2]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

func (d *Decoder) u32() (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func (d *Decoder) u64() (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// Returns an integer value as int64 when the decoded value is one
func Int(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < 1<<62 {
			return int64(n), true
		}
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// Returns the items of a tuple or list
func Items(v any) ([]any, bool) {
	switch t := v.(type) {
	case Tuple:
		return t, true
	case *List:
		return t.Items, true
	}
	return nil, false
}
