package secureazurerm

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/command/clistate"
)

// Operation does the operation in a goroutine.
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

	// Setup operation contexts.
	b.mu.Lock()
	runningCtx, done := context.WithCancel(context.Background())
	runningOp := &backend.RunningOperation{Context: runningCtx}
	stopCtx, stop := context.WithCancel(ctx)
	runningOp.Stop = stop
	cancelCtx, cancel := context.WithCancel(context.Background())
	runningOp.Cancel = cancel
	op.StateLocker = clistate.NewLocker(stopCtx, op.StateLockTimeout, b.cli.CLI, b.cli.Colorize())

	// Do the operation!
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
