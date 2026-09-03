package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const factsWidth = 72

// Prints JSON when requested, else the table renderer
func (e *env) print(msg proto.Message, render func(w io.Writer)) error {
	if e.json {
		data, err := protojson.MarshalOptions{Multiline: true, Indent: "  ", UseProtoNames: true}.Marshal(msg)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(e.out, string(data))
		return err
	}
	if render != nil {
		render(e.out)
	}
	return nil
}

func table(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n", strings.ToUpper(title))
}

func compact(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	s := strings.Join(parts, " ")
	if len(s) > factsWidth {
		return s[:factsWidth-1] + "…"
	}
	return s
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
