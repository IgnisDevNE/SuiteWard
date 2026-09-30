package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestSuitePreservesSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		project  contract.ProjectID
		id       contract.SuiteID
		current  contract.SuiteVersionID
		revision contract.StateRevision
	}{
		{name: "awaiting initial canonical", project: "project-1", id: "suite-1"},
		{name: "existing canonical", project: "project-1", id: "suite-1", current: "version-7", revision: 42},
		{name: "accepted identifier bytes", project: " project-1 ", id: " suite-1 ", current: " version-7 ", revision: ^contract.StateRevision(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suite, err := contract.NewSuite(tt.project, tt.id, tt.current, tt.revision)
			if err != nil {
				t.Fatalf("NewSuite() error = %v", err)
			}
			if suite.ProjectID() != tt.project || suite.ID() != tt.id {
				t.Errorf("suite identity = (%q, %q), want (%q, %q)", suite.ProjectID(), suite.ID(), tt.project, tt.id)
			}
			current, present := suite.CurrentVersionID()
			if current != tt.current || present != (tt.current != "") {
				t.Errorf("canonical reference = (%q, %t), want (%q, %t)", current, present, tt.current, tt.current != "")
			}
			if suite.Revision() != tt.revision {
				t.Errorf("revision = %d, want explicit revision %d", suite.Revision(), tt.revision)
			}
		})
	}
}

func TestSuiteRejectsInvalidIdentity(t *testing.T) {
	tests := []struct {
		name    string
		project contract.ProjectID
		id      contract.SuiteID
		current contract.SuiteVersionID
	}{
		{name: "empty project", id: "suite-1"},
		{name: "blank project", project: " \t\n", id: "suite-1"},
		{name: "empty suite", project: "project-1"},
		{name: "blank suite", project: "project-1", id: "\u2003"},
		{name: "blank canonical version", project: "project-1", id: "suite-1", current: " \r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := contract.NewSuite(tt.project, tt.id, tt.current, 0)
			if !errors.Is(err, contract.ErrInvalidSuite) {
				t.Fatalf("NewSuite() error = %v, want ErrInvalidSuite", err)
			}
		})
	}
}
