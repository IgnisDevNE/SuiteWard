package execution_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

func TestParseCanonicalLevel(t *testing.T) {
	for text, want := range map[string]execution.CanonicalLevel{
		"off":      execution.CanonicalLevelOff,
		"advisory": execution.CanonicalLevelAdvisory,
		"required": execution.CanonicalLevelRequired,
	} {
		got, err := execution.ParseCanonicalLevel(text)
		if err != nil || got != want {
			t.Errorf("ParseCanonicalLevel(%q) = %v, %v; want %v", text, got, err, want)
		}
		if got.String() != text {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), text)
		}
	}
}

func TestParseCanonicalLevelRejectsEverythingElse(t *testing.T) {
	// before-approval is a receipt level; the rest are misspellings, other cases and values with layout.
	for _, text := range []string{"", "before-approval", "Advisory", "REQUIRED", " off", "off ", "advisory\n", "on", "none", "true", "enforced"} {
		if got, err := execution.ParseCanonicalLevel(text); !errors.Is(err, execution.ErrInvalidLevel) {
			t.Errorf("ParseCanonicalLevel(%q) = %v, %v; want ErrInvalidLevel", text, got, err)
		}
	}
}

func TestParseReceiptLevel(t *testing.T) {
	for text, want := range map[string]execution.ReceiptLevel{
		"off":             execution.ReceiptLevelOff,
		"advisory":        execution.ReceiptLevelAdvisory,
		"before-approval": execution.ReceiptLevelBeforeApproval,
		"required":        execution.ReceiptLevelRequired,
	} {
		got, err := execution.ParseReceiptLevel(text)
		if err != nil || got != want {
			t.Errorf("ParseReceiptLevel(%q) = %v, %v; want %v", text, got, err, want)
		}
		if got.String() != text {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), text)
		}
	}
}

func TestParseReceiptLevelRejectsEverythingElse(t *testing.T) {
	for _, text := range []string{"", "before_approval", "beforeapproval", "Before-Approval", " required", "on", "strict"} {
		if got, err := execution.ParseReceiptLevel(text); !errors.Is(err, execution.ErrInvalidLevel) {
			t.Errorf("ParseReceiptLevel(%q) = %v, %v; want ErrInvalidLevel", text, got, err)
		}
	}
}

func TestLevelStringIsEmptyForAValueThatIsNotALevel(t *testing.T) {
	if got := execution.CanonicalLevel(0).String(); got != "" {
		t.Errorf("zero CanonicalLevel.String() = %q, want empty", got)
	}
	if got := execution.ReceiptLevel(200).String(); got != "" {
		t.Errorf("ReceiptLevel(200).String() = %q, want empty", got)
	}
}
