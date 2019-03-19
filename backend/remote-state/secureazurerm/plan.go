package secureazurerm

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/command/format"
	"github.com/hashicorp/terraform/config/module"
	"github.com/hashicorp/terraform/terraform"
)

// plan performs "terraform plan"
func (b *Backend) plan(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	// A local plan requires either a plan or a module
	if op.Plan == nil && op.Module == nil && !op.Destroy {
		runningOp.Err = fmt.Errorf(strings.TrimSpace(planErrNoConfig))
		return
	}

	// If we have a nil module at this point, then set it to an empty tree to avoid any potential crashes.
	if op.Module == nil {
		op.Module = module.NewEmptyTree()
	}

	// Setup our count hook that keeps track of resource changes
	countHook := new(CountHook)
	if b.ContextOpts == nil {
		b.ContextOpts = new(terraform.ContextOpts)
	}
	old := b.ContextOpts.Hooks
	defer func() { b.ContextOpts.Hooks = old }()
	b.ContextOpts.Hooks = append(b.ContextOpts.Hooks, countHook)

	// Get our context
	tfCtx, opState, err := b.context(op)
	if err != nil {
		runningOp.Err = err
		return
	}

	// Setup the state
	runningOp.State = tfCtx.State()

	// If we're refreshing before plan, perform that
	if op.PlanRefresh {
		if b.CLI != nil {
			b.CLI.Output(b.Colorize().Color(strings.TrimSpace(planRefreshing) + "\n"))
		}
		_, err := tfCtx.Refresh()
		if err != nil {
			runningOp.Err = fmt.Errorf("error refreshing state: %s", err)
			return
		}
		if b.CLI != nil {
			b.CLI.Output("\n------------------------------------------------------------------------")
		}
	}

	// Perform the plan in a goroutine so we can be interrupted
	var plan *terraform.Plan
	var planErr error
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		plan, planErr = tfCtx.Plan()
	}()
	if b.wait(doneCh, stopCtx, cancelCtx, tfCtx, opState) {
		return
	}
	if planErr != nil {
		runningOp.Err = fmt.Errorf("error running plan: %s", planErr)
		return
	}
	runningOp.PlanEmpty = plan.Diff.Empty()

	if b.CLI != nil {
		dispPlan := format.NewPlan(plan)
		if dispPlan.Empty() {
			b.CLI.Output("\n" + b.Colorize().Color(strings.TrimSpace(planNoChanges)))
			return
		}
		b.render(dispPlan)
		b.CLI.Output("\n------------------------------------------------------------------------")

		const noGuaranteeMsg = `
		Note: Terraform can't guarantee that exactly these actions will be performed if
		"terraform apply" is subsequently run.
		`
		b.CLI.Output(fmt.Sprintf("\n"+strings.TrimSpace(noGuaranteeMsg)+"\n", path, path))
	}
}

// render renders terraform plan.
func (b *Backend) render(plan *format.Plan) {
	// Render introductary header.
	const planHeaderIntro = `
An execution plan has been generated and is shown below.
Resource actions are indicated with the following symbols:
`
	header := &bytes.Buffer{}
	fmt.Fprintf(header, "\n%s\n", strings.TrimSpace(planHeaderIntro))
	counts := plan.ActionCounts()
	if counts[terraform.DiffCreate] > 0 {
		fmt.Fprintf(header, "%s: create new resource\n", format.DiffActionSymbol(terraform.DiffCreate))
	}
	if counts[terraform.DiffUpdate] > 0 {
		fmt.Fprintf(header, "%s: update in-place\n", format.DiffActionSymbol(terraform.DiffUpdate))
	}
	if counts[terraform.DiffDestroy] > 0 {
		fmt.Fprintf(header, "%s: destroy existing resource\n", format.DiffActionSymbol(terraform.DiffDestroy))
	}
	if counts[terraform.DiffDestroyCreate] > 0 {
		fmt.Fprintf(header, "%s: destroy and then create new replacement resource\n", format.DiffActionSymbol(terraform.DiffDestroyCreate))
	}
	if counts[terraform.DiffRefresh] > 0 {
		fmt.Fprintf(header, "%s read data resources\n", format.DiffActionSymbol(terraform.DiffRefresh))
	}
	b.CLI.Output(b.Colorize().Color(header.String()))

	// Render plan.
	b.CLI.Output("Terraform will perform the following actions:\n")
	b.CLI.Output(plan.Format(b.Colorize()))

	// Render number of actions.
	stats := plan.Stats()
	b.CLI.Output(b.Colorize().Color(fmt.Sprintf("[reset] %d to add, %d to change, [bold]⚠%d to destroy (irreversibly)⚠[reset].",
		stats.ToAdd, stats.ToChange, stats.ToDestroy,
	)))
}
