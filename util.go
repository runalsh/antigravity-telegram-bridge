package main

import (
	"strings"
)

func HTMLEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// chunkText splits on newlines keeping each part under limit; longer lines are
// hard-split. Never returns an empty slice.
func chunkText(s string, limit int) []string {
	if limit < 50 {
		limit = 50
	}
	if len(s) <= limit {
		return []string{s}
	}
	var out []string
	var buf strings.Builder
	for _, line := range strings.Split(s, "\n") {
		for len(line) > limit {
			if buf.Len() > 0 {
				out = append(out, buf.String())
				buf.Reset()
			}
			out = append(out, line[:limit])
			line = line[limit:]
		}
		if buf.Len()+len(line)+1 > limit {
			out = append(out, buf.String())
			buf.Reset()
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
