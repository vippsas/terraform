package secureazurerm

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/command/format"
	"github.com/hashicorp/terraform/terraform"
)

// plan performs "terraform plan"
func (b *Backend) plan(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	panic("todo")
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
		fmt.Fprintf(header, "%s: destroy and then create replacement resource\n", format.DiffActionSymbol(terraform.DiffDestroyCreate))
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
	b.CLI.Output(b.Colorize().Color(fmt.Sprintf("[reset] %d to add, %d to change, [bold]⚠%d to destroy (may be irreversible)⚠[reset].",
		stats.ToAdd, stats.ToChange, stats.ToDestroy,
	)))
}
