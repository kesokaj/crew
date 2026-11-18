package gitendpoint

import (
	"errors"
	"strings"
)

// Redact removes a header's value from an error that git's output may have repeated, and the start
// of it that ends the error, where the kept error text cut git's output short (since 1.04; here, for
// the endpoint's fetch and the store's, since 1.05).
func Redact(err error, header string) error {
	if header == "" || err == nil {
		return err
	}
	_, v, _ := strings.Cut(header, ":")
	if v = strings.TrimSpace(v); v == "" {
		return err
	}
	text := strings.ReplaceAll(err.Error(), v, "[redacted]")
	for n := len(v) - 1; n > 0; n-- {
		if strings.HasSuffix(text, v[:n]) {
			text = text[:len(text)-n] + "[redacted]"
			break
		}
	}
	return errors.New(text)
}
