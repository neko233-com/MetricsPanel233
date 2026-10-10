package alerttemplates

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

func selectLabels(labels Labels, pattern string, regex, remove bool) (Labels, error) {
	var matcher *regexp.Regexp
	var err error
	if regex {
		matcher, err = regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
	}
	result := Labels{}
	for key, value := range labels {
		match := key == pattern
		if regex {
			match = matcher.MatchString(key)
		}
		if match != remove {
			result[key] = value
		}
	}
	return result, nil
}

func exploreLink(raw string, table bool) string {
	var query struct {
		Datasource string `json:"datasource"`
		Expr       string `json:"expr"`
	}
	if json.Unmarshal([]byte(raw), &query) != nil {
		return ""
	}
	ds, expr := url.QueryEscape(query.Datasource), url.QueryEscape(query.Expr)
	return fmt.Sprintf(`/explore?left={"datasource":%q,"queries":[{"datasource":%q,"expr":%q,"instant":%t,"range":%t,"refId":"A"}],"range":{"from":"now-1h","to":"now"}}`, ds, ds, expr, table, !table)
}

func mergeValues(values map[string]Value) Labels {
	sets := map[string]map[string]bool{}
	for _, value := range values {
		for key, label := range value.Labels {
			if sets[key] == nil {
				sets[key] = map[string]bool{}
			}
			sets[key][label] = true
		}
	}
	result := Labels{}
	for key, set := range sets {
		values := make([]string, 0, len(set))
		for value := range set {
			values = append(values, value)
		}
		slices.Sort(values)
		result[key] = strings.Join(values, ", ")
	}
	return result
}

func functions() template.FuncMap {
	return template.FuncMap{
		"filterLabels":     func(ls Labels, key string) (Labels, error) { return selectLabels(ls, key, false, false) },
		"filterLabelsRe":   func(ls Labels, re string) (Labels, error) { return selectLabels(ls, re, true, false) },
		"removeLabels":     func(ls Labels, key string) (Labels, error) { return selectLabels(ls, key, false, true) },
		"removeLabelsRe":   func(ls Labels, re string) (Labels, error) { return selectLabels(ls, re, true, true) },
		"mergeLabelValues": mergeValues,
		"graphLink":        func(raw string) string { return exploreLink(raw, false) },
		"tableLink":        func(raw string) string { return exploreLink(raw, true) },
	}
}
