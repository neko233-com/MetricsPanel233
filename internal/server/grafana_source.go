package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/rand/v2"
	"mime"
	"path"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	webassets "github.com/neko233-com/MetricsPanel233/web"
)

func builtinGrafanaDataSource() model.DataSource {
	return model.DataSource{ID: -1, UID: "grafana", OrgID: 1, Name: "-- Grafana --", Type: "grafana", Access: "proxy", ReadOnly: true, JSONData: json.RawMessage(`{}`), SecureJSONFields: map[string]bool{}, Version: 1}
}

// Like Grafana's core backend, list and randomWalk are server queries; snapshot,
// measurements, annotations and timeRegions execute through the frontend SDK.
func (s *Server) queryGrafanaSource(ctx context.Context, query backend.DataQuery) (data.Frames, error) {
	switch query.QueryType {
	case "list":
		var input struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(query.JSON, &input); err != nil {
			return nil, err
		}
		folder := strings.TrimSuffix(input.Path, "/")
		if folder == "" {
			folder = "."
		}
		if !fs.ValidPath(folder) || strings.ContainsAny(folder, "\\:\x00") {
			return nil, errors.New("list path must be a relative public asset folder")
		}
		entries, err := fs.ReadDir(webassets.Files, folder)
		if err != nil {
			return nil, err
		}
		limit := min(int(query.MaxDataPoints), 10000)
		if limit <= 0 {
			limit = 500
		}
		names, types := []string{}, []string{}
		sizes := []int64{}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(names) == limit {
				break
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			media := mime.TypeByExtension(path.Ext(entry.Name()))
			if entry.IsDir() {
				media = "inode/directory"
			} else if media == "" {
				media = "application/octet-stream"
			}
			names = append(names, entry.Name())
			types = append(types, media)
			sizes = append(sizes, info.Size())
		}
		frame := data.NewFrame("Files", data.NewField("name", nil, names), data.NewField("mediaType", nil, types), data.NewField("size", nil, sizes))
		frame.RefID = query.RefID
		return data.Frames{frame}, nil
	case "randomWalk", "":
		var input struct {
			SeriesCount *int     `json:"seriesCount"`
			Start       *float64 `json:"startValue"`
			Min         *float64 `json:"min"`
			Max         *float64 `json:"max"`
			Spread      *float64 `json:"spread"`
			Noise       *float64 `json:"noise"`
			Drop        *float64 `json:"dropPercent"`
		}
		if err := json.Unmarshal(query.JSON, &input); err != nil {
			return nil, err
		}
		count := 1
		if input.SeriesCount != nil {
			count = *input.SeriesCount
		}
		if count < 0 || count > 128 || query.Interval <= 0 || query.TimeRange.To.Before(query.TimeRange.From) {
			return nil, errors.New("invalid random walk configuration")
		}
		for _, v := range []*float64{input.Start, input.Min, input.Max, input.Spread, input.Noise, input.Drop} {
			if v != nil && (math.IsInf(*v, 0) || math.IsNaN(*v)) {
				return nil, errors.New("invalid random walk configuration")
			}
		}
		if input.Min != nil && input.Max != nil && *input.Min > *input.Max {
			return nil, errors.New("invalid random walk configuration")
		}
		points := int(min(int64(10000), int64(math.Ceil(float64(query.TimeRange.To.Sub(query.TimeRange.From))/float64(query.Interval)))))
		if count*points > 1000000 {
			return nil, errors.New("random walk exceeds 1000000 generated rows")
		}
		start, spread, noise, drop := rand.Float64()*100, 1.0, 0.0, 0.0
		if input.Start != nil {
			start = *input.Start
		}
		if input.Spread != nil {
			spread = *input.Spread
		}
		if input.Noise != nil {
			noise = *input.Noise
		}
		if input.Drop != nil {
			drop = *input.Drop / 100
		}
		frames := make(data.Frames, 0, count)
		for series := 0; series < count; series++ {
			walker := start
			times, values := make([]time.Time, 0, points), make([]float64, 0, points)
			for index := 0; index < points; index++ {
				if index%256 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				sample := walker + rand.Float64()*noise
				if input.Min != nil && sample < *input.Min {
					sample, walker = *input.Min, *input.Min
				}
				if input.Max != nil && sample > *input.Max {
					sample, walker = *input.Max, *input.Max
				}
				if math.IsInf(sample, 0) || math.IsNaN(sample) {
					return nil, errors.New("random walk produced a non-finite value")
				}
				if drop <= 0 || rand.Float64() >= drop {
					times = append(times, query.TimeRange.From.Add(time.Duration(index)*query.Interval))
					values = append(values, sample)
				}
				walker += (rand.Float64() - 0.5) * spread
			}
			suffix := ""
			if series > 0 {
				suffix = fmt.Sprint(series)
			}
			clock := data.NewField("time", nil, times)
			clock.Config = &data.FieldConfig{Interval: float64(query.Interval.Milliseconds())}
			frame := data.NewFrame("", clock, data.NewField(query.RefID+"-series"+suffix, nil, values))
			frame.RefID = query.RefID
			frame.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesMulti}
			frames = append(frames, frame)
		}
		return frames, nil
	default:
		return nil, errors.New("unknown Grafana backend query type; frontend SDK handles snapshot, annotations, timeRegions and measurements")
	}
}
