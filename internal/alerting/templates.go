package alerting

import (
	"context"
	"maps"
	"net/url"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/alerttemplates"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type ruleTemplates struct {
	labels, annotations alerttemplates.Set
	rule                model.AlertRule
	root                *url.URL
	ctx                 context.Context
	at                  time.Time
}

func newRuleTemplates(ctx context.Context, rule model.AlertRule, root *url.URL, at int64) ruleTemplates {
	return ruleTemplates{labels: alerttemplates.CompileSet(rule.UID, rule.Labels), annotations: alerttemplates.CompileSet(rule.UID, rule.Annotations), rule: rule, root: root, ctx: ctx, at: time.UnixMilli(at)}
}
func (t ruleTemplates) data(value *Value, source map[string]string) alerttemplates.Data {
	labels := maps.Clone(source)
	if labels == nil {
		labels = map[string]string{}
	}
	delete(labels, "__name__")
	// Pinned Grafana expansion includes evaluation labels and reserved context,
	// while configured rule labels are expanded separately and cannot refer to each other.
	labels["alertname"] = t.rule.Title
	labels["grafana_folder"] = t.rule.FolderUID
	var captures map[string]alerttemplates.Capture
	evaluation := ""
	if value != nil {
		captures, evaluation = value.Captures, value.EvaluationString
		if captures == nil && !value.Missing && t.rule.Execution != "grafana" {
			n := value.Value
			captures = map[string]alerttemplates.Capture{"A": {Labels: maps.Clone(source), Value: &n, Datasource: true, Type: "query"}}
		}
	}
	return alerttemplates.NewData(labels, captures, evaluation)
}
func (t ruleTemplates) render(value *Value, source map[string]string) (map[string]string, map[string]string, []string) {
	data := t.data(value, source)
	labels, labelErrors := t.labels.Expand(t.ctx, data, t.root, t.at, 512)
	annotations, annotationErrors := t.annotations.Expand(t.ctx, data, t.root, t.at, 4096)
	return labels, annotations, append(labelErrors, annotationErrors...)
}
func (t ruleTemplates) annotationsFor(source map[string]string) (map[string]string, []string) {
	labels := maps.Clone(source)
	for key := range t.rule.Labels {
		delete(labels, key)
	}
	return t.annotations.Expand(t.ctx, t.data(nil, labels), t.root, t.at, 4096)
}
