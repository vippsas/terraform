package secureazurerm

import (
	"context"

	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/command/clistate"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/terraform"
)

// Context creates a new Terraform Context for the backend.
func (b *Backend) Context(op *backend.Operation) (*terraform.Context, state.State, error) {
	// Make sure the type is invalid. We use this as a way to know not
	// to ask for input/validate.
	op.Type = backend.OperationTypeInvalid

	if op.LockState {
		op.StateLocker = clistate.NewLocker(context.Background(), op.StateLockTimeout, b.CLI, b.Colorize())
	} else {
		op.StateLocker = clistate.NewNoopLocker()
	}

	return b.context(op)
}
