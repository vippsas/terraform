package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/services/preview/authorization/mgmt/2018-01-01-preview/authorization"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/keyvault"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/terraform"
	uuid "github.com/satori/go.uuid"
)

// State contains the remote state.
type State struct {
	mu sync.Mutex

	Blob     *blob.Blob         // client to communicate with the state blob storage.
	KeyVault *keyvault.KeyVault // client to communicate with the state key vault.

	Props *properties.Properties

	state, // in-memory state.
	readState *terraform.State // state read from the blob.

	resourceProviders []terraform.ResourceProvider // resource providers used in the configuration.
	secretIDs         map[string]struct{}
}

// State reads the state from the memory.
func (s *State) State() *terraform.State {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state.DeepCopy()
}

// WriteState writes the new state to memory.
func (s *State) WriteState(ts *terraform.State) error {
	// Lock, yay!
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if the new written state has the same lineage as the old previous one.
	if s.readState != nil && !ts.SameLineage(s.readState) {
		// don't err here!
		log.Printf("[WARN] incompatible state lineage: given %s but want %s", ts.Lineage, s.readState.Lineage)
	}

	// Write the state to memory.
	s.state = ts.DeepCopy()
	if s.readState != nil {
		// Fix serial if someone wrote an incorrect serial in the state.
		s.state.Serial = s.readState.Serial
		// Serial is *only* increased when the state is persisted.
	}

	return nil
}

// RefreshState fetches the state from the blob.
func (s *State) RefreshState() error {
	// Lock, milady.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get state data from the blob.
	payload, err := s.Blob.Get()
	if err != nil {
		return fmt.Errorf("error getting state from the blob: %s", err)
	}
	// Check if there is no data in the blob.
	if payload == nil {
		// Sync in-memory state with the empty blob.
		s.state = nil
		s.readState = nil
		// Indicate that the blob contains no state.
		return nil
	}

	// Unmask remote state.
	var stateMap map[string]interface{}
	if err := json.Unmarshal(payload.Data, &stateMap); err != nil {
		return fmt.Errorf("error unmarshalling state to map: %s", err)
	}
	for i, module := range stateMap["modules"].([]interface{}) {
		err = s.unmaskModule(i, module.(map[string]interface{}))
		if err != nil {
			return fmt.Errorf("error unmasking module: %s", err)
		}
	}

	// Convert it back to terraform.State.
	j, err := json.Marshal(stateMap)
	if err != nil {
		return fmt.Errorf("error marshalling map to JSON: %s", err)
	}
	var state terraform.State
	if err := json.Unmarshal(j, &state); err != nil {
		return fmt.Errorf("error unmarshalling JSON to terraform.State: %s", err)
	}
	s.state = &state

	// Make a copy used to track changes.
	s.readState = s.state.DeepCopy()
	return nil
}

