package remote

type KeyVault struct {
}

func New() KeyVault {
	return KeyVault{}
}

// Insert inserts a secret into the key vault. Returns the version.
func (k *KeyVault) Insert(secret string) string {
	return ""
}
