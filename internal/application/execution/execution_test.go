package execution_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The reason of a missing image is a family: the code and the id of the profile (docs/contracts/runtime.md, "A missing image"). It is read back only when it is
// well formed, and it is final, never retried.
func TestImageMissingIsTheReasonOfAMissingProfileImage(t *testing.T) {
	if got := execution.ImageMissing("go"); got != "image-missing-go" {
		t.Errorf("ImageMissing(go) = %q, want image-missing-go", got)
	}
	for _, id := range []string{"go", "cpp", "a", "a0", strings.Repeat("a", 16)} {
		if got, ok := execution.ImageMissing(id).MissingImage(); !ok || got != id {
			t.Errorf("ImageMissing(%q).MissingImage() = %q, %v; want %q, true", id, got, ok, id)
		}
	}
	for _, reason := range []execution.Reason{"", "image-missing", "image-missing-", "image-missing-Go", "image-missing-1", "image-missing-go-cpp", "image-missing-go ",
		execution.Reason("image-missing-" + strings.Repeat("a", 17)), "xx-image-missing-go", execution.ReasonToolchainMismatch, execution.ReasonProfileUnsupported} {
		if got, ok := reason.MissingImage(); ok {
			t.Errorf("%q.MissingImage() = %q, true; want false", reason, got)
		}
	}
	if execution.ImageMissing("go").Retried() {
		t.Error("image-missing-go is retried; it is final for the job, as toolchain-mismatch is")
	}
}
