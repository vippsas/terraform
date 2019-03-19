package secureazurerm

import (
	"context"

	"github.com/hashicorp/errwrap"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/config/module"
	"github.com/hashicorp/terraform/terraform"
)

// refresh implements "terraform refresh"
func (b *Backend) refresh(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	// If we have no config module given to use, create an empty tree to avoid crashes when Terraform.Context is initialized.
	if op.Module == nil {
		op.Module = module.NewEmptyTree()
	}

	// Get our context
	tfCtx, opState, err := b.context(op)
	if err != nil {
		runningOp.Err = err
		return
	}

	// Set our state
	runningOp.State = opState.State()
	if runningOp.State.Empty() || !runningOp.State.HasResources() {
		if b.CLI != nil {
			b.CLI.Output(b.Colorize().Color("[reset][bold][yellow]Empty remote state.[reset][yellow]\n"))
		}
	}

	// Perform the refresh in a goroutine so we can be interrupted
	var newState *terraform.State
	var refreshErr error
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		newState, refreshErr = tfCtx.Refresh()
	}()

	if b.wait(doneCh, stopCtx, cancelCtx, tfCtx, opState) {
		return
	}

	// Write the resulting state to the running operation.
	runningOp.State = newState
	if refreshErr != nil {
		runningOp.Err = errwrap.Wrapf("Error refreshing state: {{err}}", refreshErr)
		return
	}

	// Save state to storage account.
	if err := opState.WriteState(newState); err != nil {
		runningOp.Err = errwrap.Wrapf("Error writing state: {{err}}", err)
		return
	}
	if err := opState.PersistState(); err != nil {
		runningOp.Err = errwrap.Wrapf("Error saving state: {{err}}", err)
		return
	}
}
