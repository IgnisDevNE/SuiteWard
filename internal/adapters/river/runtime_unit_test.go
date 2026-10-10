package river

import (
	"testing"
	"time"
)

func TestStuckJobsAreRescuedOneMinuteAfterTheJobTimeout(t *testing.T) {
	for name, test := range map[string]struct {
		config Config
		want   time.Duration
	}{
		"default":  {Config{JobTimeout: time.Minute}, 2 * time.Minute},
		"short":    {Config{JobTimeout: 5 * time.Second}, 65 * time.Second},
		"injected": {Config{JobTimeout: 5 * time.Second, RescueStuckJobsAfter: time.Second * 10}, 10 * time.Second},
	} {
		if got := test.config.rescueStuckJobsAfter(); got != test.want {
			t.Errorf("%s: rescue after %v, want %v", name, got, test.want)
		}
	}
}
