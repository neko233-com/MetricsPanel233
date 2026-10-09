package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// queryDatasource uses the public Grafana connection query API. Streaming keeps
// the original refId/frameId records so agents can append each frame themselves.
func (c apiClient) queryDatasource(ctx context.Context, uid string, input []byte, stream bool, output io.Writer) error {
	if len(input) > 4<<20 || !json.Valid(input) {
		return errors.New("query input must be JSON under 4 MiB")
	}
	raw, err := c.call("GET", "/api/datasources/uid/"+url.PathEscape(uid), nil)
	if err != nil {
		return err
	}
	var source struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &source); err != nil || source.Type == "" {
		return errors.New("datasource response omitted its plugin type")
	}
	path := "/apis/" + url.PathEscape(source.Type+".datasource.grafana.app") + "/v0alpha1/namespaces/default/connections/" + url.PathEscape(uid) + "/query"
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+path, bytes.NewReader(input))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if stream {
		request.Header.Set("Accept", "text/jsonl")
	}
	response, err := (&http.Client{Timeout: 75 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	encoder := json.NewEncoder(output)
	if !stream {
		body, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
		if err != nil {
			return err
		}
		if len(body) > 32<<20 || !json.Valid(body) {
			return errors.New("server returned invalid or oversized query JSON")
		}
		encoder.SetIndent("", "  ")
		return encoder.Encode(json.RawMessage(body))
	}
	if strings.Split(response.Header.Get("Content-Type"), ";")[0] != "text/jsonl" {
		return errors.New("server did not return Grafana text/jsonl")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			return newline + 1, data[:newline], nil
		}
		if atEOF && len(data) > 0 {
			return 0, nil, errors.New("query stream ended with a truncated line")
		}
		return 0, nil, nil
	})
	scanner.Buffer(make([]byte, 64<<10), (8<<20)+2)
	refs := map[string]bool{}
	total := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		total += len(line) + 1
		if len(line) > 8<<20 || total > 32<<20 {
			return errors.New("query stream exceeds 8 MiB per chunk or 32 MiB per request")
		}
		var record struct {
			RefID string          `json:"refId"`
			Frame json.RawMessage `json:"frame"`
			Error string          `json:"error"`
		}
		if err := json.Unmarshal(line, &record); err != nil || record.RefID == "" || (len(record.Frame) == 0 && record.Error == "") {
			return errors.New("server returned an invalid query chunk")
		}
		if record.Error != "" {
			refs[record.RefID] = true
		}
		if err := encoder.Encode(json.RawMessage(line)); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("%d datasource queries failed; see error records on stdout", len(refs))
	}
	return nil
}
