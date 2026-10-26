package store

import (
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// Agent log records are the bulk of the database (docs/storage-design.md).
// A record of at least minCompress bytes is stored zstd-compressed as a BLOB
// in raw_log_records.record; shorter ones stay TEXT. The hash chain covers the
// original text, so verification and every reader decode first (DecodeRecord).
const minCompress = 256

var (
	encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithWindowSize(1<<20))
	decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
)

// encodeRecord returns the value to store for a record: the text itself, or
// its compressed bytes when that is smaller.
func encodeRecord(s string) any {
	if len(s) < minCompress {
		return s
	}
	if c := encoder.EncodeAll([]byte(s), nil); len(c) < len(s) {
		return c
	}
	return s
}

// DecodeRecord turns a stored raw_log_records.record value (TEXT or BLOB, or
// NULL) back into the agent's original text.
func DecodeRecord(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case []byte:
		b, err := decoder.DecodeAll(x, nil)
		if err != nil {
			return "", fmt.Errorf("decompress record: %w", err)
		}
		return string(b), nil
	}
	return "", fmt.Errorf("record has unexpected type %T", v)
}
