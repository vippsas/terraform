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

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/keyvault"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/terraform"
)

// State contains the remote state.
type State struct {
	mu sync.Mutex

	Blob     *blob.Blob         // client to communicate with the state blob storage.
	KeyVault *keyvault.KeyVault // client to communicate with the state key vault.

	AccessPolicies *[]string
	Owner          string

	state, // in-memory state.
	readState *terraform.State // state read from the blob.

	resourceProviders []terraform.ResourceProvider
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
		s.unmaskModule(i, module.(map[string]interface{}))
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

	// Unmarshall to state to map.
	stateMap := make(map[string]interface{})
	json.Unmarshal(buf.Bytes(), &stateMap)

	// Mask sensitive attributes.
	for i, module := range stateMap["modules"].([]interface{}) {
		mod := module.(map[string]interface{})
		path := mod["path"].([]interface{})

		// Get state key vault's access policies.
		accessPolicies, err := s.KeyVault.GetAccessPolicies(context.Background())
		if err != nil {
			panic(err)
		}

		// Delete access policies for service principals that does not exists anymore.
		// - Remove itself from the access policy list.
		for i, policy := range accessPolicies {
			if *policy.ObjectID == s.Owner {
				accessPolicies = append(accessPolicies[:i], accessPolicies[i+1:]...)
				break
			}
		}
		// - Compare the existing access policies with the resources in the state. Delete those that does not exists.
		for _, accessPolicy := range accessPolicies {
			for _, resource := range mod["resources"].(map[string]interface{}) {
				attributes := resource.(map[string]interface{})["primary"].(map[string]interface{})["attributes"].(map[string]interface{})
				value, ok := attributes["identity.#"]
				if !ok {
					continue
				}
				length, err := strconv.Atoi(value.(string))
				if err != nil {
					panic(err)
				}
				for i := 0; i < length; i++ {
					if *accessPolicy.ObjectID == attributes[fmt.Sprintf("identity.%d.principal_id", i)].(string) {
						goto end
					}
				}
			}
			err = s.KeyVault.RemoveIDFromAccessPolicies(context.Background(), *accessPolicy.TenantID, *accessPolicy.ObjectID)
			if err != nil {
				panic(err)
			}
		end:
		}

		// Add access policies to state key vault given in the configuration.
		var stringPath string
		if len(path) > 1 {
			stringPath = path[0].(string) + "."
			for _, s := range path[1:] {
				stringPath = stringPath + "." + s.(string)
			}
		} else {
			stringPath = path[0].(string)
		}

		for _, accessPolicy := range *s.AccessPolicies {
			accessPolicyDotSplitted := strings.Split(accessPolicy, ".")
			if strings.Join(accessPolicyDotSplitted[:len(path)], ".") == stringPath {
				resourceName := strings.Join(accessPolicyDotSplitted[len(path):], ".")
				resource, ok := mod["resources"].(map[string]interface{})[resourceName]
				if !ok {
					// could not find resource.
					continue
				}
				attributes := resource.(map[string]interface{})["primary"].(map[string]interface{})["attributes"].(map[string]interface{})
				length, err := strconv.Atoi(attributes["identity.#"].(string))
				if err != nil {
					panic(err)
				}
				for i := 0; i < length; i++ {
					managedIdentity := keyvault.ManagedIdentity{
						PrincipalID: attributes[fmt.Sprintf("identity.%d.principal_id", i)].(string),
						TenantID:    attributes[fmt.Sprintf("identity.%d.tenant_id", i)].(string),
					}
					s.KeyVault.AddIDToAccessPolicies(context.Background(), &managedIdentity)
				}
			}
		}

		s.maskModule(i, mod)
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
