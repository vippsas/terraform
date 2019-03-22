package ops

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/terraform"
)

// wait waits for an operation to complete, a stop signal or a cancelation signal.
func (b *Backend) wait(doneCh <-chan struct{}, stopCtx context.Context, cancelCtx context.Context, tfCtx *terraform.Context, opState state.State) bool {
	// Wait for the operation to finish or for us to be interrupted so we can handle it properly.
	select {
	case <-stopCtx.Done():
		if b.CLI != nil {
			b.CLI.Output("[WARN] Stopping operation...")
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

		// Stop the operation.
		go tfCtx.Stop()
		select {
		case <-cancelCtx.Done():
			if b.CLI != nil {
				b.CLI.Output("[WARN] Running operation was canceled.")
			}
			return true
		case <-doneCh:
		}
	case <-cancelCtx.Done(): // Don't call without first attempting to stop the operation.
		if b.CLI != nil {
			b.CLI.Output("[ERROR] Running operation was canceled without stopping.")
		}
		return true
	case <-doneCh:
	}
	return false
}

const earlyStateWriteErrorFmt = `Error saving current state: %s

Terraform encountered an error attempting to save the state before stopping
the current operation. Once the operation is complete another attempt will be
made to save the final state.
`
