package jinja

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Shortest or rounded decimal digits of a non negative finite float and its decimal point, as dtoa yields them
func dtoaDigits(f float64, code byte, prec int) (string, int) {
	if f == 0 {
		return "0", 1
	}
	switch code {
	case 'r':
		return shortestDigits(f)
	case 'f':
		s := strconv.FormatFloat(f, 'f', prec, 64)
		intPart, frac, _ := strings.Cut(s, ".")
		digits := intPart + frac
		decpt := len(intPart)
		for digits != "" && digits[0] == '0' {
			digits = digits[1:]
			decpt--
		}
		return strings.TrimRight(digits, "0"), decpt
	}
	s := strconv.FormatFloat(f, 'e', prec-1, 64)
	mant, expPart, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expPart)
	digits := strings.TrimRight(strings.Replace(mant, ".", "", 1), "0")
	if digits == "" {
		digits = "0"
	}
	return digits, exp + 1
}

// Formats a float as CPython's PyOS_double_to_string does for the e, f, g and r codes
func pyFloatFormat(f float64, code byte, prec int, alt, addDot0, plus, space, noNegZero bool) string {
	upper := code == 'E' || code == 'F' || code == 'G'
	code = byte(strings.ToLower(string(code))[0])
	switch code {
	case 'e':
		prec++
	case 'g':
		if prec == 0 {
			prec = 1
		}
	}
	neg := math.Signbit(f) && !math.IsNaN(f)
	var body string
	switch {
	case math.IsNaN(f):
		body = "nan"
	case math.IsInf(f, 0):
		body = "inf"
	default:
		digits, decpt := dtoaDigits(math.Abs(f), code, prec)
		if noNegZero && strings.Trim(digits, "0") == "" {
			neg = false
		}
		if noNegZero && code == 'f' && decpt <= -prec {
			neg = false
		}
		body = layoutDigits(digits, decpt, code, prec, alt, addDot0)
	}
	if upper {
		body = strings.ToUpper(body)
	}
	switch {
	case neg:
		return "-" + body
	case plus:
		return "+" + body
	case space:
		return " " + body
	}
	return body
}

// Lays digits out with zero padding, decimal point and exponent following format_float_short
func layoutDigits(digits string, decpt int, code byte, prec int, alt, addDot0 bool) string {
	vEnd := len(digits)
	useExp := false
	switch code {
	case 'e':
		useExp = true
		vEnd = prec
	case 'f':
		vEnd = decpt + prec
	case 'g':
		limit := prec
		if addDot0 {
			limit = prec - 1
		}
		if decpt <= -4 || decpt > limit {
			useExp = true
		}
		if alt {
			vEnd = prec
		}
	case 'r':
		if decpt <= -4 || decpt > 16 {
			useExp = true
		}
	}
	exp := 0
	if useExp {
		exp = decpt - 1
		decpt = 1
	}
	vStart := 0
	if decpt <= 0 {
		vStart = decpt - 1
	}
	if !useExp && addDot0 {
		vEnd = max(vEnd, decpt+1)
	} else {
		vEnd = max(vEnd, decpt)
	}
	var b strings.Builder
	if decpt <= 0 {
		b.WriteString(strings.Repeat("0", decpt-vStart))
		b.WriteByte('.')
		b.WriteString(strings.Repeat("0", -decpt))
	} else {
		b.WriteString(strings.Repeat("0", -vStart))
	}
	if decpt > 0 && decpt <= len(digits) {
		b.WriteString(digits[:decpt])
		b.WriteByte('.')
		b.WriteString(digits[decpt:])
	} else {
		b.WriteString(digits)
	}
	if len(digits) < decpt {
		b.WriteString(strings.Repeat("0", decpt-len(digits)))
		b.WriteByte('.')
		b.WriteString(strings.Repeat("0", vEnd-decpt))
	} else {
		b.WriteString(strings.Repeat("0", vEnd-len(digits)))
	}
	out := b.String()
	if strings.HasSuffix(out, ".") && !alt {
		out = out[:len(out)-1]
	}
	if useExp {
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		e := strconv.Itoa(exp)
		if len(e) < 2 {
			e = "0" + e
		}
		out += "e" + sign + e
	}
	return out
}

