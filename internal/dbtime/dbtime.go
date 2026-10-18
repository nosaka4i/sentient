// Package dbtime is the timestamp format stored in the database. It is
// separate from internal/store so that readers (query, and through it the
// sen binary) don't link the SQLite driver.
package dbtime

import "time"

// Format formats t as stored: UTC RFC 3339 with fixed nanoseconds, so
// lexical order is time order.
func Format(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// Parse parses a stored timestamp.
func Parse(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
