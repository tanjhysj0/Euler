package modelprofile

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const keyringService = "Euler"

// ErrSecretNotFound 表示某个引用没有存储对应的密钥。
var ErrSecretNotFound = errors.New("secret not found")

// SecretStore 在数据库之外读写 API key。
type SecretStore interface {
	Set(service, value string) error
	Get(service string) (string, error)
	Delete(service string) error
}

// KeychainStore 以服务名 "Euler" 和调用方提供的引用，
// 将密钥存储在系统密钥环中（go-keyring 跨平台适配：macOS Keychain、
// Windows 凭据管理器、Linux Secret Service）。
type KeychainStore struct{}

// Set 将 service 对应的 value 写入 Keychain。
func (KeychainStore) Set(service, value string) error {
	return keyring.Set(keyringService, service, value)
}

// Get 从 Keychain 读取为 service 存储的密钥。
func (KeychainStore) Get(service string) (string, error) {
	value, err := keyring.Get(keyringService, service)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return value, err
}

// Delete 删除 service 对应的 Keychain 条目。
func (KeychainStore) Delete(service string) error {
	return keyring.Delete(keyringService, service)
}