// Python's printf style string formatting, the % operator on strings
func printfFormat(format string, args Value) (string, error) {
	current, arglen, argidx := args, -1, -2
	if t, ok := args.(*Tuple); ok {
		arglen, argidx = len(t.items), 0
	}
	var mapping Value
	if hasGetitem(args) {
		mapping = args
	}
	take := func() (Value, error) {
		if argidx >= arglen {
			return nil, typeError("not enough arguments for format string")
		}
		argidx++
		if arglen < 0 {
			return current, nil
		}
		return current.(*Tuple).items[argidx-1], nil
	}
	var out strings.Builder
	rs := []rune(format)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '%' {
			out.WriteRune(rs[i])
			continue
		}
		i++
		if i >= len(rs) {
			return "", errorf("ValueError", "incomplete format")
		}
		if rs[i] == '(' {
			if mapping == nil {
				return "", typeError("format requires a mapping")
			}
			depth := 1
			j := i + 1
			for j < len(rs) && depth > 0 {
				switch rs[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			if depth > 0 {
				return "", errorf("ValueError", "incomplete format key")
			}
			v, err := mappingItem(mapping, string(rs[i+1:j-1]))
			if err != nil {
				return "", err
			}
			current, arglen, argidx = v, -1, -2
			i = j
		}
		var spec fmtSpec
		for ; i < len(rs); i++ {
			switch rs[i] {
			case '-':
				spec.left = true
				continue
			case '+':
				spec.plus = true
				continue
			case ' ':
				spec.space = true
				continue
			case '#':
				spec.alt = true
				continue
			case '0':
				spec.zero = true
				continue
			}
			break
		}
		// Reads a width or precision, from the arguments for a star, refusing what CPython refuses
		readNum := func(prec bool) (int, bool, error) {
			if i < len(rs) && rs[i] == '*' {
				i++
				v, err := take()
				if err != nil {
					return 0, false, err
				}
				if _, isInt := v.(int64); !isInt && !isBigInt(v) {
					return 0, false, typeError("* wants int")
				}
				if prec {
					n, err := argCInt(v)
					return n, true, err
				}
				n, err := argSsize(v)
				return n, true, err
			}
			start := i
			for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
				i++
			}
			if start == i {
				return 0, false, nil
			}
			n, err := strconv.Atoi(string(rs[start:i]))
			switch {
			case prec && (err != nil || n > math.MaxInt32):
				return 0, false, errorf("ValueError", "precision too big")
			case err != nil:
				return 0, false, errorf("ValueError", "width too big")
			}
			return n, true, nil
		}
		w, hasW, err := readNum(false)
		if err != nil {
			return "", err
		}
		if hasW && w < 0 {
			spec.left = true
			w = -w
		}
		if err := checkAlloc(w); err != nil {
			return "", err
		}
		spec.width = w
		spec.prec = -1
		if i < len(rs) && rs[i] == '.' {
			i++
			p, _, err := readNum(true)
			if err != nil {
				return "", err
			}
			spec.prec = max(p, 0)
		}
		for i < len(rs) && (rs[i] == 'h' || rs[i] == 'l' || rs[i] == 'L') {
			i++
		}
		if i >= len(rs) {
			return "", errorf("ValueError", "incomplete format")
		}
		conv := rs[i]
		if conv == '%' {
			out.WriteByte('%')
			continue
		}
		arg, err := take()
		if err != nil {
			return "", err
		}
		s, err := printfOne(conv, arg, spec)
		if err == nil {
			err = checkBytes(out.Len() + len(s))
		}
		if err != nil {
			return "", err
		}
		out.WriteString(s)
	}
	if argidx < arglen && mapping == nil {
		return "", typeError("not all arguments converted during string formatting")
	}
	return out.String(), nil
}

// markupsafe's _MarkupEscapeHelper, escaping what Markup's % operator converts
type escapeHelper struct{ obj Value }

// Whether % formatting takes the value as a mapping, which Python decides by a __getitem__ method
func hasGetitem(v Value) bool {
	switch v.(type) {
	case *Dict, *List, *Range, *Undefined, *escapeHelper:
		return true
	}
	return false
}

