package main

import "strings"

// judge classifies one comment. A command is a comment whose first line starts with the word /suiteward;
// anything else is ignored. reason is empty for a valid approval of ref, else malformed, wrong_author or stale_ref.
// The author and the ref are compared exactly (case-sensitive).
func (a *app) judge(c comment, ref string) (isCommand bool, reason string) {
	first, _, _ := strings.Cut(strings.TrimSpace(c.Body), "\n")
	f := strings.Fields(first)
	switch {
	case len(f) == 0 || f[0] != "/suiteward":
		return false, ""
	case len(f) != 3 || f[1] != "approve":
		return true, "malformed"
	case c.User.Login != a.cfg.ownerLogin:
		return true, "wrong_author"
	case f[2] != ref:
		return true, "stale_ref"
	}
	return true, ""
}

// scanApproval recomputes the approval of one open PR from its comments; it runs every cycle, on 304s too.
// An approval already recorded for the current ref is sticky: its comment is only observed (edited, deleted),
// never re-judged. Otherwise the earliest valid approval of the current ref wins, which is also how a ref that
// returns to an earlier value regains its approval. Each command's verdict is logged once per updated_at.
func (a *app) scanApproval(number int, ps *pullState, cs []comment) {
	byID := make(map[int64]comment, len(cs))
	var winner *comment
	for i, c := range cs {
		byID[c.ID] = c
		isCommand, reason := a.judge(c, ps.Ref)
		if !isCommand {
			continue
		}
		if reason == "" && winner == nil {
			winner = &cs[i]
		}
		if ps.Judged[c.ID] == c.UpdatedAt {
			continue
		}
		ps.Judged[c.ID] = c.UpdatedAt
		if reason != "" {
			ps.Rejected = reason
			a.log.emit("approval", map[string]any{"number": number, "comment_id": c.ID, "author": c.User.Login, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt, "result": "rejected", "reason": reason, "ref": ps.Ref})
		}
	}

	if ps.ApprovedCommentID != 0 {
		a.observeApproval(number, ps, byID)
		return
	}
	if winner == nil {
		return
	}
	ps.ApprovedCommentID, ps.ApprovedAt, ps.ApprovedUpdatedAt = winner.ID, winner.CreatedAt, winner.UpdatedAt
	ps.ApprovedDigest, ps.Rejected = ps.Digest, ""
	a.log.emit("approval", map[string]any{"number": number, "comment_id": winner.ID, "author": winner.User.Login, "created_at": winner.CreatedAt, "updated_at": winner.UpdatedAt, "result": "approved", "ref": ps.Ref})
}

// observeApproval logs, once per change, that the approving comment was edited or deleted. The approval is
// not touched: the contract says observed, not interpreted.
func (a *app) observeApproval(number int, ps *pullState, byID map[int64]comment) {
	c, ok := byID[ps.ApprovedCommentID]
	kind, updated := "deleted", ""
	if ok {
		if c.UpdatedAt == ps.ApprovedUpdatedAt {
			return
		}
		kind, updated = "edited", c.UpdatedAt
	}
	if kind == ps.ApprovalObserved && updated == ps.ApprovalObservedUpdatedAt {
		return
	}
	ps.ApprovalObserved, ps.ApprovalObservedUpdatedAt = kind, updated
	f := map[string]any{"number": number, "comment_id": ps.ApprovedCommentID, "result": kind, "approved_updated_at": ps.ApprovedUpdatedAt, "updated_at": updated}
	if ok {
		isCommand, reason := a.judge(c, ps.Ref)
		f["still_valid"] = isCommand && reason == ""
	}
	a.log.emit("approval", f)
}
