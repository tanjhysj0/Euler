package modelprofile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/persistence"
)

const environmentProfileID = "00000000-0000-0000-0000-000000000004"

func TestCreateKeychainProfileDoesNotPersistSecret(t *testing.T) {
	secrets := newFakeSecretStore()
	repo := newFakeProfileRepository()
	svc := NewService(repo, secrets)
	profile, err := svc.Create(context.Background(), CreateInput{
		Name: "test", BaseURL: "https://api.example/v1", Model: "demo",
		SecretKind: "keychain", SecretRef: "euler/test", APIKey: "sk-super-secret",
	})
	require.NoError(t, err)
	encoded, err := json.Marshal(profile)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "sk-super-secret")
	require.Equal(t, "sk-super-secret", secrets.values["euler/test"])
	require.NotContains(t, repo.lastJSON, "sk-super-secret")
}

func TestResolveEnvironmentProfile(t *testing.T) {
	t.Setenv("EULER_TEST_MODEL_KEY", "sk-from-env")
	svc := NewService(newFakeProfileRepository(), newFakeSecretStore())
	resolved, err := svc.Resolve(context.Background(), uuid.MustParse(environmentProfileID))
	require.NoError(t, err)
	require.Equal(t, "sk-from-env", resolved.APIKey)
}

func TestCreateEnvironmentProfileRejectsInlineAPIKey(t *testing.T) {
	svc := NewService(newFakeProfileRepository(), newFakeSecretStore())
	_, err := svc.Create(context.Background(), CreateInput{
		Name: "env", BaseURL: "https://api.example/v1", Model: "demo",
		SecretKind: SecretKindEnvironment, SecretRef: "EULER_TEST_MODEL_KEY",
		APIKey: "sk-must-not-be-stored",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestCreateEnvironmentProfileRejectsInvalidSecretRef(t *testing.T) {
	svc := NewService(newFakeProfileRepository(), newFakeSecretStore())
	_, err := svc.Create(context.Background(), CreateInput{
		Name: "env", BaseURL: "https://api.example/v1", Model: "demo",
		SecretKind: SecretKindEnvironment, SecretRef: "1INVALID-NAME",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestCreateKeychainProfileRemovesSecretOnRepositoryFailure(t *testing.T) {
	secrets := newFakeSecretStore()
	repo := newFakeProfileRepository()
	repo.createErr = errors.New("database down")
	svc := NewService(repo, secrets)
	_, err := svc.Create(context.Background(), CreateInput{
		Name: "test", BaseURL: "https://api.example/v1", Model: "demo",
		SecretKind: SecretKindKeychain, SecretRef: "euler/failing", APIKey: "sk-rollback",
	})
	require.Error(t, err)
	require.NotContains(t, secrets.values, "euler/failing")
	require.Equal(t, []string{"euler/failing"}, secrets.deleted)
}

func TestCreateKeychainProfileWithoutSecretFails(t *testing.T) {
	svc := NewService(newFakeProfileRepository(), newFakeSecretStore())
	_, err := svc.Create(context.Background(), CreateInput{
		Name: "test", BaseURL: "https://api.example/v1", Model: "demo",
		SecretKind: SecretKindKeychain, SecretRef: "euler/empty",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestCreateKeychainProfileRejectsExistingSecretRef(t *testing.T) {
	secrets := newFakeSecretStore()
	secrets.values["euler/existing"] = "sk-original"
	repo := newFakeProfileRepository()
	svc := NewService(repo, secrets)
	_, err := svc.Create(context.Background(), CreateInput{
		Name: "test", BaseURL: "https://api.example/v1", Model: "demo",
		SecretKind: SecretKindKeychain, SecretRef: "euler/existing", APIKey: "sk-replacement",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
	require.ErrorIs(t, err, ErrSecretRefInUse)
	require.Equal(t, "sk-original", secrets.values["euler/existing"])
	require.Zero(t, secrets.setCalls)
	require.Empty(t, repo.lastJSON)
}

func TestCreateKeychainProfileTrimsAPIKey(t *testing.T) {
	secrets := newFakeSecretStore()
	svc := NewService(newFakeProfileRepository(), secrets)
	_, err := svc.Create(context.Background(), CreateInput{
		Name: "test", BaseURL: "https://api.example/v1", Model: "demo",
		SecretKind: SecretKindKeychain, SecretRef: "euler/trimmed", APIKey: "  sk-trimmed  ",
	})
	require.NoError(t, err)
	require.Equal(t, "sk-trimmed", secrets.values["euler/trimmed"])
}

func TestCreateEnvironmentProfileRejectsWhitespaceAPIKey(t *testing.T) {
	svc := NewService(newFakeProfileRepository(), newFakeSecretStore())
	_, err := svc.Create(context.Background(), CreateInput{
		Name: "env", BaseURL: "https://api.example/v1", Model: "demo",
		SecretKind: SecretKindEnvironment, SecretRef: "EULER_TEST_MODEL_KEY",
		APIKey: "   ",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestResolveKeychainProfile(t *testing.T) {
	repo := newFakeProfileRepository()
	repo.profiles = append(repo.profiles, persistence.ModelProfile{
		ID:         uuid.MustParse("00000000-0000-0000-0000-000000000005"),
		Name:       "keychain",
		BaseURL:    "https://api.example/v1",
		Model:      "demo",
		SecretKind: SecretKindKeychain,
		SecretRef:  "euler/keychain",
	})
	secrets := newFakeSecretStore()
	secrets.values["euler/keychain"] = "sk-keychain"
	svc := NewService(repo, secrets)
	resolved, err := svc.Resolve(context.Background(), repo.profiles[1].ID)
	require.NoError(t, err)
	require.Equal(t, "sk-keychain", resolved.APIKey)
	require.Equal(t, repo.profiles[1].BaseURL, resolved.BaseURL)
	require.Equal(t, repo.profiles[1].Model, resolved.Model)
}

func TestResolveMissingEnvironmentVariableFails(t *testing.T) {
	t.Setenv("EULER_TEST_MODEL_KEY", "")
	svc := NewService(newFakeProfileRepository(), newFakeSecretStore())
	_, err := svc.Resolve(context.Background(), uuid.MustParse(environmentProfileID))
	require.ErrorIs(t, err, ErrSecretUnavailable)
}

func TestEnvironmentSecretNamesAreUnique(t *testing.T) {
	repo := newFakeProfileRepository()
	repo.profiles = append(repo.profiles, repo.profiles[0])
	svc := NewService(repo, newFakeSecretStore())
	names, err := svc.EnvironmentSecretNames(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"EULER_TEST_MODEL_KEY"}, names)
}

func TestListReturnsProfilesWithoutKeychainSecrets(t *testing.T) {
	repo := newFakeProfileRepository()
	svc := NewService(repo, newFakeSecretStore())
	profiles, err := svc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, len(repo.profiles))
	require.Equal(t, repo.profiles[0].SecretRef, profiles[0].SecretRef)
}

type fakeProfileRepository struct {
	profiles  []persistence.ModelProfile
	lastJSON  string
	createErr error
}

func newFakeProfileRepository() *fakeProfileRepository {
	return &fakeProfileRepository{profiles: []persistence.ModelProfile{{
		ID:         uuid.MustParse(environmentProfileID),
		Name:       "environment",
		BaseURL:    "https://api.example/v1",
		Model:      "demo",
		SecretKind: SecretKindEnvironment,
		SecretRef:  "EULER_TEST_MODEL_KEY",
	}}}
}

func (f *fakeProfileRepository) Create(_ context.Context, profile persistence.ModelProfile) error {
	encoded, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	f.lastJSON = string(encoded)
	if f.createErr != nil {
		return f.createErr
	}
	f.profiles = append(f.profiles, profile)
	return nil
}

func (f *fakeProfileRepository) List(_ context.Context) ([]persistence.ModelProfile, error) {
	return append([]persistence.ModelProfile(nil), f.profiles...), nil
}

func (f *fakeProfileRepository) Get(
	_ context.Context, id uuid.UUID,
) (persistence.ModelProfile, error) {
	for _, profile := range f.profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return persistence.ModelProfile{}, errors.New("model profile not found")
}

type fakeSecretStore struct {
	values    map[string]string
	setErr    error
	getErr    error
	deleteErr error
	deleted   []string
	setCalls  int
}

func newFakeSecretStore() *fakeSecretStore {
	return &fakeSecretStore{values: map[string]string{}}
}

func (f *fakeSecretStore) Set(service, value string) error {
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	f.values[service] = value
	return nil
}

func (f *fakeSecretStore) Get(service string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[service]
	if !ok {
		return "", ErrSecretNotFound
	}
	return value, nil
}

func (f *fakeSecretStore) Delete(service string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.values, service)
	f.deleted = append(f.deleted, service)
	return nil
}