// The value a %(key) conversion reads from the mapping, failing as Python's subscript does
func mappingItem(mapping Value, key string) (Value, error) {
	switch m := mapping.(type) {
	case nil:
		return nil, typeError("format requires a mapping")
	case *Dict:
		v, found, err := m.get(key)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errorf("KeyError", "%s", strRepr(key))
		}
		return v, nil
	case *List:
		return nil, typeError("list indices must be integers or slices, not str")
	case *Range:
		return nil, typeError("range indices must be integers or slices, not str")
	case *Undefined:
		return nil, m.fail()
	case *escapeHelper:
		v, err := mappingItem(m.obj, key)
		if err != nil {
			return nil, err
		}
		return &escapeHelper{obj: v}, nil
	}
	return nil, typeError("format requires a mapping")
}

type fmtSpec struct {
	left, plus, space, alt, zero bool
	width, prec                  int
}

// Formats one printf conversion
func printfOne(conv rune, arg Value, spec fmtSpec) (string, error) {
	if h, ok := arg.(*escapeHelper); ok {
		return helperOne(conv, h, spec)
	}
	switch conv {
	case 's', 'r', 'a':
		text := pyStr
		if conv != 's' {
			text = pyRepr
		}
		s, err := text(arg)
		if err != nil {
			return "", err
		}
		if conv == 'a' {
			s = pyASCII(s)
		}
		if spec.prec >= 0 && runeLen(s) > spec.prec {
			s = string([]rune(s)[:spec.prec])
		}
		return padField(s, spec.width, spec.left), nil
	case 'c':
		switch a := arg.(type) {
		case bool, int64, *big.Int:
			n := toBig(a)
			if n.Sign() < 0 || n.Cmp(big.NewInt(0x10ffff)) > 0 {
				return "", errorf("OverflowError", "%%c arg not in range(0x110000)")
			}
			return padField(encodeRune(rune(n.Int64())), spec.width, spec.left), nil
		case string, Markup:
			s, _ := asStr(a)
			if runeLen(s) != 1 {
				return "", typeError("%%c requires an int or a unicode character, not a string of length %d", runeLen(s))
			}
			return padField(s, spec.width, spec.left), nil
		}
		return "", typeError("%%c requires an int or a unicode character, not %s", typeName(arg))
	case 'd', 'i', 'u', 'o', 'x', 'X':
		var n *big.Int
		switch a := arg.(type) {
		case bool, int64, *big.Int:
			n = toBig(a)
		case float64:
			if conv != 'd' && conv != 'i' && conv != 'u' {
				return "", typeError("%%%c format: an integer is required, not float", conv)
			}
			if math.IsInf(a, 0) {
				return "", errorf("OverflowError", "cannot convert float infinity to integer")
			}
			if math.IsNaN(a) {
				return "", errorf("ValueError", "cannot convert float NaN to integer")
			}
			n, _ = new(big.Float).SetFloat64(math.Trunc(a)).Int(nil)
		case *Undefined:
			if conv == 'd' || conv == 'i' || conv == 'u' {
				return "", a.fail()
			}
			return "", typeError("%%%c format: an integer is required, not Undefined", conv)
		default:
			if conv == 'd' || conv == 'i' || conv == 'u' {
				return "", typeError("%%%c format: a real number is required, not %s", conv, typeName(arg))
			}
			return "", typeError("%%%c format: an integer is required, not %s", conv, typeName(arg))
		}
		base := 10
		prefix := ""
		switch conv {
		case 'o':
			base = 8
			if spec.alt {
				prefix = "0o"
			}
		case 'x', 'X':
			base = 16
			if spec.alt {
				prefix = "0" + string(conv)
			}
		}
		digits, err := baseDigits(n, base)
		if err != nil {
			return "", err
		}
		if conv == 'X' {
			digits = strings.ToUpper(digits)
		}
		if spec.prec > len(digits) {
			digits = strings.Repeat("0", spec.prec-len(digits)) + digits
		}
		sign := ""
		switch {
		case n.Sign() < 0:
			sign = "-"
		case spec.plus:
			sign = "+"
		case spec.space:
			sign = " "
		}
		return padNumber(sign+prefix, digits, spec.width, spec.left, spec.zero), nil
	case 'e', 'E', 'f', 'F', 'g', 'G':
		f, err := toFloatArg(arg)
		if err != nil {
			return "", err
		}
		if err := checkPrecision(spec.prec); err != nil {
			return "", err
		}
		prec := spec.prec
		if prec < 0 {
			prec = 6
		}
		s := pyFloatFormat(f, byte(conv), prec, spec.alt, false, spec.plus, spec.space, false)
		sign := ""
		if s != "" && (s[0] == '-' || s[0] == '+' || s[0] == ' ') {
			sign, s = s[:1], s[1:]
		}
		return padNumber(sign, s, spec.width, spec.left, spec.zero), nil
	}
	return "", errorf("ValueError", "unsupported format character %s (0x%x)", strRepr(string(conv)), conv)
}

