package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const (
	remoteWriteBodyLimit    = 32 << 20
	remoteWriteDecodedLimit = 64 << 20
	remoteWriteMaxSamples   = 100000
)

// remoteWrite accepts Prometheus remote_write v1 requests. Pull scraping and
// push receivers share the same metric store and ingest role policy.
func (s *Server) remoteWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(w, http.StatusMethodNotAllowed, errors.New("remote write endpoint accepts POST only"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, remoteWriteBodyLimit))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, errors.New("remote write request exceeds 32 MiB"))
		return
	}
	raw := body
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "snappy":
		decoded, decodeErr := snappy.Decode(nil, body)
		if decodeErr != nil {
			fail(w, http.StatusBadRequest, errors.New("invalid snappy remote write body"))
			return
		}
		raw = decoded
	default:
		fail(w, http.StatusUnsupportedMediaType, fmt.Errorf("unsupported remote write Content-Encoding %q", r.Header.Get("Content-Encoding")))
		return
	}
	if len(raw) > remoteWriteDecodedLimit {
		fail(w, http.StatusRequestEntityTooLarge, errors.New("remote write body exceeds 64 MiB after decoding"))
		return
	}
	var request prompb.WriteRequest
	if err := proto.Unmarshal(raw, &request); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("invalid remote write protobuf: %w", err))
		return
	}
	samples := remoteWriteSamples(request.Timeseries)
	if len(samples) > remoteWriteMaxSamples {
		fail(w, http.StatusRequestEntityTooLarge, fmt.Errorf("remote write request has %d samples; limit is %d", len(samples), remoteWriteMaxSamples))
		return
	}
	for offset := 0; offset < len(samples); offset += 10000 {
		end := min(offset+10000, len(samples))
		if err := s.Store.Ingest(r.Context(), samples[offset:end]); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	}
	w.Header().Set("X-Accepted-Samples", strconv.Itoa(len(samples)))
	w.WriteHeader(http.StatusNoContent)
}

func remoteWriteSamples(series []prompb.TimeSeries) []model.Sample {
	samples := make([]model.Sample, 0, len(series))
	for _, item := range series {
		name := ""
		labels := make(map[string]string, len(item.Labels))
		for _, label := range item.Labels {
			if label.Name == "__name__" {
				name = label.Value
				continue
			}
			labels[label.Name] = label.Value
		}
		if name == "" {
			continue
		}
		for _, sample := range item.Samples {
			samples = append(samples, model.Sample{
				Name:      name,
				Labels:    labels,
				Value:     sample.Value,
				Timestamp: sample.Timestamp,
			})
		}
	}
	return samples
}
