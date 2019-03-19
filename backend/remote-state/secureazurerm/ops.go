package secureazurerm

import (
	"context"
	"fmt"
	"log"

	"github.com/hashicorp/errwrap"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/command/clistate"
	"github.com/hashicorp/terraform/config/module"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/terraform"
)

// Operation TODO!
func (b *Backend) Operation(ctx context.Context, op *backend.Operation) (*backend.RunningOperation, error) {
	var f func(context.Context, context.Context, *backend.Operation, *backend.RunningOperation)
	switch op.Type {
	case backend.OperationTypeRefresh:
		f = b.refresh
	case backend.OperationTypePlan:
		f = b.plan
	case backend.OperationTypeApply:
		f = b.apply
	default:
		return nil, fmt.Errorf("unsupported operation type: %s", op.Type.String())
	}

	// Prepare
	b.mu.Lock()

	runningCtx, done := context.WithCancel(context.Background())
	runningOp := &backend.RunningOperation{Context: runningCtx}

	stopCtx, stop := context.WithCancel(ctx)
	runningOp.Stop = stop

	cancelCtx, cancel := context.WithCancel(context.Background())
	runningOp.Cancel = cancel

	if op.LockState {
		op.StateLocker = clistate.NewLocker(stopCtx, op.StateLockTimeout, b.CLI, b.Colorize())
	} else {
		op.StateLocker = clistate.NewNoopLocker()
	}

	// Do it
	go func() { // Terraform wants to do the operations in a goroutine.
		defer done()
		defer stop()
		defer cancel()
		defer func() {
			runningOp.Err = op.StateLocker.Unlock(runningOp.Err)
		}()
		defer b.mu.Unlock()

		f(stopCtx, cancelCtx, op, runningOp)
	}()

	return runningOp, nil
}

// opWait waits for the operation to complete, and a stop signal or a
// cancelation signal.
func (b *Backend) wait(doneCh <-chan struct{}, stopCtx context.Context, cancelCtx context.Context, tfCtx *terraform.Context, opState state.State) (canceled bool) {
	// Wait for the operation to finish or for us to be interrupted so we can handle it properly.
	select {
	case <-stopCtx.Done():
		if b.CLI != nil {
			b.CLI.Output("stopping operation...")
		}

		// Try to force a PersistState just in case the process is terminated before we can complete.
		if err := opState.PersistState(); err != nil {
			// We can't error out from here, but warn the user if there was an error.
			// If this isn't transient, we will catch it again below, and
			// attempt to save the state another way.
			if b.CLI != nil {
				b.CLI.Error(fmt.Sprintf(earlyStateWriteErrorFmt, err))
			}
		}

		// Stop execution
		go tfCtx.Stop()
		select {
		case <-cancelCtx.Done():
			log.Println("[WARN] Running operation was canceled.")
			// if the operation was canceled, we need to return immediately
			canceled = true
		case <-doneCh:
		}
	case <-cancelCtx.Done():
		// This should not be called without first attempting to stop the operation.
		log.Println("[ERROR] Running operation was canceled without stopping.")
		canceled = true
	case <-doneCh:
	}
	return
}

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

// terraform plan
func (b *Backend) plan(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	panic("todo")
}

// terraform apply
func (b *Backend) apply(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	panic("todo")
}
