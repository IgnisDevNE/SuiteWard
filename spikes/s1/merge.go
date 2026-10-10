package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// promotion is the record appended to the state's promotions: the merge facts of a PR whose integrated
// protected digest equals the approved one.
type promotion struct {
	Number int `json:"number"`
	mergedState
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

func (a *app) commit(ctx context.Context, sha string) (gitCommit, error) {
	var c gitCommit
	err := a.c.getJSON(ctx, "/repos/"+a.cfg.repo+"/git/commits/"+sha, &c)
	return c, err
}

// closed examines a PR that was open in the state and is not in the open list any more. Nothing is recorded
// until every request succeeded, so a failed cycle retries without appending a promotion twice.
func (a *app) closed(ctx context.Context, number int, ps *pullState) error {
	var d pullDetail
	if err := a.c.getJSON(ctx, fmt.Sprintf("/repos/%s/pulls/%d", a.cfg.repo, number), &d); err != nil {
		return err
	}
	switch {
	case d.State == "open":
		return nil // not on the first page of the list: still tracked, looked at again next cycle
	case !d.Merged:
		ps.Closed = "closed"
		a.log.emit("merge", map[string]any{"number": number, "merged": false})
		return nil
	case d.MergeCommitSHA == "":
		return errors.New("PR is merged but GitHub reports no merge_commit_sha")
	}
	merge, err := a.commit(ctx, d.MergeCommitSHA)
	if err != nil {
		return err
	}
	head, err := a.commit(ctx, d.Head.SHA)
	if err != nil {
		return err
	}
	entries, err := a.c.tree(ctx, a.cfg.repo, merge.Tree.SHA)
	if err != nil {
		return err
	}
	integrated, _ := protectedDigest(entries, a.cfg.protectedPrefix)

	m := mergedState{
		MergeCommitSHA: d.MergeCommitSHA, MergeCommitIsHead: d.MergeCommitSHA == d.Head.SHA,
		TreeSHA: merge.Tree.SHA, HeadSHA: d.Head.SHA, HeadTreeSHA: head.Tree.SHA, TreeEqualsHead: merge.Tree.SHA == head.Tree.SHA,
		BaseSHA: d.Base.SHA, HeadIsBase: d.Head.SHA == d.Base.SHA, MergedBy: d.MergedBy.Login, MergedAt: d.MergedAt,
		ApprovedDigest: ps.ApprovedDigest, IntegratedDigest: integrated, At: time.Now().UTC().Format(time.RFC3339Nano),
	}
	for _, p := range merge.Parents {
		m.Parents = append(m.Parents, p.SHA)
	}
	switch len(m.Parents) {
	case 2:
		m.Method = "merge"
	case 1:
		// GitHub's squash and rebase merges both leave one parent; commit titles are the only hint.
		m.Method = "squash_or_rebase"
		switch title := firstLine(merge.Message); {
		case strings.HasSuffix(title, fmt.Sprintf("(#%d)", number)):
			m.TitleHint = "squash"
		case title == firstLine(head.Message):
			m.TitleHint = "rebase"
		}
	default:
		m.Method = fmt.Sprintf("unknown_%d_parents", len(m.Parents))
	}
	switch {
	case m.ApprovedDigest == "":
		m.Reason = "no approval recorded"
	case m.IntegratedDigest != m.ApprovedDigest:
		m.Reason = "integrated digest differs from the approved digest"
	default:
		m.MatchesApproved = true
	}
	a.log.emit("merge", map[string]any{"number": number, "merged": true, "result": m})
	if m.MatchesApproved {
		b, err := json.Marshal(promotion{number, m})
		if err != nil {
			return err
		}
		a.st.Promotions = append(a.st.Promotions, b)
		a.log.emit("promotion", map[string]any{"number": number, "merge_commit_sha": m.MergeCommitSHA, "integrated_digest": integrated})
	}
	ps.Merged, ps.Closed = m, "merged"
	return nil
}
