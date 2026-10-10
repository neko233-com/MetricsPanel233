package expressions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func IsSource(uid string) bool { return uid == "__expr__" || uid == "-100" || uid == "Expression" }

type SourceQuery func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error)

// Execute batches each actual datasource once, then evaluates an order-independent
// DAG. Errors are attached to the failing refId and its dependent nodes; unrelated
// queries remain usable. Source data is normalized into number/series frames
// without mutating plugin-owned inputs or carrying transient Live channels.
func Execute(ctx context.Context, groups map[string][]backend.DataQuery, source SourceQuery) *backend.QueryDataResponse {
	response := backend.NewQueryDataResponse()
	vars := map[string]values{}
	queries := map[string]backend.DataQuery{}
	ops := map[string]operation{}
	visiting := map[string]bool{}
	done := map[string]bool{}
	b := &budget{ctx: ctx}
	sourceTypes := map[string]string{}
	uids := make([]string, 0, len(groups))
	for uid := range groups {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	for _, uid := range uids {
		group := groups[uid]
		if IsSource(uid) {
			for _, q := range group {
				queries[q.RefID] = q
				var m queryModel
				err := json.Unmarshal(q.JSON, &m)
				var op operation
				if err == nil {
					op, err = compile(m)
				}
				if err != nil {
					response.Responses[q.RefID] = backend.DataResponse{Error: err, Status: backend.StatusBadRequest}
					done[q.RefID] = true
				} else {
					ops[q.RefID] = op
				}
			}
			continue
		}
		result, sourceType, err := source(ctx, uid, group)
		for _, q := range group {
			if err != nil {
				response.Responses[q.RefID] = backend.DataResponse{Error: err, Status: backend.StatusBadGateway}
			} else if r, ok := result[q.RefID]; ok {
				if r.Error == nil {
					parsed, convertErr := fromFrames(r.Frames, sourceType, b)
					if convertErr != nil {
						r.Error = convertErr
						r.Status = backend.StatusBadRequest
						r.Frames = nil
					} else {
						vars[q.RefID] = parsed
						r.Frames = toFrames(q.RefID, parsed)
					}
				}
				response.Responses[q.RefID] = r
			} else {
				response.Responses[q.RefID] = backend.DataResponse{Error: errors.New("datasource omitted query response"), Status: backend.StatusBadGateway}
			}
			sourceTypes[q.RefID] = sourceType
			done[q.RefID] = true
		}
	}
	var evaluate func(string) error
	evaluate = func(ref string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if done[ref] {
			result, ok := response.Responses[ref]
			if !ok {
				return fmt.Errorf("missing query reference %q", ref)
			}
			return result.Error
		}
		op, ok := ops[ref]
		if !ok {
			return fmt.Errorf("missing query reference %q", ref)
		}
		if visiting[ref] {
			return fmt.Errorf("cyclic expression dependency at %s", ref)
		}
		visiting[ref] = true
		var failure error
		for _, dependency := range op.dependencies {
			if err := evaluate(dependency); err != nil {
				failure = fmt.Errorf("dependency %s failed: %w", dependency, err)
				break
			}
			if _, converted := vars[dependency]; !converted {
				result := response.Responses[dependency]
				parsed, err := fromFrames(result.Frames, sourceTypes[dependency], b)
				if err != nil {
					failure = fmt.Errorf("dependency %s: %w", dependency, err)
					break
				}
				vars[dependency] = parsed
			}
		}
		if failure == nil {
			q := queries[ref]
			output, err := op.execute(vars, q.TimeRange.From, q.TimeRange.To, b)
			if err != nil {
				failure = err
			} else {
				vars[ref] = output
				response.Responses[ref] = backend.DataResponse{Frames: toFrames(ref, output)}
			}
		}
		if failure != nil {
			response.Responses[ref] = backend.DataResponse{Error: failure, Status: backend.StatusBadRequest}
		}
		visiting[ref] = false
		done[ref] = true
		return failure
	}
	refs := make([]string, 0, len(ops))
	for ref := range ops {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	for _, ref := range refs {
		if err := evaluate(ref); err != nil {
			if _, exists := response.Responses[ref]; !exists {
				response.Responses[ref] = backend.DataResponse{Error: err, Status: backend.StatusBadGateway}
			}
		}
	}
	return response
}
