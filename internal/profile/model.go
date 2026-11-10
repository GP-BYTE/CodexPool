package profile

import "time"

const SchemaVersion = 1

type Lifecycle string

const (
	LifecyclePending            Lifecycle = "pending"
	LifecycleReady              Lifecycle = "ready"
	LifecycleReauthRequired     Lifecycle = "reauthRequired"
	LifecycleLogoutPending      Lifecycle = "logoutPending"
	LifecyclePurgeLogoutPending Lifecycle = "purgeLogoutPending"
	LifecycleDeleting           Lifecycle = "deleting"
)

type CredentialStore string

const (
	StoreFile    CredentialStore = "file"
	StoreKeyring CredentialStore = "keyring"
)

type Identity struct {
	Email            string    `json:"email,omitempty"`
	BackendAccountID string    `json:"backendAccountId,omitempty"`
	PlanType         string    `json:"planType,omitempty"`
	ObservedAt       time.Time `json:"observedAt,omitempty"`
}

type Profile struct {
	ID              string          `json:"id"`
	Alias           string          `json:"alias"`
	CreatedAt       time.Time       `json:"createdAt"`
	CodexHome       string          `json:"codexHome"`
	CredentialStore CredentialStore `json:"credentialStore"`
	AuthGeneration  uint64          `json:"authGeneration"`
	Lifecycle       Lifecycle       `json:"lifecycle"`
	Identity        Identity        `json:"identity,omitempty"`
}

type Registry struct {
	SchemaVersion   int       `json:"schemaVersion"`
	Revision        uint64    `json:"revision"`
	ActiveProfileID string    `json:"activeProfileId,omitempty"`
	Profiles        []Profile `json:"profiles"`
}
