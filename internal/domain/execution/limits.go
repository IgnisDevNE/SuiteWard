package execution

import "time"

// TestTimeoutSeconds is the -timeout that go test gets in every job: the limits.testTimeoutSeconds of the lease answer.
const TestTimeoutSeconds = 600

// MinJobTimeout is the shortest time a job may be given: go test kills its own test binary a minute past -timeout, so a job that is shorter would end
// before go test does.
const MinJobTimeout = TestTimeoutSeconds*time.Second + time.Minute
