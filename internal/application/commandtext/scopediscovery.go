package commandtext

import "errors"

// ScopeNotice is what the scope-notice comment states.
type ScopeNotice struct {
	Source      int64
	Repo        string
	Branch      string
	Paths       []string
	PathsTotal  int
	Declaration []byte
	Mixed       []string
}

func ScopeNoticeComment(n ScopeNotice) string { panic("not implemented") }

func ValidateScopeNotice(n ScopeNotice) error { return errors.New("not implemented") }

func ScopeSuggestionBody(anchorLine, indent string, patterns []string) string {
	panic("not implemented")
}

func ValidateScopeSuggestion(anchorLine, indent string, patterns []string) error {
	return errors.New("not implemented")
}
