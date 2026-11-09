package jinja

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
)

// The most items the sandbox lets range produce
const maxRange = 100000

func builtin(name string, fn func(r *renderer, args []Value, kw *Dict) (Value, error)) *Builtin {
	return &Builtin{name: name, fn: fn}
}

// A global that Python holds as a class, so its repr and type are those of a class
func class(qualified, instance string, fn func(r *renderer, args []Value, kw *Dict) (Value, error)) *Builtin {
	return &Builtin{name: instance, fn: fn, class: qualified, instance: instance}
}

// The globals transformers' environment offers, jinja2's defaults plus raise_exception and strftime_now
func (r *renderer) globals() map[string]Value {
	return map[string]Value{
		"range": builtin("safe_range", func(r *renderer, args []Value, kw *Dict) (Value, error) {
			if len(kw.keys) > 0 {
				return nil, typeError("range() takes no keyword arguments")
			}
			return pyRange(args)
		}),
		"dict": class("dict", "dict", func(r *renderer, args []Value, kw *Dict) (Value, error) {
			return pyDict("dict", args, kw)
		}),
		"namespace": class("jinja2.utils.Namespace", "Namespace", func(r *renderer, args []Value, kw *Dict) (Value, error) {
			d, err := pyDict("Namespace", args, kw)
			if err != nil {
				return nil, err
			}
			return &Namespace{attrs: d}, nil
		}),
		"cycler": class("jinja2.utils.Cycler", "Cycler", func(r *renderer, args []Value, kw *Dict) (Value, error) {
			if len(kw.keys) > 0 {
				return nil, typeError("Cycler.__init__() got an unexpected keyword argument %s", strRepr(kw.keys[0].(string)))
			}
			if len(args) == 0 {
				return nil, errorf("RuntimeError", "at least one item has to be provided")
			}
			return &Cycler{items: args}, nil
		}),
		"joiner": class("jinja2.utils.Joiner", "Joiner", func(r *renderer, args []Value, kw *Dict) (Value, error) {
			a, err := bindArgs("Joiner.__init__", []param{opt("sep", ", ")}, args, kw)
			if err != nil {
				return nil, err
			}
			return &Joiner{sep: a[0]}, nil
		}),
		"lipsum": builtin("generate_lorem_ipsum", func(r *renderer, args []Value, kw *Dict) (Value, error) {
			a, err := bindArgs("generate_lorem_ipsum", []param{opt("n", int64(5)), opt("html", true), opt("min", int64(20)), opt("max", int64(100))}, args, kw)
			if err != nil {
				return nil, err
			}
			return loremIpsum(r, a)
		}),
		"raise_exception": builtin("_cached_compile_jinja_template.<locals>.raise_exception", func(r *renderer, args []Value, kw *Dict) (Value, error) {
			a, err := bindArgs("raise_exception", []param{req("message")}, args, kw)
			if err != nil {
				return nil, err
			}
			return nil, exceptionWith("TemplateError", a[0])
		}),
		"strftime_now": builtin("_cached_compile_jinja_template.<locals>.strftime_now", func(r *renderer, args []Value, kw *Dict) (Value, error) {
			a, err := bindArgs("strftime_now", []param{req("format")}, args, kw)
			if err != nil {
				return nil, err
			}
			format, ok := asStr(a[0])
			if !ok {
				return nil, typeError("strftime() argument 1 must be str, not %s", typeName(a[0]))
			}
			return pyStrftime(r.clock(), format), nil
		}),
	}
}

// Python's range built from one to three integer arguments, refused beyond the sandbox limit
func pyRange(args []Value) (Value, error) {
	if len(args) == 0 || len(args) > 3 {
		if len(args) == 0 {
			return nil, typeError("range expected at least 1 argument, got 0")
		}
		return nil, typeError("range expected at most 3 arguments, got %d", len(args))
	}
	nums := make([]*big.Int, len(args))
	for i, a := range args {
		if !isIntegral(a) {
			return nil, typeError("%s object cannot be interpreted as an integer", strRepr(typeName(a)))
		}
		nums[i] = toBig(a)
	}
	start, stop, step := big.NewInt(0), nums[0], big.NewInt(1)
	if len(nums) >= 2 {
		start, stop = nums[0], nums[1]
	}
	if len(nums) == 3 {
		step = nums[2]
		if step.Sign() == 0 {
			return nil, errorf("ValueError", "range() arg 3 must not be zero")
		}
	}
	rng, err := newRange(new(big.Int).Set(start), new(big.Int).Set(stop), new(big.Int).Set(step))
	if err != nil {
		return nil, err
	}
	if rng.n > maxRange {
		return nil, errorf("OverflowError", "Range too big. The sandbox blocks ranges larger than MAX_RANGE (%d).", maxRange)
	}
	return rng, nil
}

