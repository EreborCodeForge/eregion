package dispatcher

import (
	"net/http"
	"unicode/utf8"
)

const maxLoggedBodyBytes = 8 * 1024

var sensitiveHeaders = map[string]struct{}{
	"Authorization":       {},
	"Cookie":              {},
	"Set-Cookie":          {},
	"Proxy-Authorization": {},
	"X-Api-Key":           {},
	"X-Auth-Token":        {},
}

func redactHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, vals := range h {
		if _, sens := sensitiveHeaders[http.CanonicalHeaderKey(k)]; sens {
			out[k] = []string{"[REDACTED]"}
			continue
		}
		cp := make([]string, len(vals))
		copy(cp, vals)
		out[k] = cp
	}
	return out
}

func truncateBodyForLog(body []byte) (logged string, omitted, truncated bool) {
	if len(body) == 0 {
		return "", false, false
	}
	if !utf8.Valid(body) || looksBinary(body) {
		return "", true, false
	}
	if len(body) > maxLoggedBodyBytes {
		return string(body[:maxLoggedBodyBytes]), false, true
	}
	return string(body), false, false
}

func looksBinary(b []byte) bool {
	n := len(b)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

func (d *Dispatcher) accessLog(attrs ...any) {
	if !d.cfg.Logging.AccessLog {
		return
	}
	d.logger.Info("access", attrs...)
}

func (d *Dispatcher) maybeLogExtras(r *http.Request, reqBody, respBody []byte) []any {
	var extras []any
	if d.cfg.Logging.IncludeRequestHeaders {
		extras = append(extras, "request_headers", redactHeaders(r.Header))
	}
	if d.cfg.Logging.IncludeRequestBody {
		s, omitted, truncated := truncateBodyForLog(reqBody)
		if omitted {
			extras = append(extras, "request_body_omitted", true)
		} else if s != "" {
			extras = append(extras, "request_body", s)
			if truncated {
				extras = append(extras, "request_body_truncated", true)
			}
		}
	}
	if d.cfg.Logging.IncludeResponseBody {
		s, omitted, truncated := truncateBodyForLog(respBody)
		if omitted {
			extras = append(extras, "response_body_omitted", true)
		} else if s != "" {
			extras = append(extras, "response_body", s)
			if truncated {
				extras = append(extras, "response_body_truncated", true)
			}
		}
	}
	return extras
}
