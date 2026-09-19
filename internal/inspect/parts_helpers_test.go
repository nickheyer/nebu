package inspect

import (
	"bytes"
	"time"
)

var timeZero time.Time

func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }
