package contract

import "errors"

// OperationID identifies a stable internally normalized processing operation.
type OperationID string

// SourceCommandID identifies a command in its authenticated source context.
type SourceCommandID string

// CommandOrder is positive, stable source order supplied by a trusted caller.
// It is neither a timestamp nor the order in which observations arrive.
type CommandOrder uint64

type ConsentAction uint8

const (
	ApproveConsent ConsentAction = iota + 1
	RevokeConsent
)

var ErrInvalidCommand = errors.New("invalid consent command")

// CommandInput contains previously authenticated, scoped command facts.
// Constructing this value does not authenticate the actor or establish ordering.
type CommandInput struct {
	OperationID     OperationID
	SourceCommandID SourceCommandID
	Actor           Principal
	Reference       ProposalReference
	Carrier         ApprovalCarrierID
	Action          ConsentAction
	Order           CommandOrder
}

// Command seals a complete exact-reference approval or revocation observation.
type Command struct {
	input CommandInput
}

func NewCommand(input CommandInput) (Command, error) {
	return Command{}, nil
}

func (c Command) OperationID() OperationID         { return c.input.OperationID }
func (c Command) SourceCommandID() SourceCommandID { return c.input.SourceCommandID }
func (c Command) Actor() Principal                 { return c.input.Actor }
func (c Command) Reference() ProposalReference     { return c.input.Reference }
func (c Command) Carrier() ApprovalCarrierID       { return c.input.Carrier }
func (c Command) Action() ConsentAction            { return c.input.Action }
func (c Command) Order() CommandOrder              { return c.input.Order }