func toFloatArg(arg Value) (float64, error) {
	switch x := arg.(type) {
	case bool, int64, *big.Int, float64:
		return toFloat(arg)
	case *Undefined:
		return 0, x.fail()
	}
	return 0, typeError("must be real number, not %s", typeName(arg))
}

// One conversion of a Markup % operand, which escapes text and converts numbers through int() and float()
func helperOne(conv rune, h *escapeHelper, spec fmtSpec) (string, error) {
	switch conv {
	case 's':
		m, err := escapeValue(h.obj)
		if err != nil {
			return "", err
		}
		return printfOne('s', string(m), spec)
	case 'r', 'a':
		s, err := pyRepr(h.obj)
		if err != nil {
			return "", err
		}
		s = htmlEscape(s)
		if conv == 'a' {
			s = pyASCII(s)
		}
		return printfOne('s', s, spec)
	case 'd', 'i', 'u':
		n, err := pyIntConvert(h.obj, int64(10))
		if e, ok := err.(*Error); ok && e.Kind == "TypeError" {
			return "", typeError("%%%c format: a real number is required, not _MarkupEscapeHelper", conv)
		}
		if err != nil {
			return "", err
		}
		return printfOne(conv, n, spec)
	case 'e', 'E', 'f', 'F', 'g', 'G':
		f, err := pyFloatConvert(h.obj)
		if err != nil {
			return "", err
		}
		return printfOne(conv, f, spec)
	case 'o', 'x', 'X':
		return "", typeError("%%%c format: an integer is required, not _MarkupEscapeHelper", conv)
	case 'c':
		return "", typeError("%%c requires an int or a unicode character, not _MarkupEscapeHelper")
	}
	return printfOne(conv, nil, spec)
}

// Pads a numeric field, zeros going between the sign and digits unless left aligned
func padNumber(sign, digits string, width int, left, zero bool) string {
	n := runeLen(sign) + runeLen(digits)
	if n >= width {
		return sign + digits
	}
	switch {
	case left:
		return sign + digits + strings.Repeat(" ", width-n)
	case zero:
		return sign + strings.Repeat("0", width-n) + digits
	}
	return strings.Repeat(" ", width-n) + sign + digits
}

func padField(s string, width int, left bool) string {
	n := runeLen(s)
	if n >= width {
		return s
	}
	if left {
		return s + strings.Repeat(" ", width-n)
	}
	return strings.Repeat(" ", width-n) + s
}

// Escapes non ASCII runes of a repr the way Python's ascii() does
func pyASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r <= 0xff:
			b.WriteString(`\x` + leftPadHex(int64(r), 2))
		case r <= 0xffff:
			b.WriteString(`\u` + leftPadHex(int64(r), 4))
		default:
			b.WriteString(`\U` + leftPadHex(int64(r), 8))
		}
	}
	return b.String()
}

func leftPadHex(v int64, n int) string {
	s := strconv.FormatInt(v, 16)
	return strings.Repeat("0", max(0, n-len(s))) + s
}

// A parsed format specification of Python's format mini language
type formatSpec struct {
	fill                 rune
	align                byte
	sign                 byte
	noNegZero, alt, zero bool
	width, prec          int
	grouping, typ        byte
	hasFill, hasAlign    bool
}

