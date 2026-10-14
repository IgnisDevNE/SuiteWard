package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// CheckRun is a check run to create. Status is in_progress or completed;
// Conclusion is success or failure with completed and empty otherwise.
type CheckRun struct {
	Name, HeadSHA, Status, Conclusion, ExternalID, Title, Summary string
}

// CheckRunSummary is one listed check run; Conclusion is empty until it completes.
type CheckRunSummary struct {
	ID                             int64
	Status, Conclusion, ExternalID string
}

// Checks creates and lists check runs. Its client's token map is
// {metadata: read, checks: write}; the composition supplies it, apart from
// the Writer's so the two tokens cannot be mixed.
type Checks struct {
	client *rest.Client
}

// NewChecks returns a check writer over client.
func NewChecks(client *rest.Client) (*Checks, error) {
	if client == nil {
		return nil, errors.New("github checks: nil client")
	}
	return &Checks{client: client}, nil
}

// maxCheckBody is the body limit of rest.Post, checked here to name the cause.
const maxCheckBody = 64 << 10

func (r CheckRun) validate() error {
	switch {
	case r.Name == "":
		return fmt.Errorf("%w: empty check run name", ErrInvalidArgument)
	case !hexSHA.MatchString(r.HeadSHA):
		return fmt.Errorf("%w: head SHA %q is not 40 lowercase hex characters", ErrInvalidArgument, r.HeadSHA)
	case r.Status == "in_progress" && r.Conclusion != "":
		return fmt.Errorf("%w: an in_progress check run has no conclusion", ErrInvalidArgument)
	case r.Status == "completed" && r.Conclusion != "success" && r.Conclusion != "failure":
		return fmt.Errorf("%w: a completed check run concludes success or failure, not %q", ErrInvalidArgument, r.Conclusion)
	case r.Status != "in_progress" && r.Status != "completed":
		return fmt.Errorf("%w: check run status %q is not in_progress or completed", ErrInvalidArgument, r.Status)
	case r.ExternalID == "" || r.Title == "" || r.Summary == "":
		return fmt.Errorf("%w: a check run needs an external id, a title and a summary", ErrInvalidArgument)
	}
	return nil
}

// Post creates run on repo and returns its id. The latest run with a name
// wins, so a later run supersedes an earlier one; none is ever updated.
func (c *Checks) Post(ctx context.Context, repo string, run CheckRun) (int64, error) {
	if err := checkRepo(repo); err != nil {
		return 0, err
	}
	if err := run.validate(); err != nil {
		return 0, err
	}
	type output struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	payload, err := json.Marshal(struct {
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion,omitempty"`
		ExternalID string `json:"external_id"`
		Output     output `json:"output"`
	}{run.Name, run.HeadSHA, run.Status, run.Conclusion, run.ExternalID, output{run.Title, run.Summary}})
	if err != nil {
		return 0, fmt.Errorf("encode check run on %s of %s: %w", run.HeadSHA, repo, err)
	}
	if len(payload) > maxCheckBody {
		return 0, fmt.Errorf("%w: check run body of %d bytes exceeds %d", ErrInvalidArgument, len(payload), maxCheckBody)
	}
	path := "/repos/" + repo + "/check-runs"
	resp, err := c.client.Post(ctx, path, payload)
	if err != nil {
		return 0, fmt.Errorf("post check run on %s of %s: %w", run.HeadSHA, repo, err)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := decode(path, resp.Body, &created); err != nil {
		return 0, err
	}
	if created.ID <= 0 {
		return 0, fmt.Errorf("%w: the answer to %s has no check run id", ErrInvalidResponse, path)
	}
	return created.ID, nil
}

type checkRunJSON struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"` // null while the run is not completed
	ExternalID string `json:"external_id"`
}

var checkRunStatuses = []string{"queued", "in_progress", "completed", "waiting", "requested", "pending"}

// Runs lists the check runs of sha named name that the App appID created,
// sorted by id. GitHub's default listing keeps the latest run per name, so
// the last element is the one that counts.
func (c *Checks) Runs(ctx context.Context, repo, sha, name string, appID int64) ([]CheckRunSummary, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	switch {
	case !hexSHA.MatchString(sha):
		return nil, fmt.Errorf("%w: commit SHA %q is not 40 lowercase hex characters", ErrInvalidArgument, sha)
	case name == "":
		return nil, fmt.Errorf("%w: empty check run name", ErrInvalidArgument)
	case appID <= 0:
		return nil, fmt.Errorf("%w: app id %d", ErrInvalidArgument, appID)
	}
	raw, err := readPagesOf(ctx, c.client, func(page int) string {
		return fmt.Sprintf("/repos/%s/commits/%s/check-runs?check_name=%s&app_id=%d&per_page=%d&page=%d",
			repo, sha, url.QueryEscape(name), appID, pageSize, page)
	}, func(path string, body []byte) ([]checkRunJSON, error) {
		var list struct {
			CheckRuns []checkRunJSON `json:"check_runs"`
		}
		if err := decode(path, body, &list); err != nil {
			return nil, err
		}
		return list.CheckRuns, nil
	})
	if err != nil {
		return nil, fmt.Errorf("check runs of %s on %s: %w", repo, sha, err)
	}
	runs := make([]CheckRunSummary, 0, len(raw))
	for _, r := range latest(raw, func(r checkRunJSON) int64 { return r.ID }) {
		switch {
		case r.ID <= 0:
			return nil, fmt.Errorf("%w: check run of %s on %s without an id", ErrInvalidResponse, repo, sha)
		case !slices.Contains(checkRunStatuses, r.Status):
			return nil, fmt.Errorf("%w: check run %d of %s has status %q", ErrInvalidResponse, r.ID, repo, r.Status)
		}
		runs = append(runs, CheckRunSummary(r))
	}
	slices.SortFunc(runs, func(a, b CheckRunSummary) int { return cmp.Compare(a.ID, b.ID) })
	return runs, nil
}