// Python's dict constructor from an optional mapping or pair iterable and keyword arguments
func pyDict(fname string, args []Value, kw *Dict) (*Dict, error) {
	d := newDict()
	if len(args) > 1 {
		return nil, typeError("%s expected at most 1 argument, got %d", fname, len(args))
	}
	if len(args) == 1 {
		if src, ok := args[0].(*Dict); ok {
			d = src.copy()
		} else {
			pairs, err := iterValues(args[0])
			if err != nil {
				return nil, typeError("%s object is not iterable", strRepr(typeName(args[0])))
			}
			for i, p := range pairs {
				kv, err := iterValues(p)
				if err != nil {
					return nil, typeError("cannot convert dictionary update sequence element #%d to a sequence", i)
				}
				if len(kv) != 2 {
					return nil, errorf("ValueError", "dictionary update sequence element #%d has length %d; 2 is required", i, len(kv))
				}
				if err := d.set(kv[0], kv[1]); err != nil {
					return nil, err
				}
			}
		}
	}
	for i, k := range kw.keys {
		_ = d.set(k, kw.vals[i])
	}
	return d, nil
}

var loremWords = strings.Fields(`a ac accumsan ad adipiscing aenean aliquam aliquet amet ante aptent arcu at
auctor augue bibendum blandit class commodo condimentum congue consectetuer
consequat conubia convallis cras cubilia cum curabitur curae cursus dapibus
diam dictum dictumst dignissim dis dolor donec dui duis egestas eget eleifend
elementum elit enim erat eros est et etiam eu euismod facilisi facilisis fames
faucibus felis fermentum feugiat fringilla fusce gravida habitant habitasse hac
hendrerit hymenaeos iaculis id imperdiet in inceptos integer interdum ipsum
justo lacinia lacus laoreet lectus leo libero ligula litora lobortis lorem
luctus maecenas magna magnis malesuada massa mattis mauris metus mi molestie
mollis montes morbi mus nam nascetur natoque nec neque netus nibh nisi nisl non
nonummy nostra nulla nullam nunc odio orci ornare parturient pede pellentesque
penatibus per pharetra phasellus placerat platea porta porttitor posuere
potenti praesent pretium primis proin pulvinar purus quam quis quisque rhoncus
ridiculus risus rutrum sagittis sapien scelerisque sed sem semper senectus sit
sociis sociosqu sodales sollicitudin suscipit suspendisse taciti tellus tempor
tempus tincidunt torquent tortor tristique turpis ullamcorper ultrices
ultricies urna ut varius vehicula vel velit venenatis vestibulum vitae vivamus
viverra volutpat vulputate`)

// jinja2's generate_lorem_ipsum, random paragraphs of lorem ipsum text
func loremIpsum(r *renderer, a []Value) (Value, error) {
	ints := make([]int, 0, 3)
	for _, v := range []Value{a[0], a[2], a[3]} {
		n, err := argInt(v)
		if err != nil {
			return nil, err
		}
		ints = append(ints, n)
	}
	n, lo, hi := ints[0], ints[1], ints[2]
	if hi <= lo {
		return nil, errorf("ValueError", "empty range in randrange(%d, %d)", lo, hi)
	}
	var paragraphs []string
	for range max(n, 0) {
		nextCap, last := true, ""
		var words []string
		count := lo + rand.IntN(hi-lo)
		lastComma, lastFullstop := 0, 0
		for i := 0; i < count; i++ {
			word := ""
			for {
				word = loremWords[rand.IntN(len(loremWords))]
				if word != last {
					last = word
					break
				}
			}
			if nextCap {
				word = pyCapitalize(word)
				nextCap = false
			}
			if i-(3+rand.IntN(5)) > lastComma {
				lastComma = i
				lastFullstop += 2
				word += ","
			}
			if i-(10+rand.IntN(10)) > lastFullstop {
				lastComma, lastFullstop = i, i
				word += "."
				nextCap = true
			}
			if err := r.charge(len(word) + 1); err != nil {
				return nil, err
			}
			words = append(words, word)
		}
		p := strings.Join(words, " ")
		if strings.HasSuffix(p, ",") {
			p = p[:len(p)-1] + "."
		} else if !strings.HasSuffix(p, ".") {
			p += "."
		}
		paragraphs = append(paragraphs, p)
	}
	if !truthy(a[1]) {
		return strings.Join(paragraphs, "\n\n"), nil
	}
	for i, p := range paragraphs {
		paragraphs[i] = fmt.Sprintf("<p>%s</p>", htmlEscape(p))
	}
	return Markup(strings.Join(paragraphs, "\n")), nil
}