// Parses a standard format specifier
func parseFormatSpec(spec string) (formatSpec, error) {
	fs := formatSpec{fill: ' ', prec: -1}
	rs := []rune(spec)
	i := 0
	isAlign := func(r rune) bool { return r == '<' || r == '>' || r == '^' || r == '=' }
	if len(rs) >= 2 && isAlign(rs[1]) {
		fs.fill, fs.align, fs.hasFill, fs.hasAlign = rs[0], byte(rs[1]), true, true
		i = 2
	} else if len(rs) >= 1 && isAlign(rs[0]) {
		fs.align, fs.hasAlign = byte(rs[0]), true
		i = 1
	}
	if i < len(rs) && (rs[i] == '+' || rs[i] == '-' || rs[i] == ' ') {
		fs.sign = byte(rs[i])
		i++
	}
	if i < len(rs) && rs[i] == 'z' {
		fs.noNegZero = true
		i++
	}
	if i < len(rs) && rs[i] == '#' {
		fs.alt = true
		i++
	}
	if i < len(rs) && rs[i] == '0' {
		fs.zero = true
		if !fs.hasFill {
			fs.fill = '0'
		}
		if !fs.hasAlign {
			fs.align = '='
		}
		i++
	}
	start := i
	for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
		i++
	}
	if i > start {
		var err error
		if fs.width, err = strconv.Atoi(string(rs[start:i])); err != nil {
			return fs, errorf("ValueError", "Too many decimal digits in format string")
		}
		if err := checkAlloc(fs.width); err != nil {
			return fs, err
		}
	}
	if i < len(rs) && (rs[i] == ',' || rs[i] == '_') {
		fs.grouping = byte(rs[i])
		i++
		if i < len(rs) && (rs[i] == ',' || rs[i] == '_') {
			return fs, errorf("ValueError", "Cannot specify both ',' and '_'.")
		}
	}
	if i < len(rs) && rs[i] == '.' {
		i++
		start = i
		for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
			i++
		}
		if i == start {
			return fs, errorf("ValueError", "Format specifier missing precision")
		}
		var err error
		if fs.prec, err = strconv.Atoi(string(rs[start:i])); err != nil {
			return fs, errorf("ValueError", "Too many decimal digits in format string")
		}
	}
	if len(rs)-i > 1 {
		return fs, errorf("ValueError", "Invalid format specifier '%s' for object of type", spec)
	}
	if i < len(rs) {
		fs.typ = byte(rs[i])
	}
	return fs, nil
}

// Python's builtin format(value, spec) for the value types templates hold
func pyFormat(v Value, spec string) (string, error) {
	if _, ok := v.(*Undefined); ok {
		if spec != "" {
			return "", typeError("unsupported format string passed to Undefined.__format__")
		}
		return "", nil
	}
	switch x := v.(type) {
	case string, Markup:
		fs, err := parseFormatSpec(spec)
		if err != nil {
			return "", err
		}
		if fs.typ != 0 && fs.typ != 's' {
			return "", errorf("ValueError", "Unknown format code '%c' for object of type 'str'", fs.typ)
		}
		if fs.sign != 0 {
			return "", errorf("ValueError", "Sign not allowed in string format specifier")
		}
		if fs.align == '=' {
			return "", errorf("ValueError", "'=' alignment not allowed in string format specifier")
		}
		s, _ := asStr(x)
		if fs.prec >= 0 && runeLen(s) > fs.prec {
			s = string([]rune(s)[:fs.prec])
		}
		align := fs.align
		if align == 0 {
			align = '<'
		}
		return alignText(s, fs.fill, align, fs.width), nil
	case bool:
		if spec == "" {
			return pyStr(x)
		}
		return formatInt(toBig(x), spec)
	case int64, *big.Int:
		return formatInt(toBig(x), spec)
	case float64:
		return formatFloatSpec(x, spec)
	case complex128:
		return formatComplex(x, spec)
	}
	if spec != "" {
		return "", typeError("unsupported format string passed to %s.__format__", typeName(v))
	}
	return pyStr(v)
}

func alignText(s string, fill rune, align byte, width int) string {
	n := runeLen(s)
	if n >= width {
		return s
	}
	pad := width - n
	f := string(fill)
	switch align {
	case '>':
		return strings.Repeat(f, pad) + s
	case '^':
		left := pad / 2
		return strings.Repeat(f, left) + s + strings.Repeat(f, pad-left)
	}
	return s + strings.Repeat(f, pad)
}

