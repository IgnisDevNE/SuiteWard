package execution

import "time"

// TestTimeoutSeconds is the -timeout that go test gets in every job: the limits.testTimeoutSeconds of the lease answer.
const TestTimeoutSeconds = 600

// MinJobTimeout is the shortest time a job may be given, so that a hung test ends as go test's own kill (a failure) and never as the executor's
// drop of the attempt (a timeout). It adds up:
//   - the test bound;
//   - go test's kill of a binary that ran past it, a minute later;
//   - the margin before its deadline at which the executor drops an attempt, 30 s: a copy of DefaultPacing.Margin in internal/executor/run, which
//     this package cannot import;
//   - 60 s for everything around the test binary: from the lease to its start (the archive download, the prefetch, the tar, the container start
//     and a cold compile; about 32 s per container was measured) and the parse and post of the result after it.
const MinJobTimeout = TestTimeoutSeconds*time.Second + time.Minute + 30*time.Second + 60*time.Second
