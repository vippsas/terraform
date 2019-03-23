package secureazurerm

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/backend/local"
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

	// Setup our count hook that keeps track of resource changes.
	// bao: It still works without. It seems it is never used at this stage.
	if b.ContextOpts == nil {
		b.ContextOpts = new(terraform.ContextOpts)
	}
	old := b.ContextOpts.Hooks
	defer func() { b.ContextOpts.Hooks = old }()
	b.ContextOpts.Hooks = append(b.ContextOpts.Hooks, new(local.CountHook))

	// Get our context
	tfCtx, opState, err := b.context(op)
	if err != nil {
		runningOp.Err = err
		return
	}

	// Setup the state
	runningOp.State = tfCtx.State()

	// Always refresh before plan.
	if _, err := tfCtx.Refresh(); err != nil {
		runningOp.Err = fmt.Errorf("error refreshing state: %s", err)
		return
	}

	// Perform the plan in a goroutine so we can be interrupted
	var plan *terraform.Plan
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		plan, err = tfCtx.Plan()
	}()
	if b.wait(doneCh, stopCtx, cancelCtx, tfCtx, opState) {
		return
	}
	if err != nil {
		runningOp.Err = fmt.Errorf("error planning: %s", err)
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
		b.CLI.Output(fmt.Sprintf("\n" + strings.TrimSpace(noGuaranteeMsg) + "\n"))
	}
}

const noGuaranteeMsg = `
Terraform cannot guarantee that exactly these actions will be performed if
"terraform apply" is subsequently run.
`

// render renders terraform plan.
func (b *Backend) render(plan *format.Plan) {
	// Render intro header.
	header := &bytes.Buffer{}
	fmt.Fprintf(header, "%s\n", planHeaderIntro)
	counts := plan.ActionCounts()
	if counts[terraform.DiffCreate] > 0 {
		fmt.Fprintf(header, "%s: Create a new resource in Azure.\n", format.DiffActionSymbol(terraform.DiffCreate))
	}
	if counts[terraform.DiffUpdate] > 0 {
		fmt.Fprintf(header, "%s: Update a resource in-place in Azure.\n", format.DiffActionSymbol(terraform.DiffUpdate))
	}
	if counts[terraform.DiffDestroy] > 0 {
		fmt.Fprintf(header, "%s: Destroy an existing resource in Azure.\n", format.DiffActionSymbol(terraform.DiffDestroy))
	}
	if counts[terraform.DiffDestroyCreate] > 0 {
		fmt.Fprintf(header, "%s: Destroy and then create a new replacement resource in Azure.\n", format.DiffActionSymbol(terraform.DiffDestroyCreate))
	}
	if counts[terraform.DiffRefresh] > 0 {
		fmt.Fprintf(header, "%s: Read data from a resource in Azure.\n", format.DiffActionSymbol(terraform.DiffRefresh))
	}
	b.CLI.Output(b.Colorize().Color(header.String()))

	// Render plan.
	b.CLI.Output("Terraform will perform the following actions:\n")
	b.CLI.Output(plan.Format(b.Colorize()))

	// Render number of actions.
	stats := plan.Stats()
	if stats.ToDestroy > 0 {
		b.CLI.Output(b.Colorize().Color(fmt.Sprintf("[reset]%d to add, %d to change, [bold][red]️%d to (irreversibly) destroy![reset]",
			stats.ToAdd, stats.ToChange, stats.ToDestroy,
		)))
	} else {
		b.CLI.Output(b.Colorize().Color(fmt.Sprintf("[reset]%d to add, %d to change, %d to destroy.[reset]",
			stats.ToAdd, stats.ToChange, stats.ToDestroy,
		)))
	}
}

const planHeaderIntro = `An execution plan has been generated and is shown below.
Actions are indicated with the following symbols:
`

const planErrNoConfig = `
No configuration files found!

Plan requires configuration to be present. Planning without a configuration
would mark everything for destruction, which is normally not what is desired.
If you would like to destroy everything, please run plan with the "-destroy"
flag or create a single empty configuration file. Otherwise, please create
a Terraform configuration file in the path being executed and try again.
`

const planNoChanges = `
[reset][bold][green]No changes. Infrastructure is up-to-date.[reset][green]

This means that Terraform did not detect any differences between your
configuration and real physical resources that exist. As a result, no
actions need to be performed.
`