// Inserts a grouping separator every size digits from the right
func groupDigits(digits string, sep byte, size int) string {
	if sep == 0 {
		return digits
	}
	var parts []string
	for len(digits) > size {
		parts = append([]string{digits[len(digits)-size:]}, parts...)
		digits = digits[:len(digits)-size]
	}
	parts = append([]string{digits}, parts...)
	return strings.Join(parts, string(sep))
}

// Assembles sign, prefix and digits into the field, zero padding inside groups when asked
func finishNumber(fs formatSpec, neg bool, prefix, intDigits, rest string, groupSize int) string {
	sign := ""
	switch {
	case neg:
		sign = "-"
	case fs.sign == '+':
		sign = "+"
	case fs.sign == ' ':
		sign = " "
	}
	align := fs.align
	if align == 0 {
		align = '>'
	}
	if align == '=' && fs.fill == '0' && fs.grouping != 0 {
		avail := fs.width - runeLen(sign) - len(prefix) - runeLen(rest)
		grouped := groupDigits(intDigits, fs.grouping, groupSize)
		for len(grouped) < avail {
			intDigits = "0" + intDigits
			grouped = groupDigits(intDigits, fs.grouping, groupSize)
		}
		return sign + prefix + grouped + rest
	}
	body := groupDigits(intDigits, fs.grouping, groupSize) + rest
	n := runeLen(sign) + len(prefix) + runeLen(body)
	if n >= fs.width {
		return sign + prefix + body
	}
	pad := strings.Repeat(string(fs.fill), fs.width-n)
	switch align {
	case '=':
		return sign + prefix + pad + body
	case '<':
		return sign + prefix + body + pad
	case '^':
		half := (fs.width - n) / 2
		return strings.Repeat(string(fs.fill), half) + sign + prefix + body + strings.Repeat(string(fs.fill), fs.width-n-half)
	}
	return pad + sign + prefix + body
}

func formatInt(n *big.Int, spec string) (string, error) {
	fs, err := parseFormatSpec(spec)
	if err != nil {
		return "", err
	}
	switch fs.typ {
	case 'e', 'E', 'f', 'F', 'g', 'G', '%':
		f, err := toFloat(n)
		if err != nil {
			return "", err
		}
		return formatFloatSpec(f, spec)
	case 'c':
		if fs.sign != 0 || fs.alt {
			return "", errorf("ValueError", "Sign not allowed with integer format specifier 'c'")
		}
		if !n.IsInt64() {
			return "", errorf("OverflowError", "Python int too large to convert to C long")
		}
		if n.Int64() < 0 || n.Int64() > 0x10ffff {
			return "", errorf("OverflowError", "%%c arg not in range(0x110000)")
		}
		align := fs.align
		if align == 0 {
			align = '<'
		}
		return alignText(encodeRune(rune(n.Int64())), fs.fill, align, fs.width), nil
	}
	if fs.prec >= 0 {
		return "", errorf("ValueError", "Precision not allowed in integer format specifier")
	}
	base, prefix, groupSize := 10, "", 3
	switch fs.typ {
	case 'b':
		base, prefix, groupSize = 2, "0b", 4
	case 'o':
		base, prefix, groupSize = 8, "0o", 4
	case 'x':
		base, prefix, groupSize = 16, "0x", 4
	case 'X':
		base, prefix, groupSize = 16, "0X", 4
	case 0, 'd', 'n':
	default:
		return "", errorf("ValueError", "Unknown format code '%c' for object of type 'int'", fs.typ)
	}
	if !fs.alt {
		prefix = ""
	}
	digits, err := baseDigits(n, base)
	if err != nil {
		return "", err
	}
	if fs.typ == 'X' {
		digits = strings.ToUpper(digits)
	}
	return finishNumber(fs, n.Sign() < 0, prefix, digits, "", groupSize), nil
}

