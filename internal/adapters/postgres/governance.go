// Package postgres implements transactional governance with PostgreSQL.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

type ArtifactVerifier interface { Verify(context.Context, artifact.Digest) error }

type Store struct { pool *pgxpool.Pool; verifier ArtifactVerifier }

// TrustedAuthority initializes an unbaselined Suite from authenticated instance
// configuration. It cannot replace authority or import processing history.
type TrustedAuthority struct {
	Canonical contract.CanonicalSnapshot
	Policy contract.Policy
	Scheduling contract.Schedule
	Target contract.IntegrationTargetID
	Proposals []TrustedProposal
}
type TrustedProposal struct {
	Proposal contract.Proposal
	Consent contract.Consent
	Assessments []contract.IntegrityAssessment
}

func NewStore(pool *pgxpool.Pool, verifier ArtifactVerifier) (*Store,error) {
	if pool==nil||verifier==nil{return nil,governance.ErrInvalidRequest}
	return &Store{pool:pool,verifier:verifier},nil
}
func (s *Store) InitializeTrusted(context.Context,TrustedAuthority) error { return governance.ErrNotFound }
func (s *Store) Load(context.Context,governance.ReadRequest) (governance.Snapshot,error) { return governance.Snapshot{},governance.ErrNotFound }
func (s *Store) CommitPromotion(context.Context,governance.AuthorityFence,governance.PromotionWrite) error { return governance.ErrNotFound }
func (s *Store) CommitConsent(context.Context,governance.AuthorityFence,governance.ConsentWrite) error { return governance.ErrNotFound }

var _ governance.Store = (*Store)(nil)
