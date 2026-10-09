package collector

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	prommodel "github.com/prometheus/common/model"
)

// ParsePrometheus accepts the Prometheus 0.0.4 text exposition format, including
// classic histograms and summaries. Target labels win over exporter labels.
func ParsePrometheus(r io.Reader, target model.Target) ([]model.Sample, error) {
	parser := expfmt.NewTextParser(prommodel.LegacyValidation)
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	out := []model.Sample{}
	for name, family := range families {
		for _, m := range family.Metric {
			labels := map[string]string{}
			for _, p := range m.Label {
				if _, exists := labels[p.GetName()]; exists {
					return nil, fmt.Errorf("duplicate label %q", p.GetName())
				}
				labels[p.GetName()] = p.GetValue()
			}
			labels["job"] = target.Name
			labels["instance"] = target.URL
			for k, v := range target.Labels {
				labels[k] = v
			}
			ts := now
			if m.TimestampMs != nil {
				ts = m.GetTimestampMs()
			}
			add := func(metric string, value float64, key, val string) {
				if math.IsNaN(value) || math.IsInf(value, 0) {
					return
				}
				copyLabels := map[string]string{}
				for k, v := range labels {
					copyLabels[k] = v
				}
				if key != "" {
					copyLabels[key] = val
				}
				out = append(out, model.Sample{Name: metric, Labels: copyLabels, Value: value, Timestamp: ts})
			}
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				add(name, m.GetCounter().GetValue(), "", "")
			case dto.MetricType_GAUGE:
				add(name, m.GetGauge().GetValue(), "", "")
			case dto.MetricType_UNTYPED:
				add(name, m.GetUntyped().GetValue(), "", "")
			case dto.MetricType_SUMMARY:
				s := m.GetSummary()
				add(name+"_sum", s.GetSampleSum(), "", "")
				add(name+"_count", float64(s.GetSampleCount()), "", "")
				for _, q := range s.Quantile {
					add(name, q.GetValue(), "quantile", strconv.FormatFloat(q.GetQuantile(), 'g', -1, 64))
				}
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				add(name+"_sum", h.GetSampleSum(), "", "")
				add(name+"_count", float64(h.GetSampleCount()), "", "")
				hasInf := false
				for _, b := range h.Bucket {
					if math.IsInf(b.GetUpperBound(), 1) {
						hasInf = true
					}
					add(name+"_bucket", float64(b.GetCumulativeCount()), "le", strconv.FormatFloat(b.GetUpperBound(), 'g', -1, 64))
				}
				if !hasInf {
					add(name+"_bucket", float64(h.GetSampleCount()), "le", "+Inf")
				}
			default:
				return nil, fmt.Errorf("unsupported metric family type for %s", name)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("exporter returned no finite samples")
	}
	if len(out) > 10000 {
		return nil, fmt.Errorf("exporter exceeds 10000 samples per scrape")
	}
	return out, nil
}
