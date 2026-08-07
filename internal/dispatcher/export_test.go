package dispatcher

import "net/http"

// RedactHeadersForTest exports redactHeaders for unit tests.
func RedactHeadersForTest(h http.Header) map[string][]string {
	return redactHeaders(h)
}

// TruncateBodyForTest exports truncateBodyForLog for unit tests.
func TruncateBodyForTest(body []byte) (string, bool, bool) {
	return truncateBodyForLog(body)
}
