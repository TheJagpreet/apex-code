package sse

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

type Event struct {
	Type string
	Data []byte
}

type Reader struct {
	scanner *bufio.Scanner
}

func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	return &Reader{scanner: sc}
}

func (r *Reader) Next() (Event, error) {
	var (
		eventType string
		dataLines []string
		haveData  bool
	)

	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			if eventType == "" && !haveData {
				continue
			}
			return Event{Type: eventType, Data: []byte(strings.Join(dataLines, "\n"))}, nil
		}

		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			eventType = value
		case "data":
			haveData = true
			dataLines = append(dataLines, value)
		}
	}

	if err := r.scanner.Err(); err != nil {
		return Event{}, fmt.Errorf("read sse: %w", err)
	}
	if eventType != "" || haveData {
		return Event{Type: eventType, Data: []byte(strings.Join(dataLines, "\n"))}, nil
	}
	return Event{}, io.EOF
}

func IsDoneMarker(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]"))
}