// PersistState saves the in-memory state to the blob.
func (s *State) PersistState() error {
	// Lock, harr!
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == nil {
		return errors.New("state is empty")
	}

	// Check for any changes to the in-memory state.
	if !s.state.MarshalEqual(s.readState) {
		s.state.Serial++
	}

	// Put the current in-memory state in a byte buffer.
	var buf bytes.Buffer
	if err := terraform.WriteState(s.state, &buf); err != nil {
		return fmt.Errorf("error writing state to buffer: %s", err)
	}

	// Unmarshal state to map.
	stateMap := make(map[string]interface{})
	json.Unmarshal(buf.Bytes(), &stateMap)

	// List all the secrets from the keyvault.
	var err error
	s.secretIDs, err = s.KeyVault.ListSecrets(context.Background())
	if err != nil {
		return fmt.Errorf("error listing secrets: %s", err)
	}

	// Delete the resource's attributes that does not exists anymore in the key vault.
	resourceAddresses := make(map[string]struct{})
	for _, module := range stateMap["modules"].([]interface{}) {
		for _, resource := range module.(map[string]interface{})["resources"].(map[string]interface{}) {
			for _, attributeValue := range resource.(map[string]interface{})["primary"].(map[string]interface{})["attributes"].(map[string]interface{}) {
				object, ok := attributeValue.(map[string]interface{})
				if ok {
					resourceAddresses[object["id"].(string)] = struct{}{}
				}
			}
		}
	}
	for secretID := range s.secretIDs {
		if _, ok := resourceAddresses[secretID]; !ok {
			if err := s.KeyVault.DeleteSecret(context.Background(), secretID); err != nil {
				return fmt.Errorf("error deleting secret %s: %s", secretID, err)
			}
			delete(s.secretIDs, secretID)
		}
	}

	// Get state key vault's access policies.
	accessPolicies, err := s.KeyVault.GetAccessPolicies(context.Background())
	if err != nil {
		return fmt.Errorf("error getting the state key vault's access policies: %s", err)
	}

	// Remove itself from the access policy list for comparison.
	for i, policy := range accessPolicies {
		if *policy.ObjectID == s.Props.ObjectID {
			accessPolicies = append(accessPolicies[:i], accessPolicies[i+1:]...)
			break
		}
	}

	// Mask sensitive attributes.
	for i, module := range stateMap["modules"].([]interface{}) {
		mod := module.(map[string]interface{})

		// Compare the existing access policies with the current resources in the state. Delete those that does not exist anymore.
		for _, accessPolicy := range accessPolicies {
			for _, resource := range mod["resources"].(map[string]interface{}) {
				attributes := resource.(map[string]interface{})["primary"].(map[string]interface{})["attributes"].(map[string]interface{})
				value, ok := attributes["identity.#"]
				if !ok {
					continue
				}
				length, err := strconv.Atoi(value.(string))
				if err != nil {
					return fmt.Errorf("error converting identity.# to integer: %s", err)
				}
				for i := 0; i < length; i++ {
					if *accessPolicy.ObjectID == attributes[fmt.Sprintf("identity.%d.principal_id", i)].(string) {
						goto end
					}
				}
			}
			err = s.KeyVault.RemoveIDFromAccessPolicies(context.Background(), *accessPolicy.TenantID, *accessPolicy.ObjectID)
			if err != nil {
				return fmt.Errorf("error removing managed ID from access policies: %s", err)
			}
		end:
		}

		// Give resources access to the state as described in the configuration.
		path := mod["path"].([]interface{})
		var stringPath string
		if len(path) > 1 {
			stringPath = path[0].(string) + "."
			for _, s := range path[1:] {
				stringPath = stringPath + "." + s.(string)
			}
		} else {
			stringPath = path[0].(string)
		}
		for _, accessPolicy := range s.Props.AccessPolicies {
			accessPolicyDotSplitted := strings.Split(accessPolicy, ".")
			if strings.Join(accessPolicyDotSplitted[:len(path)], ".") != stringPath {
				continue
			}
			resource, ok := mod["resources"].(map[string]interface{})[strings.Join(accessPolicyDotSplitted[len(path):], ".")]
			if !ok {
				// could not find resource, perhaps due to being destroyed.
				continue
			}
			attributes := resource.(map[string]interface{})["primary"].(map[string]interface{})["attributes"].(map[string]interface{})
			value, ok := attributes["identity.#"]
			if !ok {
				return fmt.Errorf("backend state's access policies contains a resource with no managed identity: %s", err)
			}
			length, err := strconv.Atoi(value.(string))
			if err != nil {
				return fmt.Errorf("error converting identity.# to integer: %s", err)
			}
			roleAssignmentClient := authorization.NewRoleAssignmentsClient(s.Props.SubscriptionID)
			roleAssignmentClient.Authorizer = s.Props.MgmtAuthorizer
			for i := 0; i < length; i++ {
				managedIdentity := keyvault.ManagedIdentity{
					PrincipalID: attributes[fmt.Sprintf("identity.%d.principal_id", i)].(string),
					TenantID:    attributes[fmt.Sprintf("identity.%d.tenant_id", i)].(string),
				}
				s.KeyVault.AddIDToAccessPolicies(context.Background(), &managedIdentity)

				// Assign "Storage Blob Data Reader"-role to the managed identity.
				uuidv1, err := uuid.NewV1()
				if err != nil {
					return fmt.Errorf("error generating UUID V1: %s", err)
				}
				storageBlobDataReaderBuiltInRoleID := fmt.Sprintf(
					"/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/%s",
					s.Props.SubscriptionID, "2a2b9908-6ea1-4ae2-8e65-a410df84e7d1",
				)
				_, err = roleAssignmentClient.Create(
					context.Background(),
					s.Props.StorageAccountResourceID,
					uuidv1.String(),
					authorization.RoleAssignmentCreateParameters{
						RoleAssignmentProperties: &authorization.RoleAssignmentProperties{
							PrincipalID:      &managedIdentity.PrincipalID,
							RoleDefinitionID: &storageBlobDataReaderBuiltInRoleID,
						},
					})
				if err != nil {
					return fmt.Errorf("error assigning the role \"Storage Blob Data Reader\" to the managed ID %s for gaining read-access to the state's storage account: %s", managedIdentity.PrincipalID, err)
				}
			}
		}

		// Then mask the module.
		err = s.maskModule(i, mod)
		if err != nil {
			var paths []string
			for _, s := range path {
				paths = append(paths, s.(string))
			}
			return fmt.Errorf("error masking module %s: %s", strings.Join(paths, "."), err)
		}
	}
	stateMap["keyVaultName"] = s.KeyVault.Name()

	// Marshal state map to JSON.
	data, err := json.MarshalIndent(stateMap, "", "    ")
	if err != nil {
		return fmt.Errorf("error marshalling map: %s", err)
	}
	data = append(data, '\n')

	// Put it into the blob.
	if err := s.Blob.Put(data); err != nil {
		return fmt.Errorf("error leasing and putting buffer: %s", err)
	}

	// Set the persisted state as our new main reference state.
	s.readState = s.state.DeepCopy()

	// Print it.
	fmt.Printf("\nCurrent persisted infrastructure state:\n%s", data)

	return nil
}

// Lock locks the state.
func (s *State) Lock(info *state.LockInfo) (string, error) {
	return s.Blob.Lock(info)
}

// Unlock unlocks the state.
func (s *State) Unlock(id string) error {
	return s.Blob.Unlock(id)
}