func formatFloatSpec(f float64, spec string) (string, error) {
	fs, err := parseFormatSpec(spec)
	if err != nil {
		return "", err
	}
	if err := checkPrecision(fs.prec); err != nil {
		return "", err
	}
	code := fs.typ
	prec := fs.prec
	addDot0 := false
	switch code {
	case 0:
		addDot0 = true
		if prec < 0 {
			code, prec = 'r', 0
		} else {
			code = 'g'
		}
	case 'n':
		code = 'g'
		if prec < 0 {
			prec = 6
		}
	case 'e', 'E', 'f', 'F', 'g', 'G', '%':
		if prec < 0 {
			prec = 6
		}
	default:
		return "", errorf("ValueError", "Unknown format code '%c' for object of type 'float'", code)
	}
	suffix := ""
	if code == '%' {
		f *= 100
		code = 'f'
		suffix = "%"
	}
	s := pyFloatFormat(f, code, prec, fs.alt, addDot0, false, false, fs.noNegZero)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intEnd := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if intEnd < 0 {
		intEnd = len(s)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		intEnd = 0
	}
	return finishNumber(fs, neg, "", s[:intEnd], s[intEnd:]+suffix, 3), nil
}

// Python str.format with field lookups going through the sandbox
func (r *renderer) strFormat(format string, args []Value, kwargs *Dict, escape bool) (string, error) {
	return r.strFormatTracked(format, args, kwargs, escape, nil)
}

