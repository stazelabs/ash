package proto

import (
	"fmt"
	"sort"
	"strings"
)

// PrettyRequest renders a request in the canonical line-oriented form that
// the daemon tokenizes for tokens_in and that the client renders for echo.
//
// Example: `ash read --path src/foo.go --range 10:50`
func PrettyRequest(req *Request) string {
	var b strings.Builder
	b.WriteString("ash ")
	b.WriteString(req.Verb)
	keys := make([]string, 0, len(req.Args))
	for k := range req.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := req.Args[k]
		b.WriteByte(' ')
		b.WriteString("--")
		b.WriteString(k)
		b.WriteByte(' ')
		b.WriteString(fmt.Sprintf("%v", v))
	}
	return b.String()
}

// PrettyResponseHeader renders the metadata line that prefixes a verb-specific
// pretty body. Verbs append their own body afterward.
func PrettyResponseHeader(rsp *Response) string {
	if !rsp.OK {
		if rsp.Err == nil {
			return "err"
		}
		return fmt.Sprintf("err %s\n%s", rsp.Err.Code, rsp.Err.Msg)
	}
	return "ok"
}
