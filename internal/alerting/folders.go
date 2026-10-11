package alerting

import "context"

func (e *Engine) DeleteFolder(ctx context.Context, uid string, forceDeleteRules bool) error {
	e.groupMu.Lock()
	defer e.groupMu.Unlock()
	return e.Store.DeleteFolderWithRuleGate(ctx, uid, forceDeleteRules, func(uids []string) (func(), error) {
		e.mu.Lock()
		for _, uid := range uids {
			if e.active[uid] {
				e.mu.Unlock()
				return nil, ErrBusy
			}
		}
		for _, uid := range uids {
			e.active[uid] = true
		}
		e.mu.Unlock()
		return func() {
			e.mu.Lock()
			for _, uid := range uids {
				delete(e.active, uid)
			}
			e.mu.Unlock()
		}, nil
	})
}
