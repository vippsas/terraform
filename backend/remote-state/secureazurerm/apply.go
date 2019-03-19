package secureazurerm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/command/format"
	"github.com/hashicorp/terraform/config/module"
	"github.com/hashicorp/terraform/terraform"
)

// terraform apply
func (b *Backend) apply(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	if op.Plan == nil && op.Module == nil && !op.Destroy {
		runningOp.Err = fmt.Errorf(strings.TrimSpace(applyErrNoConfig))
		return
	}
	if op.Module == nil {
		op.Module = module.NewEmptyTree()
	}

	// Setup our count hook that keeps track of resource changes
	countHook := new(CountHook)
	stateHook := new(StateHook)
	if b.ContextOpts == nil {
		b.ContextOpts = new(terraform.ContextOpts)
	}
	old := b.ContextOpts.Hooks
	defer func() { b.ContextOpts.Hooks = old }()
	b.ContextOpts.Hooks = append(b.ContextOpts.Hooks, countHook, stateHook)

	// Get our context
	tfCtx, opState, err := b.context(op)
	if err != nil {
		runningOp.Err = err
		return
	}

	// Setup the state
	runningOp.State = tfCtx.State()

	if op.Plan == nil {
		if op.PlanRefresh {
			_, err := tfCtx.Refresh()
			if err != nil {
				runningOp.Err = fmt.Errorf("error refreshing state: %s", err)
				return
			}
		}

		// Perform the plan
		plan, err := tfCtx.Plan()
		if err != nil {
			runningOp.Err = fmt.Errorf("error running plan: %s", err)
			return
		}

		dispPlan := format.NewPlan(plan)
		emptyPlan := dispPlan.Empty()
		hasUI := op.UIOut != nil && op.UIIn != nil
		if hasUI && ((op.Destroy && (!op.DestroyForce && !op.AutoApprove)) || (!op.Destroy && !op.AutoApprove && !emptyPlan)) {
			var desc, query string
			if op.Destroy {
				query = "Do you really want to destroy all resources in workspace \"" + op.Workspace + "\"?"
				desc = "Terraform will destroy all your managed infrastructure, as shown above.\n" +
					"⚠There is no undo! It may be irreversible!⚠\n" +
					"Type 'yes' to confirm. Other inputs will cancel the operation."
			} else {
				query = "Do you want to perform these actions in workspace \"" + op.Workspace + "\"?"
				desc = "Terraform will perform the actions, as shown above.\n" +
					"Type 'yes' to confirm. Other inputs will cancel the operation."
			}

			if !emptyPlan {
				// Display the plan of what we are going to apply/destroy.
				b.render(dispPlan)
				b.CLI.Output("")
			}

			v, err := op.UIIn.Input(stopCtx, &terraform.InputOpts{
				Id:          "approve",
				Query:       query,
				Description: desc,
			})
			if err != nil {
				runningOp.Err = fmt.Errorf("error asking for approval: %s", err)
				return
			}
			if v != "yes" {
				if op.Destroy {
					runningOp.Err = errors.New("destroy cancelled")
				} else {
					runningOp.Err = errors.New("apply cancelled")
				}
				return
			}
		}
	}

	// Setup our hook for continuous state updates
	stateHook.State = opState

	// Start the apply in a goroutine so that we can be interrupted.
	var applyState *terraform.State
	var applyErr error
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		_, applyErr = tfCtx.Apply()
		// we always want the state, even if apply failed
		applyState = tfCtx.State()
	}()

	if b.wait(doneCh, stopCtx, cancelCtx, tfCtx, opState) {
		return
	}

	// Store the final state
	runningOp.State = applyState

	// Persist the state
	if err := opState.WriteState(applyState); err != nil {
		// TODO: Output state to CLI.
		runningOp.Err = b.backupStateForError(applyState, err)
		return
	}
	if err := opState.PersistState(); err != nil {
		// TODO: Output state to CLI.
		runningOp.Err = b.backupStateForError(applyState, err)
		return
	}

	if applyErr != nil {
		runningOp.Err = fmt.Errorf(
			"Error applying plan: %s\n\n"+
				"Terraform does not automatically rollback in the face of errors.\n"+
				"Instead, your Terraform state file has been partially updated with\n"+
				"any resources that successfully completed. Please address the error\n"+
				"above and apply again to incrementally change your infrastructure.",
			multierror.Flatten(applyErr))
		return
	}

	// If we have a UI, output the results
	if b.CLI != nil {
		if op.Destroy {
			b.CLI.Output(b.Colorize().Color(fmt.Sprintf(
				"[reset][bold][green]\n"+
					"Destroy complete! Resources: %d destroyed.",
				countHook.Removed)))
		} else {
			b.CLI.Output(b.Colorize().Color(fmt.Sprintf(
				"[reset][bold][green]\n"+
					"Apply complete! Resources: %d added, %d changed, %d destroyed.",
				countHook.Added,
				countHook.Changed,
				countHook.Removed)))
			// TODO: Output state file.
		}
	}
}
