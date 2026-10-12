// Package notificationtemplate provides bounded notification rendering using
// Alertmanager's functions and Grafana's documented notification data shape.
package notificationtemplate

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	amtemplate "github.com/prometheus/alertmanager/template"
)

type KV = amtemplate.KV
type Alert struct {
	Status       string             `json:"status"`
	Labels       KV                 `json:"labels"`
	Annotations  KV                 `json:"annotations"`
	StartsAt     time.Time          `json:"startsAt"`
	EndsAt       time.Time          `json:"endsAt"`
	GeneratorURL string             `json:"generatorURL"`
	Fingerprint  string             `json:"fingerprint"`
	SilenceURL   string             `json:"silenceURL"`
	DashboardURL string             `json:"dashboardURL"`
	PanelURL     string             `json:"panelURL"`
	RuleUID      string             `json:"ruleUID,omitempty"`
	Values       map[string]float64 `json:"values"`
	ValueString  string             `json:"valueString"`
	OrgID        *int64             `json:"orgId,omitempty"`
}
type Alerts []Alert

func (a Alerts) Firing() Alerts {
	out := Alerts{}
	for _, v := range a {
		if v.Status == "firing" {
			out = append(out, v)
		}
	}
	return out
}
func (a Alerts) Resolved() Alerts {
	out := Alerts{}
	for _, v := range a {
		if v.Status == "resolved" {
			out = append(out, v)
		}
	}
	return out
}
func (a Alerts) Len() int { return len(a) }

type Data struct {
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	Alerts            Alerts            `json:"alerts"`
	GroupLabels       KV                `json:"groupLabels"`
	CommonLabels      KV                `json:"commonLabels"`
	CommonAnnotations KV                `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	AppVersion        string            `json:"appVersion,omitempty"`
	Vars              map[string]string `json:"vars,omitempty"`
	RouteLabels       KV                `json:"routeLabels,omitempty"`
}

func publicKV(values KV) KV {
	out := KV{}
	for k, v := range values {
		if strings.HasPrefix(k, "__") && strings.HasSuffix(k, "__") {
			continue
		}
		out[k] = v
	}
	return out
}

func (d *Data) Normalize(at time.Time) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if len(raw) > 1048576 || len(d.Alerts) > 1000 {
		return errors.New("notification template data exceeds 1 MiB or 1000 alerts")
	}
	if len(d.GroupLabels) > 128 || len(d.CommonLabels) > 128 || len(d.CommonAnnotations) > 128 || len(d.Vars) > 128 || len(d.RouteLabels) > 128 {
		return errors.New("template data supports at most 128 entries per label or variable map")
	}
	if d.Status == "test" {
		d.Status = "firing"
	}
	if d.Status != "firing" && d.Status != "resolved" {
		return errors.New("notification template status must be firing or resolved")
	}
	d.GroupLabels = publicKV(d.GroupLabels)
	d.CommonLabels = publicKV(d.CommonLabels)
	d.CommonAnnotations = publicKV(d.CommonAnnotations)
	if d.Vars == nil {
		d.Vars = map[string]string{}
	}
	if d.RouteLabels == nil {
		d.RouteLabels = KV{}
	}
	for i := range d.Alerts {
		a := &d.Alerts[i]
		if a.Status == "test" {
			a.Status = "firing"
		}
		if a.Status != "firing" && a.Status != "resolved" {
			return errors.New("template alert status must be firing or resolved")
		}
		if len(a.Labels) > 128 || len(a.Annotations) > 128 || len(a.Values) > 128 {
			return errors.New("template alert supports at most 128 labels, annotations and values")
		}
		if a.RuleUID == "" {
			a.RuleUID = a.Labels["__alert_rule_uid__"]
		}
		dashboard, panel := a.Annotations["__dashboardUid__"], a.Annotations["__panelId__"]
		if a.OrgID == nil {
			value := int64(1)
			if raw := a.Annotations["__orgId__"]; raw != "" {
				if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
					value = parsed
				}
			}
			a.OrgID = &value
		}
		if a.Values == nil {
			a.Values = map[string]float64{}
		}
		if a.ValueString == "" {
			a.ValueString = a.Annotations["__value_string__"]
		}
		if d.ExternalURL != "" {
			base, err := url.Parse(d.ExternalURL)
			if err == nil && (base.Scheme == "http" || base.Scheme == "https") && base.User == nil {
				if dashboard != "" {
					target := *base.JoinPath("d", dashboard)
					target.Fragment = ""
					q := url.Values{}
					if !a.StartsAt.IsZero() {
						q.Set("from", strconv.FormatInt(a.StartsAt.Add(-time.Hour).UnixMilli(), 10))
						to := at
						if !a.EndsAt.IsZero() {
							to = a.EndsAt
						}
						q.Set("to", strconv.FormatInt(to.UnixMilli(), 10))
					}
					target.RawQuery = q.Encode()
					if a.DashboardURL == "" {
						a.DashboardURL = target.String()
					}
					if panel != "" && a.PanelURL == "" {
						q.Set("viewPanel", panel)
						target.RawQuery = q.Encode()
						a.PanelURL = target.String()
					}
				}
				if a.SilenceURL == "" {
					target := *base
					target.Fragment = "alerts"
					a.SilenceURL = target.String()
				}
			}
		}
		a.Labels = publicKV(a.Labels)
		a.Annotations = publicKV(a.Annotations)
	}
	return nil
}

func DataFromPayload(payload map[string]any, at time.Time) (Data, error) {
	var data Data
	raw, err := json.Marshal(payload)
	if err != nil {
		return data, err
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		return data, err
	}
	return data, data.Normalize(at)
}
