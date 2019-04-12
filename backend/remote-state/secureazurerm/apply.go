package secureazurerm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/backend/local"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote"
	"github.com/hashicorp/terraform/command/format"
	"github.com/hashicorp/terraform/config/module"
	"github.com/hashicorp/terraform/terraform"
)

// getResourceProviders returns all the resource providers for the given configuration.
func getResourceProviders(c *terraform.Context) []terraform.ResourceProvider {
	var providers []terraform.ResourceProvider
	components := c.GetComponents()

	for _, id := range components.ResourceProviders() {
		p, err := components.ResourceProvider(id, "")
		if err != nil {
			continue
		}
		providers = append(providers, p)
	}

	return providers
}

// apply does "terraform apply".
func (b *Backend) apply(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	if op.Plan == nil && op.Module == nil && !op.Destroy {
		runningOp.Err = fmt.Errorf(strings.TrimSpace(applyErrNoConfig))
		return
	}
	if op.Module == nil {
		op.Module = module.NewEmptyTree()
	}

	// Setup our count hook that keeps track of resource changes
	countHook := new(local.CountHook)
	stateHook := new(local.StateHook)
	if b.ContextOpts == nil {
		b.ContextOpts = new(terraform.ContextOpts)
	}
	old := b.ContextOpts.Hooks
	defer func() { b.ContextOpts.Hooks = old }()
	b.ContextOpts.Hooks = append(b.ContextOpts.Hooks, countHook, stateHook)

	// Get our context
	tfCtx, remoteState, err := b.context(op)
	if err != nil {
		runningOp.Err = err
		return
	}

	// Setup the state
	runningOp.State = tfCtx.State()

	// Always refresh before plan.
	_, err = b.informBeforeRefresh(tfCtx)
	if err != nil {
		runningOp.Err = fmt.Errorf("error refreshing state: %s", err)
		return
	}

	// Generate a plan.
	plan, err := tfCtx.Plan()
	if err != nil {
		runningOp.Err = fmt.Errorf("error planning: %s", err)
		return
	}
	dispPlan := format.NewPlan(plan)
	emptyPlan := dispPlan.Empty()

	// Ask user to confirm performing the actions in the plan.
	if (op.UIOut != nil && op.UIIn != nil) && ((op.Destroy && (!op.DestroyForce && !op.AutoApprove)) || (!op.Destroy && !op.AutoApprove && !emptyPlan)) {
		var desc, query string
		if op.Destroy {
			query = "Do you really want to destroy all resources in the workspace \"" + op.Workspace + "\"?"
			desc = "Terraform will destroy all your managed infrastructure, as shown above.\n" +
				"[reset][bold][red]There is no undo! It may be irreversible![reset]\n\n" +
				"Type 'yes' to confirm. Other inputs will cancel the operation."
		} else {
			query = "Do you want to perform these actions in the workspace \"" + op.Workspace + "\"?"
			desc = "Terraform will perform the actions, as shown above.\n\n" +
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
			runningOp.Err = fmt.Errorf("error asking for confirmation: %s", err)
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

	// Set resource providers for masking sensitive attributes in remote state.
	blobState := remoteState.(*remote.State)
	blobState.SetResourceProviders(getResourceProviders(tfCtx))

	// Setup our hook for continuous state updates.
	stateHook.State = remoteState

	// Begin the "apply" (in a goroutine so that we can be interrupted).
	var applyState *terraform.State
	var applyErr error
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		_, applyErr = tfCtx.Apply()
		// Fetch state, even if apply failed.
		applyState = tfCtx.State()
	}()
	// Wait for it to finish.
	if b.wait(doneCh, stopCtx, cancelCtx, tfCtx, remoteState) {
		return
	}
	// Store the final state.
	runningOp.State = applyState
	// Write the state to memory.
	if err := remoteState.WriteState(applyState); err != nil {
		// TODO: Output state to CLI.
		//runningOp.Err = b.backupStateForError(applyState, err)
		runningOp.Err = fmt.Errorf("error writing state in-memory: %s", err)
		return
	}
	// Save the state to remote.
	if err := remoteState.PersistState(); err != nil {
		// TODO: Output state to CLI.
		//runningOp.Err = b.backupStateForError(applyState, err)
		runningOp.Err = fmt.Errorf("error persisting state: %s", err)
		return
	}
	// Check if "apply" failed.
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
			b.ColorOutput(
				fmt.Sprintf("[reset][bold][green]\n%d destroyed.\n\nDestroy complete!",
					countHook.Removed))
		} else {
			b.ColorOutput(
				fmt.Sprintf("[reset][bold][green]\n%d added, %d changed, %d destroyed.\n\nApply complete!",
					countHook.Added, countHook.Changed, countHook.Removed))
		}
	}
}

const applyErrNoConfig = `
No configuration files found!

Apply requires configuration to be present. Applying without a configuration
would mark everything for destruction, which is normally not what is desired.
If you would like to destroy everything, please run 'terraform destroy' which
does not require any configuration files.
`
