package ingest

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/drilonrecica/siftail/internal/logs"
)

type DecoderLimits struct {
	MaxCompressedBytes   int64
	MaxDecompressedBytes int64
	MaxEventBytes        int64
	MaxEvents            int
	MaxJSONDepth         int
}

type JSONDecoder struct {
	limits DecoderLimits
}

func NewJSONDecoder(limits DecoderLimits) *JSONDecoder {
	if limits.MaxJSONDepth <= 0 {
		limits.MaxJSONDepth = 32
	}
	return &JSONDecoder{limits: limits}
}

func (d *JSONDecoder) Decode(ctx context.Context, request DecodeRequest) (DecodedBatch, error) {
	compressed := &capReader{reader: request.Body, remaining: d.limits.MaxCompressedBytes}
	var source io.Reader = compressed
	var gzipReader *gzip.Reader
	if request.Gzip {
		var err error
		gzipReader, err = gzip.NewReader(compressed)
		if err != nil {
			return DecodedBatch{}, &Error{Category: CategoryBadRequest}
		}
		defer gzipReader.Close()
		source = gzipReader
	}
	decompressed := &capReader{reader: source, remaining: d.limits.MaxDecompressedBytes}
	var records []json.RawMessage
	var err error
	switch request.MediaType {
	case "application/x-ndjson":
		records, err = d.decodeNDJSON(ctx, decompressed)
	case "application/json":
		records, err = d.decodeJSON(ctx, decompressed)
	default:
		return DecodedBatch{}, &Error{Category: CategoryBadRequest}
	}
	if err != nil {
		return DecodedBatch{}, safeDecodeError(err)
	}
	if len(records) == 0 {
		return DecodedBatch{}, &Error{Category: CategoryBadRequest}
	}

	batch := DecodedBatch{Events: make([]logs.CanonicalEvent, 0, len(records))}
	for index, raw := range records {
		if err := ctx.Err(); err != nil {
			return DecodedBatch{}, err
		}
		if int64(len(raw)) > d.limits.MaxEventBytes {
			return DecodedBatch{}, &Error{Category: CategoryTooLarge}
		}
		if err := logs.ValidateJSON(raw, d.limits.MaxJSONDepth); err != nil {
			return DecodedBatch{}, &Error{Category: CategoryBadRequest}
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return DecodedBatch{}, &Error{Category: CategoryBadRequest}
		}
		record := logs.ReceivedRecord{Fields: fields, Raw: append([]byte(nil), raw...)}
		if timestamp, ok := fields["date"]; ok {
			record.Timestamp = timestamp
		}
		if tag, ok := fields["tag"]; ok {
			_ = json.Unmarshal(tag, &record.Tag)
		}
		event, err := logs.Normalize(record, request.Server, request.ReceivedAt)
		if err != nil {
			_ = index // index is intentionally not included with payload data.
			if errors.Is(err, logs.ErrLimit) {
				return DecodedBatch{}, &Error{Category: CategoryTooLarge}
			}
			return DecodedBatch{}, &Error{Category: CategoryBadRequest}
		}
		batch.ApproxBytes += event.RetainedBytes()
		batch.Events = append(batch.Events, event)
	}
	return batch, nil
}

func (d *JSONDecoder) decodeNDJSON(ctx context.Context, reader io.Reader) ([]json.RawMessage, error) {
	scanner := bufio.NewScanner(reader)
	bufferSize := int(d.limits.MaxEventBytes + 1)
	if bufferSize < 64*1024 {
		bufferSize = 64 * 1024
	}
	scanner.Buffer(make([]byte, 64*1024), bufferSize)
	var records []json.RawMessage
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if int64(len(line)) > d.limits.MaxEventBytes {
			return nil, &Error{Category: CategoryTooLarge}
		}
		records = append(records, append([]byte(nil), line...))
		if len(records) > d.limits.MaxEvents {
			return nil, &Error{Category: CategoryTooLarge}
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			return nil, &Error{Category: CategoryTooLarge}
		}
		return nil, err
	}
	return records, nil
}

func (d *JSONDecoder) decodeJSON(ctx context.Context, reader io.Reader) ([]json.RawMessage, error) {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	first, err := readNonspace(buffered)
	if err != nil {
		return nil, err
	}
	if first == '{' {
		raw, err := readJSONObject(buffered, first, d.limits.MaxEventBytes)
		if err != nil {
			return nil, err
		}
		if err := requireWhitespaceEOF(buffered); err != nil {
			return nil, err
		}
		return []json.RawMessage{raw}, nil
	}
	if first != '[' {
		return nil, &Error{Category: CategoryBadRequest}
	}
	if err := buffered.UnreadByte(); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(buffered)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, &Error{Category: CategoryBadRequest}
	}
	var records []json.RawMessage
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		if int64(len(raw)) > d.limits.MaxEventBytes {
			return nil, &Error{Category: CategoryTooLarge}
		}
		if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
			return nil, &Error{Category: CategoryBadRequest}
		}
		records = append(records, raw)
		if len(records) > d.limits.MaxEvents {
			return nil, &Error{Category: CategoryTooLarge}
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') {
		return nil, &Error{Category: CategoryBadRequest}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, &Error{Category: CategoryBadRequest}
	}
	return records, nil
}

type capReader struct {
	reader    io.Reader
	remaining int64
}

func (r *capReader) Read(buffer []byte) (int, error) {
	if r.remaining < 0 {
		return 0, &Error{Category: CategoryTooLarge}
	}
	if int64(len(buffer)) > r.remaining+1 {
		buffer = buffer[:r.remaining+1]
	}
	n, err := r.reader.Read(buffer)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return n, &Error{Category: CategoryTooLarge}
	}
	return n, err
}

func readNonspace(reader *bufio.Reader) (byte, error) {
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		switch value {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return value, nil
		}
	}
}

func readJSONObject(reader *bufio.Reader, first byte, maxBytes int64) ([]byte, error) {
	raw := make([]byte, 0, min(int(maxBytes), 64*1024))
	raw = append(raw, first)
	depth := 1
	inString := false
	escaped := false
	for depth > 0 {
		value, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		raw = append(raw, value)
		if int64(len(raw)) > maxBytes {
			return nil, &Error{Category: CategoryTooLarge}
		}
		if inString {
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return raw, nil
}

func requireWhitespaceEOF(reader *bufio.Reader) error {
	for {
		value, err := reader.ReadByte()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch value {
		case ' ', '\t', '\r', '\n':
		default:
			return &Error{Category: CategoryBadRequest}
		}
	}
}

func safeDecodeError(err error) error {
	var ingestErr *Error
	if errors.As(err, &ingestErr) {
		return ingestErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &Error{Category: CategoryBadRequest}
}
