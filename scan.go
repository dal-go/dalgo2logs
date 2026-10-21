package dalgo2logs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

type row struct {
	ID   string
	Data map[string]any
}
type entry struct {
	raw         []byte
	start, end  int64
	first, last int
	data        map[string]any
}

var textHead = regexp.MustCompile(`^(\S+)\s+(TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL)\s+([A-Za-z0-9_.-]+)\s+(.*)$`)
var kvToken = regexp.MustCompile(`(?:^|\s)(correlation_id|trace_id|order_id)=([^\s]+)`)

func (d *database) scan(ctx context.Context, s source, keep func(map[string]any) bool, postReferenceFilter bool) ([]row, error) {
	if err := noSymlink(s.path); err != nil {
		return nil, errors.New("dalgo2logs: source path is no longer allowed")
	}
	f, err := d.root.Open(s.Path)
	if err != nil {
		return nil, errors.New("dalgo2logs: source is unavailable")
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > d.cfg.MaxFileBytes {
		return nil, errors.New("dalgo2logs: source is not a bounded regular file")
	}
	reader := bufio.NewReaderSize(f, d.cfg.MaxLineBytes+1)
	h := sha256.New()
	var entries []entry
	var pending *entry
	entryCount := 0
	var offset int64
	line := 0
	finish := func() error {
		if pending == nil {
			return nil
		}
		pending.data = parseText(pending.raw)
		entryCount++
		if keep == nil || postReferenceFilter || keep(pending.data) {
			entries = append(entries, *pending)
		}
		pending = nil
		if entryCount > d.cfg.MaxEntries {
			return errors.New("dalgo2logs: entry limit exceeded")
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, readErr := reader.ReadSlice('\n')
		if readErr == bufio.ErrBufferFull {
			return nil, errors.New("dalgo2logs: line limit exceeded")
		}
		if len(chunk) == 0 && readErr == io.EOF {
			break
		}
		if len(chunk) > d.cfg.MaxLineBytes {
			return nil, errors.New("dalgo2logs: line limit exceeded")
		}
		if readErr != nil && readErr != io.EOF {
			return nil, errors.New("dalgo2logs: file read failed")
		}
		if offset+int64(len(chunk)) > d.cfg.MaxFileBytes {
			return nil, errors.New("dalgo2logs: file limit exceeded")
		}
		_, _ = h.Write(chunk)
		line++
		start := offset
		offset += int64(len(chunk))
		if s.Format == JSONL {
			entryCount++
			if entryCount > d.cfg.MaxEntries {
				return nil, errors.New("dalgo2logs: entry limit exceeded")
			}
			data := parseJSONL(chunk, s.TimestampField)
			if keep == nil || postReferenceFilter || keep(data) {
				entries = append(entries, entry{raw: bytes.Clone(chunk), start: start, end: offset, first: line, last: line, data: data})
			}
		} else if pending != nil && (chunk[0] == ' ' || chunk[0] == '\t') {
			if len(pending.raw)+len(chunk) > d.cfg.MaxLineBytes {
				return nil, errors.New("dalgo2logs: multiline entry limit exceeded")
			}
			pending.raw = append(pending.raw, chunk...)
			pending.end = offset
			pending.last = line
		} else {
			if err := finish(); err != nil {
				return nil, err
			}
			pending = &entry{raw: bytes.Clone(chunk), start: start, end: offset, first: line, last: line}
		}
		if readErr == io.EOF {
			break
		}
	}
	if err := finish(); err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("dalgo2logs: source changed during scan")
	}
	if err := noSymlink(s.path); err != nil {
		return nil, errors.New("dalgo2logs: source path changed during scan")
	}
	pathInfo, err := os.Stat(s.path)
	if err != nil || !os.SameFile(before, pathInfo) {
		return nil, errors.New("dalgo2logs: source rotated during scan")
	}
	digest := hex.EncodeToString(h.Sum(nil))
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ref := fmt.Sprintf("%s:%s:%s:%d:%d", d.cfg.ID, s.Collection, digest, e.start, e.end)
		idSum := sha256.Sum256([]byte(ref))
		e.data["source_ref"] = ref
		e.data["source"] = s.Collection
		e.data["parser"] = string(s.Format)
		e.data["line_start"] = e.first
		e.data["line_end"] = e.last
		e.data["byte_offset"] = e.start
		e.data["raw"] = string(e.raw)
		if keep == nil || !postReferenceFilter || keep(e.data) {
			rows = append(rows, row{ID: hex.EncodeToString(idSum[:]), Data: e.data})
		}
	}
	return rows, nil
}

func parseJSONL(raw []byte, timestampField string) map[string]any {
	var obj map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&obj); err != nil || obj == nil {
		return map[string]any{"parse_status": "malformed", "message": "Unparsed JSONL entry"}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return map[string]any{"parse_status": "malformed", "message": "Unparsed JSONL entry"}
	}
	data := map[string]any{"parse_status": "parsed"}
	copyValue := func(target string, keys ...string) {
		for _, key := range keys {
			if v, ok := obj[key]; ok && v != nil {
				data[target] = fmt.Sprint(v)
				return
			}
		}
	}
	copyValue("severity", "severity", "level")
	copyValue("message", "message", "msg")
	copyValue("service", "service")
	copyValue("correlation_id", "correlation_id", "trace_id")
	copyValue("order_id", "order_id", "orderId")
	stamp, found := "", false
	keys := []string{"timestamp", "time"}
	if timestampField != "" {
		keys = []string{timestampField}
	}
	for _, key := range keys {
		if v, ok := obj[key]; ok && v != nil {
			found = true
			if timestampField == "" {
				stamp = fmt.Sprint(v)
			} else {
				stamp, _ = v.(string)
			}
			break
		}
	}
	if !found || stamp == "" && timestampField == "" {
		data["parse_status"] = "missing_timestamp"
	} else if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		data["timestamp"] = t.UTC().Format(time.RFC3339Nano)
	} else {
		data["parse_status"] = "invalid_timestamp"
	}
	if severity, ok := data["severity"].(string); ok {
		data["severity"] = strings.ToUpper(severity)
	}
	if _, ok := data["message"]; !ok {
		data["message"] = "Structured entry without message"
	}
	fields, _ := json.Marshal(obj)
	data["fields_json"] = string(fields)
	return data
}

func parseText(raw []byte) map[string]any {
	first, _, _ := strings.Cut(strings.TrimRight(string(raw), "\r\n"), "\n")
	parts := textHead.FindStringSubmatch(strings.TrimSuffix(first, "\r"))
	if parts == nil {
		return map[string]any{"parse_status": "malformed", "message": "Unparsed text entry"}
	}
	data := map[string]any{"parse_status": "parsed", "severity": strings.ToUpper(parts[2]), "service": parts[3], "message": strings.TrimSpace(parts[4])}
	if stamp, err := time.Parse(time.RFC3339Nano, parts[1]); err == nil {
		data["timestamp"] = stamp.UTC().Format(time.RFC3339Nano)
	} else {
		data["parse_status"] = "invalid_timestamp"
	}
	for _, match := range kvToken.FindAllStringSubmatch(parts[4], -1) {
		key := match[1]
		if key == "trace_id" {
			key = "correlation_id"
		}
		data[key] = match[2]
	}
	if idx := bytes.IndexByte(raw, '\n'); idx >= 0 && idx+1 < len(raw) {
		data["message"] = strings.TrimSpace(parts[4]) + "\n" + strings.TrimRight(string(raw[idx+1:]), "\r\n")
	}
	return data
}
