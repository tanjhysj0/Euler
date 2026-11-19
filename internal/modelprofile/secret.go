package modelprofile

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const keyringService = "Euler"

// ErrSecretNotFound reports that no secret is stored for a reference.
var ErrSecretNotFound = errors.New("secret not found")

// SecretStore reads and writes API keys outside the database.
type SecretStore interface {
	Set(service, value string) error
	Get(service string) (string, error)
	Delete(service string) error
}

// KeychainStore stores secrets in the macOS Keychain under the service name
// "Euler" and the caller provided reference.
type KeychainStore struct{}

// Set writes value for service to the Keychain.
func (KeychainStore) Set(service, value string) error {
	return keyring.Set(keyringService, service, value)
}

// Get reads the secret stored for service from the Keychain.
func (KeychainStore) Get(service string) (string, error) {
	value, err := keyring.Get(keyringService, service)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return value, err
}

// Delete removes the Keychain entry for service.
func (KeychainStore) Delete(service string) error {
	return keyring.Delete(keyringService, service)
}
