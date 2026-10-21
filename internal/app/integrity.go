package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// contentVerifier is the part of the artifact store the integrity job uses.
type contentVerifier interface {
	Verify(ctx context.Context, digest artifact.Digest) error
}

// integrityJob is the canonical_integrity job (docs/contracts/runtime.md, "Merges and integrity jobs (M1.7)"): it
// re-verifies the stored bytes of each Suite's current canonical against the digests of its manifest and records
// the verdict. The verdict is evidence about the bytes, never authority; the checks job and the merges job act on it.
type integrityJob struct {
	owners  owner.Store
	suites  governance.UnitOfWork
	content contentVerifier
	store   integrity.Store
	logger  *slog.Logger
	now     func() time.Time
}

// run verifies the canonical of every enrolled project. A store failure fails the run after every project was tried;
// a cancellation ends it at once and records nothing for the project it interrupted.
func (j *integrityJob) run(ctx context.Context) error {
	enrollment, enrolled, err := j.owners.Current(ctx)
	if err != nil {
		return fmt.Errorf("read the enrollment: %w", err)
	}
	if !enrolled {
		return nil
	}
	var failures []error
	for _, project := range enrollment.Projects {
		if err := j.project(ctx, owner.ProjectID(project.RepositoryID)); err != nil {
			if cancelled := ctx.Err(); cancelled != nil {
				return cancelled
			}
			failures = append(failures, fmt.Errorf("integrity %s: %w", project.Repo, err))
		}
	}
	return errors.Join(failures...)
}

// project verifies the canonical of one project, if it has one.
func (j *integrityJob) project(ctx context.Context, project contract.ProjectID) error {
	version, digests, err := j.canonical(ctx, project)
	if err != nil || version == "" {
		return err
	}
	verdict := j.verify(ctx, project, digests)
	if err := ctx.Err(); err != nil { // a cancelled read says nothing about the bytes
		return err
	}
	if err := j.store.Record(ctx, integrity.Record{Project: project, Suite: defaultSuite, Version: version, Verdict: verdict, CheckedAt: j.now()}); err != nil {
		return fmt.Errorf("record the verdict: %w", err)
	}
	if verdict == integrity.Failed { // the project and the version only: never the digests or the bytes
		j.logger.ErrorContext(ctx, "canonical integrity failed", "project", string(project), "version", string(version))
	}
	return nil
}

// canonical returns the current canonical version of the project's Suite and the content digests of its manifest,
// read in one unit of work. The version is empty when there is no Suite or no canonical.
func (j *integrityJob) canonical(ctx context.Context, project contract.ProjectID) (version contract.SuiteVersionID, digests []artifact.Digest, err error) {
	err = j.suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		current, present := state.Canonical.Suite().CurrentVersionID()
		if !present {
			return nil
		}
		version = current
		for _, entry := range state.Canonical.Version().Manifest().Entries() {
			digests = append(digests, entry.Content)
		}
		return nil
	})
	if errors.Is(err, governance.ErrNotFound) { // no Suite yet: the bootstrap creates it
		return "", nil, nil
	}
	return version, digests, err
}

// verify returns failed when a digest is missing or does not match its bytes, else unavailable when one could not be
// read, else passed. It goes on after an unreadable entry, so that one never hides a corrupt one.
func (j *integrityJob) verify(ctx context.Context, project contract.ProjectID, digests []artifact.Digest) integrity.Verdict {
	verdict := integrity.Passed
	for _, digest := range digests {
		switch err := j.content.Verify(ctx, digest); {
		case err == nil:
		case errors.Is(err, filesystem.ErrCorruptArtifact) || errors.Is(err, fs.ErrNotExist):
			return integrity.Failed
		default:
			if verdict == integrity.Passed && ctx.Err() == nil { // once per project and run: the error names an object file, never its content
				j.logger.WarnContext(ctx, "canonical integrity unavailable", "project", string(project), "error", err)
			}
			verdict = integrity.Unavailable
		}
	}
	return verdict
}
