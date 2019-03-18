package secureazurerm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"

	armStorage "github.com/Azure/azure-sdk-for-go/services/storage/mgmt/2018-07-01/storage"
	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/command/clistate"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
	"github.com/mitchellh/cli"
	"github.com/mitchellh/colorstring"
)

// Backend maintains the remote state in Azure.
// TODO: Store the backend-configuration in a (separate) container instead of .terraform-dir?
type Backend struct {
	schema.Backend

	// Fields used by Storage Account:
	blobClient    storage.BlobStorageClient
	containerName string
	blobName      string
	leaseID       string

	// CLI shit.
	CLI                 cli.Ui
	CLIColor            *colorstring.Colorize
	ContextOpts         *terraform.ContextOpts
	OpInput             bool
	OpValidation        bool
	RunningInAutomation bool

	mu sync.Mutex
}

// New creates a new backend for remote state stored in Azure storage account and key vault.
func New() backend.Backend {
	b := &Backend{
		Backend: schema.Backend{
			// Fields in backend {}. Ensure that all values are stored only in the configuration files.
			Schema: map[string]*schema.Schema{
				// Resource group:
				"resource_group_name": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The resource group name.",
				},

				// Key Vault:
				"key_vault_name": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The key vault name.",
				},

				// Storage Account:
				"storage_account_name": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The storage account name.",
				},
				"container_name": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The container name.",
				},
			},
		},
	}
	b.Backend.ConfigureFunc = b.configure
	return b
}

// configure bootstraps the Azure resources needed to use this backend.
func (b *Backend) configure(ctx context.Context) error {
	// Get the data attributes from the "backend"-block.
	dataAttrs := schema.FromContextBackendConfig(ctx)

	// Resource Group:
	resourceGroupName := dataAttrs.Get("resource_group_name").(string)
	fmt.Printf("TODO: Provision resource group: %s\n", resourceGroupName)
	// 1. Check if the given resource group exists.
	//   - If not, create it!
	// (idempotent)

	// Azure Key Vault:
	keyVaultName := dataAttrs.Get("key_vault_name").(string)
	fmt.Printf("TODO: Provision key vault: %s\n", keyVaultName)
	// 2. Check if the key vault has been made in the resource group.
	//   - If not, create it!
	// (idempotent)

	// Azure Storage Account:
	storageAccountName := dataAttrs.Get("storage_account_name").(string)
	// 2. Check if the storage account has been made in the resource group.
	//   - If not, create it!
	// (idempotent)

	var subscriptionID string
	// Try authorizing using Azure CLI.
	authorizer, err := auth.NewAuthorizerFromCLI()
	if err != nil {
		// Fetch subscriptionID from environment variable AZURE_SUBSCRIPTION_ID.
		settings, err := auth.GetSettingsFromEnvironment()
		if err != nil {
			return fmt.Errorf("error getting settings from environment: %s", err)
		}
		subscriptionID = settings.GetSubscriptionID()
		if subscriptionID == "" {
			return fmt.Errorf("environment variable %s is not set", auth.SubscriptionID)
		}
		// Authorize using MSI.
		var innerErr error
		authorizer, innerErr = settings.GetMSI().Authorizer()
		if innerErr != nil {
			return fmt.Errorf("error creating authorizer from CLI: %s: error creating authorizer from environment: %s", err, innerErr)
		}
	} else {
		// Fetch subscriptionID from Azure CLI.
		out, err := exec.Command("az", "account", "show", "--output", "json", "--query", "id").Output()
		if err != nil {
			return fmt.Errorf("error fetching subscription id using Azure CLI: %s", err)
		}
		if err = json.Unmarshal(out, &subscriptionID); err != nil {
			return fmt.Errorf("error unmarshalling JSON output from Azure CLI: %s", err)
		}
	}
	accountsClient := armStorage.NewAccountsClient(subscriptionID)
	accountsClient.Authorizer = authorizer

	// Fetch access key for storage account.
	keys, err := accountsClient.ListKeys(ctx, resourceGroupName, storageAccountName)
	if err != nil {
		return fmt.Errorf("error listing the access keys in the storage account %q: %s", storageAccountName, err)
	}
	if keys.Keys == nil {
		return fmt.Errorf("no keys returned from storage account %q", storageAccountName)
	}
	accessKey1 := *(*keys.Keys)[0].Value
	if accessKey1 == "" {
		return errors.New("missing access key")
	}

	// Create new storage account client using fetched access key.
	storageClient, err := storage.NewBasicClient(storageAccountName, accessKey1)
	if err != nil {
		return fmt.Errorf("error creating client for storage account %q: %s", storageAccountName, err)
	}

	// Check if the given container exists.
	blobService := storageClient.GetBlobService()
	b.containerName = dataAttrs.Get("container_name").(string)
	resp, err := blobService.ListContainers(storage.ListContainersParameters{Prefix: b.containerName, MaxResults: 1})
	if err != nil {
		return fmt.Errorf("error listing containers: %s", err)
	}
	for _, container := range resp.Containers {
		if container.Name == b.containerName {
			b.blobClient = blobService
			return nil // success!
		}
	}
	// TODO: Create container if it does not exists.
	return fmt.Errorf("cannot find container: %s", b.containerName)
}

