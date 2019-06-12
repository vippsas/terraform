package common

import "encoding/json"

// OutputState contains the state of each output variable.
type OutputState struct {
	ValueRaw     json.RawMessage `json:"value"`
	ValueTypeRaw json.RawMessage `json:"type"`
	Sensitive    bool            `json:"sensitive,omitempty"`
}

// ResourceState contains the state of each resource.
type ResourceState struct {
	Module         string                `json:"module,omitempty"`
	Mode           string                `json:"mode"`
	Type           string                `json:"type"`
	Name           string                `json:"name"`
	EachMode       string                `json:"each,omitempty"`
	ProviderConfig string                `json:"provider"`
	Instances      []InstanceObjectState `json:"instances"`
}

// InstanceObjectState contains the state of each instance of a resource.
type InstanceObjectState struct {
	IndexKey interface{} `json:"index_key,omitempty"`
	Status   string      `json:"status,omitempty"`
	Deposed  string      `json:"deposed,omitempty"`

	SchemaVersion uint64          `json:"schema_version"`
	AttributesRaw json.RawMessage `json:"attributes,omitempty"`

	PrivateRaw []byte `json:"private,omitempty"`

	Dependencies []string `json:"depends_on,omitempty"`
}

// SecureState describes the current state of infrastructure managed by Terraform in JSON.
type SecureState struct {
	Version          string                 `json:"version"`
	TerraformVersion string                 `json:"terraform_version"`
	Serial           uint64                 `json:"serial"`
	Lineage          string                 `json:"lineage"`
	RootOutputs      map[string]OutputState `json:"outputs,omitempty"`
	Resources        []ResourceState        `json:"resources,omitempty"`
}
