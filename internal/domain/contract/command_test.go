package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func commandInputForTest(t *testing.T) contract.CommandInput {
	t.Helper()
	return contract.CommandInput{
		OperationID:     " operation-1 ",
		SourceCommandID: " source-command-1 ",
		Actor:           principalForTest(t, " owner ", contract.Human),
		Reference: contract.ProposalReference{
			ProjectID: " project-1 ", SuiteID: " suite-1 ", ProposalID: " proposal-1 ", RevisionID: " revision-1 ",
		},
		Carrier: " carrier-1 ",
		Action:  contract.ApproveConsent,
		Order:   42,
	}
}

func TestCommandPreservesExactFacts(t *testing.T) {
	for _, kind := range []contract.PrincipalKind{contract.Human, contract.Agent, contract.Service} {
		for _, action := range []contract.ConsentAction{contract.ApproveConsent, contract.RevokeConsent} {
			input := commandInputForTest(t)
			input.Actor = principalForTest(t, " owner ", kind)
			input.Action = action
			command, err := contract.NewCommand(input)
			if err != nil {
				t.Fatal(err)
			}
			if command.OperationID() != input.OperationID || command.SourceCommandID() != input.SourceCommandID || command.Actor() != input.Actor || command.Reference() != input.Reference || command.Carrier() != input.Carrier || command.Action() != input.Action || command.Order() != input.Order {
				t.Fatalf("command did not retain the exact supplied facts for actor kind %d, action %d", kind, action)
			}
			// Later edits to caller-owned input and returned references cannot edit the command.
			input.Reference.RevisionID = "edited-revision"
			returned := command.Reference()
			returned.RevisionID = "another-revision"
			if command.Reference().RevisionID != " revision-1 " {
				t.Fatal("a command reference changed after modifying a copied input or getter result")
			}
		}
	}
}

func TestCommandRejectsIncompleteFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contract.CommandInput)
	}{
		{"empty operation", func(input *contract.CommandInput) { input.OperationID = "" }},
		{"blank operation", func(input *contract.CommandInput) { input.OperationID = " \u2003" }},
		{"empty source command", func(input *contract.CommandInput) { input.SourceCommandID = "" }},
		{"blank source command", func(input *contract.CommandInput) { input.SourceCommandID = "\t\n" }},
		{"absent actor", func(input *contract.CommandInput) { input.Actor = contract.Principal{} }},
		{"empty project", func(input *contract.CommandInput) { input.Reference.ProjectID = "" }},
		{"blank project", func(input *contract.CommandInput) { input.Reference.ProjectID = " \u2003" }},
		{"empty suite", func(input *contract.CommandInput) { input.Reference.SuiteID = "" }},
		{"blank suite", func(input *contract.CommandInput) { input.Reference.SuiteID = "\t" }},
		{"empty proposal", func(input *contract.CommandInput) { input.Reference.ProposalID = "" }},
		{"blank proposal", func(input *contract.CommandInput) { input.Reference.ProposalID = "\n" }},
		{"empty revision", func(input *contract.CommandInput) { input.Reference.RevisionID = "" }},
		{"blank revision", func(input *contract.CommandInput) { input.Reference.RevisionID = "\u2003" }},
		{"empty carrier", func(input *contract.CommandInput) { input.Carrier = "" }},
		{"blank carrier", func(input *contract.CommandInput) { input.Carrier = " \r" }},
		{"absent action", func(input *contract.CommandInput) { input.Action = 0 }},
		{"unknown action", func(input *contract.CommandInput) { input.Action = 255 }},
		{"absent order", func(input *contract.CommandInput) { input.Order = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := commandInputForTest(t)
			tt.mutate(&input)
			command, err := contract.NewCommand(input)
			if !errors.Is(err, contract.ErrInvalidCommand) {
				t.Errorf("NewCommand error = %v; want ErrInvalidCommand", err)
			}
			if command != (contract.Command{}) {
				t.Error("invalid facts produced a constructed command")
			}
		})
	}
}

func TestCommandAcceptsMaximumExplicitOrder(t *testing.T) {
	input := commandInputForTest(t)
	input.Order = ^contract.CommandOrder(0)
	command, err := contract.NewCommand(input)
	if err != nil || command.Order() != input.Order {
		t.Errorf("maximum supplied order = %d, error %v; want %d", command.Order(), err, input.Order)
	}
}
