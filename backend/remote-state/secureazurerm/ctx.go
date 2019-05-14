package secureazurerm

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/command/clistate"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/terraform"
)

// Context creates a new Terraform Context for the backend.
func (b *Backend) Context(op *backend.Operation) (*terraform.Context, state.State, error) {
	op.StateLocker = clistate.NewLocker(context.Background(), op.StateLockTimeout, b.CLI, b.Colorize())
	return b.context(op)
}

// context is the actual method that creates a new Terraform Context for the backend.
func (b *Backend) context(op *backend.Operation) (*terraform.Context, state.State, error) {
	// Get the state.
	s, err := b.State(op.Workspace)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting state: %s", err)
	}
	// Lock state.
	if err := op.StateLocker.Lock(s, op.Type.String()); err != nil {
		return nil, nil, fmt.Errorf("error locking state: %s", err)
	}
	// Load state from blob storage.
	if err := s.RefreshState(); err != nil {
		return nil, nil, fmt.Errorf("error loading state: %s", err)
	}

	// Initialize our context options
	var opts terraform.ContextOpts
	if b.ContextOpts != nil {
		opts = *b.ContextOpts
	}

	// Copy set options from the operation and load our state.
	opts.Destroy = op.Destroy
	opts.Module = op.Module
	opts.Targets = op.Targets
	opts.UIInput = op.UIIn
	opts.State = s.State()

	// Build the Terraform context.
	var tfCtx *terraform.Context
	if op.Plan != nil {
		tfCtx, err = op.Plan.Context(&opts)
	} else {
		tfCtx, err = terraform.NewContext(&opts)
	}
	if _, ok := err.(*terraform.ResourceProviderError); ok {
		return nil, nil, errors.New("error satisfying plugin requirements")
	}
	if err != nil {
		return nil, nil, err
	}

	return tfCtx, s, nil
}