// Formats like strFormat, passing track the frame each step of the sandbox's string.Formatter reaches above the template frame
func (r *renderer) strFormatTracked(format string, args []Value, kwargs *Dict, escape bool, track func(int)) (string, error) {
	if track == nil {
		track = func(int) {}
	}
	auto := 0
	manual := false
	autoUsed := false
	var expand func(text string, depth int) (string, error)
	expand = func(text string, depth int) (string, error) {
		vf := 5 + depth
		track(vf + 1)
		var b strings.Builder
		for i := 0; i < len(text); {
			c := text[i]
			if c == '}' {
				if i+1 < len(text) && text[i+1] == '}' {
					b.WriteByte('}')
					i += 2
					continue
				}
				return "", errorf("ValueError", "Single '}' encountered in format string")
			}
			if c != '{' {
				_, size := utf8.DecodeRuneInString(text[i:])
				b.WriteString(text[i : i+size])
				i += size
				continue
			}
			if i+1 < len(text) && text[i+1] == '{' {
				b.WriteByte('{')
				i += 2
				continue
			}
			level := 1
			j := i + 1
			for j < len(text) && level > 0 {
				switch text[j] {
				case '{':
					level++
				case '}':
					level--
				}
				j++
			}
			if level > 0 {
				return "", errorf("ValueError", "expected '}' before end of string")
			}
			field := text[i+1 : j-1]
			i = j
			if depth > 1 {
				track(vf + 1)
				return "", errorf("ValueError", "Max string recursion exceeded")
			}
			name, specText, hasSpec := cutField(field)
			conv := byte(0)
			if k := strings.IndexByte(name, '!'); k >= 0 {
				if k+2 != len(name) {
					return "", errorf("ValueError", "expected ':' after conversion specifier")
				}
				conv = name[k+1]
				name = name[:k]
			}
			first, rest := splitFieldName(name)
			var obj Value
			if first == "" {
				if manual {
					return "", errorf("ValueError", "cannot switch from manual field specification to automatic field numbering")
				}
				autoUsed = true
				track(vf + 2)
				if auto >= len(args) {
					return "", errorf("IndexError", "Replacement index %d out of range for positional args tuple", auto)
				}
				obj = args[auto]
				auto++
			} else if n, err := strconv.Atoi(first); err == nil {
				if autoUsed {
					return "", errorf("ValueError", "cannot switch from automatic field numbering to manual field specification")
				}
				manual = true
				track(vf + 2)
				if n >= len(args) {
					return "", errorf("IndexError", "Replacement index %d out of range for positional args tuple", n)
				}
				obj = args[n]
			} else {
				track(vf + 2)
				v, found := kwargs.lookup(first)
				if !found {
					return "", errorf("KeyError", "%s", strRepr(first))
				}
				obj = v
			}
			for _, part := range rest {
				var err error
				if part.attr {
					track(vf + 1 + r.getattrFrames(obj, part.key.(string)))
					obj, err = r.getattr(obj, part.key.(string))
				} else {
					track(vf + 1 + r.getitemFrames(obj, part.key))
					obj, err = r.getitem(obj, part.key)
				}
				if err != nil {
					return "", err
				}
			}
			var err error
			track(vf + 1)
			switch conv {
			case 's':
				f, _ := strWalk(obj)
				track(vf + 1 + f)
			case 'r', 'a':
				f, _ := reprWalk(obj)
				track(vf + 1 + f)
			}
			switch conv {
			case 0:
			case 'r':
				obj, err = pyRepr(obj)
			case 's':
				obj, err = pyStr(obj)
			case 'a':
				var s string
				s, err = pyRepr(obj)
				obj = pyASCII(s)
			default:
				return "", errorf("ValueError", "Unknown conversion specifier %c", conv)
			}
			if err != nil {
				return "", err
			}
			if hasSpec {
				var err error
				if specText, err = expand(specText, depth+1); err != nil {
					return "", err
				}
			} else {
				track(vf + 2)
			}
			track(formatFieldFrames(vf+1, obj, specText, escape))
			if escape {
				if obj, err = escapeValue(obj); err != nil {
					return "", err
				}
			}
			s, err := pyFormat(obj, specText)
			if err == nil {
				err = checkBytes(b.Len() + len(s))
			}
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		return b.String(), nil
	}
	return expand(format, 0)
}

// Splits a replacement field into its name with conversion and its format spec
func cutField(field string) (string, string, bool) {
	depth := 0
	for i := 0; i < len(field); i++ {
		switch field[i] {
		case '[':
			depth++
		case ']':
			depth--
		case ':':
			if depth == 0 {
				return field[:i], field[i+1:], true
			}
		}
	}
	return field, "", false
}

type fieldPart struct {
	attr bool
	key  Value
}

// Splits a field name into its first part and the attribute and index lookups after it
func splitFieldName(name string) (string, []fieldPart) {
	end := strings.IndexAny(name, ".[")
	if end < 0 {
		return name, nil
	}
	first := name[:end]
	var parts []fieldPart
	rest := name[end:]
	for rest != "" {
		if rest[0] == '.' {
			k := strings.IndexAny(rest[1:], ".[")
			if k < 0 {
				k = len(rest) - 1
			}
			parts = append(parts, fieldPart{attr: true, key: rest[1 : 1+k]})
			rest = rest[1+k:]
			continue
		}
		k := strings.IndexByte(rest, ']')
		if k < 0 {
			k = len(rest)
		}
		key := rest[1:k]
		var kv Value = key
		if n, err := strconv.ParseInt(key, 10, 64); err == nil && key != "" && key[0] != '+' && key[0] != '-' {
			kv = n
		}
		parts = append(parts, fieldPart{key: kv})
		rest = rest[min(k+1, len(rest)):]
	}
	return first, parts
}

// The digits of an integer's magnitude in a base, decimal ones refused past CPython's digit limit
func baseDigits(n *big.Int, base int) (string, error) {
	abs := new(big.Int).Abs(n)
	if base == 10 {
		return decimalString(abs)
	}
	return abs.Text(base), nil
}

func isBigInt(v Value) bool {
	_, ok := v.(*big.Int)
	return ok
}

// The largest precision CPython formats, INT_MAX less its 1024 digit reserve, refused as it refuses them
func checkPrecision(p int) error {
	if p > math.MaxInt32-1024 {
		return errorf("ValueError", "precision too big")
	}
	return checkAlloc(p)
}

// The deepest frame string.Formatter.format_field reaches from its frame, object.__format__ calling str() and the escape formatter escaping
func formatFieldFrames(depth int, obj Value, spec string, escape bool) int {
	objectFormat := 0
	switch obj.(type) {
	case string, Markup, int64, bool, float64, complex128:
	default:
		if !isBigInt(obj) && spec == "" {
			objectFormat, _ = strWalk(obj)
		}
	}
	if !escape {
		return depth + objectFormat
	}
	if isMarkup(obj) {
		return depth + 1 + escapeFrames(obj)
	}
	return max(depth+hasattrFrames(obj), depth+1+objectFormat, depth+1+escapeFrames(""))
}
