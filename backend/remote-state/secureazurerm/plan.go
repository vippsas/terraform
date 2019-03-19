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

func (b *Backend) renderPlan(dispPlan *format.Plan) {
	headerBuf := &bytes.Buffer{}
	fmt.Fprintf(headerBuf, "\n%s\n", strings.TrimSpace(planHeaderIntro))
	counts := dispPlan.ActionCounts()
	if counts[terraform.DiffCreate] > 0 {
		fmt.Fprintf(headerBuf, "%s create\n", format.DiffActionSymbol(terraform.DiffCreate))
	}
	if counts[terraform.DiffUpdate] > 0 {
		fmt.Fprintf(headerBuf, "%s update in-place\n", format.DiffActionSymbol(terraform.DiffUpdate))
	}
	if counts[terraform.DiffDestroy] > 0 {
		fmt.Fprintf(headerBuf, "%s destroy\n", format.DiffActionSymbol(terraform.DiffDestroy))
	}
	if counts[terraform.DiffDestroyCreate] > 0 {
		fmt.Fprintf(headerBuf, "%s destroy and then create replacement\n", format.DiffActionSymbol(terraform.DiffDestroyCreate))
	}
	if counts[terraform.DiffRefresh] > 0 {
		fmt.Fprintf(headerBuf, "%s read (data resources)\n", format.DiffActionSymbol(terraform.DiffRefresh))
	}
	b.CLI.Output(b.Colorize().Color(headerBuf.String()))
	b.CLI.Output("Terraform will perform the following actions:\n")
	b.CLI.Output(dispPlan.Format(b.Colorize()))
	stats := dispPlan.Stats()
	b.CLI.Output(b.Colorize().Color(fmt.Sprintf(
		"[reset][bold]Plan:[reset] %d to add, %d to change, [bold]⚠%d to destroy (irreversibly)⚠[reset].",
		stats.ToAdd, stats.ToChange, stats.ToDestroy,
	)))
}
