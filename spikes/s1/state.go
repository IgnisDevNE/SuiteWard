package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// state is the file shape frozen in CONTRACT.md. S1-B fills Merged and Promotions.
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
	ApprovedAt        string      `json:"approved_at"`
	Merged            mergedState `json:"merged"`
}

type mergedState struct {
	Method           string `json:"method"`
	MergeCommitSHA   string `json:"merge_commit_sha"`
	IntegratedDigest string `json:"integrated_digest"`
	MatchesApproved  bool   `json:"matches_approved"`
	At               string `json:"at"`
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
