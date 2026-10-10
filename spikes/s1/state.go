package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// state is the file shape frozen in CONTRACT.md plus the S1-B fields listed in README.md.
type state struct {
	Etags      map[string]string  `json:"etags"`
	Pulls      map[int]*pullState `json:"pulls"`
	Promotions []json.RawMessage  `json:"promotions"`
}

type pullState struct {
	HeadSHA           string      `json:"head_sha"`
	Digest            string      `json:"digest"`
	Ref               string      `json:"ref"`
	CheckRunID        int64       `json:"check_run_id"`
	ApprovedCommentID int64       `json:"approved_comment_id"`
	ApprovedAt        string      `json:"approved_at"` // created_at of the approving comment
	Merged            mergedState `json:"merged"`

	ApprovedUpdatedAt         string           `json:"approved_updated_at"` // updated_at of the approving comment when it was recorded
	ApprovedDigest            string           `json:"approved_digest"`     // protected digest the approval covers
	ApprovalObserved          string           `json:"approval_observed"`   // "", "edited" or "deleted": what was seen of the approving comment since
	ApprovalObservedUpdatedAt string           `json:"approval_observed_updated_at"`
	Rejected                  string           `json:"rejected"` // reason of an invalid approval attempt under the current ref; makes the check failure
	Judged                    map[int64]string `json:"judged"`   // command comment id -> updated_at already judged, so a verdict is logged once
	Closed                    string           `json:"closed"`   // "", "merged" or "closed": the PR left the open list and was examined
}

// mergedState is what GitHub reported about the integrated commit plus what was derived from it.
type mergedState struct {
	Method            string   `json:"method"`     // "merge" (two parents) or "squash_or_rebase" (one parent): structure only
	TitleHint         string   `json:"title_hint"` // guess from commit titles: "squash", "rebase" or ""; depends on the repository's squash-title setting
	MergeCommitSHA    string   `json:"merge_commit_sha"`
	MergeCommitIsHead bool     `json:"merge_commit_is_head"`
	Parents           []string `json:"parents"`
	TreeSHA           string   `json:"tree_sha"`
	HeadSHA           string   `json:"head_sha"`
	HeadTreeSHA       string   `json:"head_tree_sha"`
	TreeEqualsHead    bool     `json:"tree_equals_head"`
	BaseSHA           string   `json:"base_sha"` // base.sha as the closed PR reports it, not the post-merge tip
	HeadIsBase        bool     `json:"head_is_base"`
	MergedBy          string   `json:"merged_by"`
	MergedAt          string   `json:"merged_at"`
	ApprovedDigest    string   `json:"approved_digest"`
	IntegratedDigest  string   `json:"integrated_digest"`
	MatchesApproved   bool     `json:"matches_approved"`
	Reason            string   `json:"reason"`
	At                string   `json:"at"`
}

func loadState(path string) (*state, error) {
	s := &state{}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(b, s); err != nil {
			return nil, fmt.Errorf("parse state: %w", err)
		}
	}
	// A file written by hand or by another version may omit these.
	if s.Etags == nil {
		s.Etags = map[string]string{}
	}
	if s.Pulls == nil {
		s.Pulls = map[int]*pullState{}
	}
	if s.Promotions == nil {
		s.Promotions = []json.RawMessage{}
	}
	return s, nil
}

// save writes the state atomically: temp file in the same directory, then rename.
func (s *state) save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("write temp state: %w", werr)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}