const containerNameNotSetErrorMsg = "container name is not set"

// States returns a list of the names of all remote states stored in separate unique blob.
// They are all named after the workspace.
// Basically, remote state = workspace = blob.
func (b *Backend) States() ([]string, error) {
	if b.containerName == "" {
		return nil, errors.New(containerNameNotSetErrorMsg)
	}

	// Get blobs of container.
	r, err := b.blobClient.GetContainerReference(b.containerName).ListBlobs(storage.ListBlobsParameters{})
	if err != nil {
		return nil, err
	}

	// List workspaces (which is equivalent to blobs) in the container.
	workspaces := []string{}
	for _, blob := range r.Blobs {
		workspaces = append(workspaces, blob.Name)
	}
	sort.Strings(workspaces[1:]) // default is placed first in the returned list.
	return workspaces, nil
}

// DeleteState deletes remote state.
func (b *Backend) DeleteState(name string) error {
	if b.containerName == "" {
		return errors.New(containerNameNotSetErrorMsg)
	}

	if name == backend.DefaultStateName {
		return errors.New("can't delete default state")
	}
	c := &Client{
		blobClient:    b.blobClient,
		containerName: b.containerName,
		blobName:      name, // workspace name.
	}
	lockInfo := state.NewLockInfo()
	lockInfo.Operation = "DeleteState"
	leaseID, err := c.Lock(lockInfo)
	if err != nil {
		return fmt.Errorf("error locking blob: %s", err)
	}
	if err = c.Delete(); err != nil {
		if err := c.Unlock(leaseID); err != nil {
			return fmt.Errorf("error unlocking blob (may need to be manually broken): %s", err)
		}
		return fmt.Errorf("error deleting blob: %s", err)
	}
	return nil
}

// State returns remote state specified by name.
func (b *Backend) State(name string) (state.State, error) {
	if b.containerName == "" {
		return nil, errors.New(containerNameNotSetErrorMsg)
	}

	c := &Client{
		blobClient:    b.blobClient,
		containerName: b.containerName,
		blobName:      name, // workspace name.
	}
	s := &remote.State{Client: c}

	// Check if blob exists.
	exists, err := c.Exists()
	if err != nil {
		return nil, fmt.Errorf("error checking blob existence: %s", err)
	}
	// If not exists, write empty state blob (no need for lock when the blob does not exists).
	if !exists {
		// Create new state in-memory.
		if err := s.WriteState(terraform.NewState()); err != nil {
			return nil, fmt.Errorf("error creating new state in-memory: %s", err)
		}
		// Write that in-memory state to remote state.
		if err := s.PersistState(); err != nil {
			return nil, fmt.Errorf("error writing in-memory state to remote: %s", err)
		}
	}

	return s, nil
}

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

// CLIInit inits CLI.
func (b *Backend) CLIInit(opts *backend.CLIOpts) error {
	b.CLI = opts.CLI                 // neckbeard cli.
	b.CLIColor = opts.CLIColor       // i <3 my colors.
	b.ContextOpts = opts.ContextOpts // muh context.

	b.OpInput = opts.Input           // i want user input or not.
	b.OpValidation = opts.Validation // i want to validate or not.

	b.RunningInAutomation = opts.RunningInAutomation // i am running in automation tool with no user interaction or not.
	return nil
}

// Colorize makes the CLI output colored text.
func (b *Backend) Colorize() *colorstring.Colorize {
	if b.CLIColor != nil {
		return b.CLIColor
	}
	return &colorstring.Colorize{
		Colors:  colorstring.DefaultColors,
		Disable: false, // ofc, we want color.
	}
}

// terraform refresh
func (b *Backend) refresh(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	panic("todo")
}

// terraform plan
func (b *Backend) plan(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	panic("todo")
}

// terraform apply
func (b *Backend) apply(stopCtx context.Context, cancelCtx context.Context, op *backend.Operation, runningOp *backend.RunningOperation) {
	panic("todo")
}
