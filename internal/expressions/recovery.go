package expressions

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

type thresholdEvaluator struct {
	Type   string    `json:"type"`
	Params []float64 `json:"params"`
}

func validateThreshold(e thresholdEvaluator) error {
	arity := 1
	switch e.Type {
	case "gt", "lt", "eq", "ne", "gte", "lte":
	case "within_range", "outside_range", "within_range_included", "outside_range_included":
		arity = 2
	default:
		return fmt.Errorf("unknown threshold evaluator %q", e.Type)
	}
	if len(e.Params) < arity {
		return fmt.Errorf("threshold %s needs at least %d parameters", e.Type, arity)
	}
	for _, n := range e.Params {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return errors.New("threshold parameters must be finite")
		}
	}
	return nil
}

func compareThreshold(e thresholdEvaluator, x float64) bool {
	a := e.Params[0]
	switch e.Type {
	case "gt":
		return x > a
	case "lt":
		return x < a
	case "eq":
		return x == a
	case "ne":
		return x != a
	case "gte":
		return x >= a
	case "lte":
		return x <= a
	case "within_range":
		return x > a && x < e.Params[1]
	case "outside_range":
		return x < a || x > e.Params[1]
	case "within_range_included":
		return x >= a && x <= e.Params[1]
	case "outside_range_included":
		return x <= a || x >= e.Params[1]
	}
	panic("unvalidated threshold")
}

func (o *operation) compileThreshold() error {
	if len(o.model.Conditions) != 1 {
		return errors.New("threshold needs one evaluator")
	}
	c := o.model.Conditions[0]
	if err := validateThreshold(c.Evaluator); err != nil {
		return err
	}
	if raw := strings.TrimSpace(string(c.UnloadEvaluator)); raw == "" || raw == "null" {
		return nil
	}
	var unload thresholdEvaluator
	if err := json.Unmarshal(c.UnloadEvaluator, &unload); err != nil {
		return fmt.Errorf("recovery threshold: %w", err)
	}
	if err := validateThreshold(unload); err != nil {
		return fmt.Errorf("recovery threshold: %w", err)
	}
	o.unload = &unload
	o.loaded = map[data.Fingerprint]struct{}{}
	if c.LoadedFingerprints != nil {
		if len(c.LoadedFingerprints) > MaxItems {
			return errors.New("recovery threshold exceeds loaded dimension limit")
		}
		for _, text := range c.LoadedFingerprints {
			fp, err := strconv.ParseUint(text, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid loaded fingerprint %q", text)
			}
			o.loaded[data.Fingerprint(fp)] = struct{}{}
		}
		return nil
	}
	if raw := strings.TrimSpace(string(c.LoadedDimensions)); raw == "" || raw == "null" {
		return nil
	}
	var parts struct {
		Schema json.RawMessage `json:"schema"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(c.LoadedDimensions, &parts); err != nil {
		return fmt.Errorf("loaded dimensions: %w", err)
	}
	// SDK frame decoding requires schema before data. Untyped JSON transports may reorder keys.
	raw := append(append(append([]byte(`{"schema":`), parts.Schema...), []byte(`,"data":`)...), parts.Data...)
	raw = append(raw, '}')
	frame := &data.Frame{}
	if err := frame.UnmarshalJSON(raw); err != nil {
		return fmt.Errorf("loaded dimensions: %w", err)
	}
	kind, version := frame.TypeInfo("")
	if kind != "fingerprints" || version.Greater(data.FrameTypeVersion{1, 0}) || len(frame.Fields) != 1 || frame.Fields[0].Type() != data.FieldTypeUint64 {
		return errors.New("loaded dimensions requires a fingerprints v1 frame with one uint64 field")
	}
	if frame.Fields[0].Len() > MaxItems {
		return errors.New("recovery threshold exceeds loaded dimension limit")
	}
	for i := 0; i < frame.Fields[0].Len(); i++ {
		n, ok := frame.Fields[0].ConcreteAt(i)
		if ok {
			fp, ok := n.(uint64)
			if !ok {
				return errors.New("invalid loaded dimension")
			}
			o.loaded[data.Fingerprint(fp)] = struct{}{}
		}
	}
	return nil
}

// HasRecovery is called after Describe has validated the model.
func HasRecovery(raw json.RawMessage) bool {
	var m queryModel
	if json.Unmarshal(raw, &m) != nil || m.Type != "threshold" || len(m.Conditions) != 1 {
		return false
	}
	u := strings.TrimSpace(string(m.Conditions[0].UnloadEvaluator))
	return u != "" && u != "null"
}

// WithLoadedFingerprints makes an evaluation-only copy; persisted query models and
// caller-supplied models remain untouched. A fresh list overrides legacy cached data.
func WithLoadedFingerprints(raw json.RawMessage, loaded []string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	var conditions []map[string]json.RawMessage
	if err := json.Unmarshal(m["conditions"], &conditions); err != nil || len(conditions) != 1 {
		return nil, errors.New("recovery threshold needs one condition")
	}
	c := conditions[0]
	if c == nil {
		return nil, errors.New("invalid recovery threshold condition")
	}
	if loaded == nil {
		loaded = []string{}
	}
	fingerprints, err := json.Marshal(loaded)
	if err != nil {
		return nil, err
	}
	c["loadedFingerprints"] = fingerprints
	delete(c, "loadedDimensions")
	encoded, err := json.Marshal(conditions)
	if err != nil {
		return nil, err
	}
	m["conditions"] = encoded
	return json.Marshal(m)
}
