package ownertest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Memory is an in-memory owner.Store with the semantics of the persistence
// contract. One mutex serializes every call, and Enroll validates and checks
// before its first write, so a failed call changes nothing.
type Memory struct {
	mu         sync.Mutex
	enrolled   bool
	generation int64
	state      owner.State
	steps      map[stepKey]bool
	suites     map[contract.ProjectID]bool
	protection map[int64]owner.ProtectionResult
}

type stepKey struct{ account, step int64 }

var _ owner.Store = (*Memory)(nil)

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{steps: map[stepKey]bool{}, suites: map[contract.ProjectID]bool{}, protection: map[int64]owner.ProtectionResult{}}
}

// AddSuite records that project has a Suite, which Enroll consults when the owner changes.
func (m *Memory) AddSuite(project contract.ProjectID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.suites[project] = true
}

// Enroll implements owner.Store.
func (m *Memory) Enroll(ctx context.Context, e owner.Enrollment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enrolled && m.state.Owner.AccountID != e.Owner.AccountID {
		for _, p := range e.Projects {
			if m.suites[owner.ProjectID(p.RepositoryID)] {
				return fmt.Errorf("%w: another owner cannot take over a project that has a Suite", owner.ErrInvalidState)
			}
		}
	}
	if m.steps[stepKey{e.Owner.AccountID, e.Step}] {
		return owner.ErrStepUsed
	}
	projects := slices.Clone(e.Projects)
	slices.SortFunc(projects, func(a, b owner.ProjectPolicy) int { return cmp.Compare(a.RepositoryID, b.RepositoryID) })
	m.generation++
	m.enrolled = true
	m.state = owner.State{Owner: e.Owner, Secret: slices.Clone(e.Secret), Projects: projects, Tier: e.Tier, PolicyRevision: owner.RevisionID(m.generation),
		EnrolledAt: e.EnrolledAt.UTC()}
	m.steps[stepKey{e.Owner.AccountID, e.Step}] = true
	return nil
}

// Current implements owner.Store.
func (m *Memory) Current(ctx context.Context) (owner.State, bool, error) {
	if err := ctx.Err(); err != nil {
		return owner.State{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enrolled {
		return owner.State{}, false, nil
	}
	state := m.state
	state.Secret, state.Projects = slices.Clone(state.Secret), slices.Clone(state.Projects)
	return state, true, nil
}

// ConsumeStep implements owner.Store.
func (m *Memory) ConsumeStep(ctx context.Context, accountID int64, step int64, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if at.IsZero() {
		return fmt.Errorf("%w: the consumption time is required", owner.ErrInvalidState)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enrolled || m.state.Owner.AccountID != accountID {
		return owner.ErrNotEnrolled
	}
	if m.steps[stepKey{accountID, step}] {
		return owner.ErrStepUsed
	}
	m.steps[stepKey{accountID, step}] = true
	return nil
}

// RecordProtection implements owner.Store.
func (m *Memory) RecordProtection(ctx context.Context, r owner.ProtectionResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r.Findings, r.Caveats, r.CheckedAt = nonNil(r.Findings), nonNil(r.Caveats), r.CheckedAt.UTC()
	m.protection[r.RepositoryID] = r
	return nil
}

func nonNil(codes []string) []string {
	if codes == nil {
		return []string{}
	}
	return slices.Clone(codes)
}

// Protection implements owner.Store.
func (m *Memory) Protection(ctx context.Context) ([]owner.ProtectionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	results := make([]owner.ProtectionResult, 0, len(m.protection))
	for _, r := range m.protection {
		results = append(results, r)
	}
	slices.SortFunc(results, func(a, b owner.ProtectionResult) int { return cmp.Compare(a.RepositoryID, b.RepositoryID) })
	return results, nil
}
