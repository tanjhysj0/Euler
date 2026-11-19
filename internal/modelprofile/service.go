// Package modelprofile manages OpenAI compatible model configuration and keeps
// API keys in the Keychain or in environment variables.
package modelprofile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"euler/internal/persistence"
)

const (
	// SecretKindKeychain stores the API key in the macOS Keychain.
	SecretKindKeychain = "keychain"
	// SecretKindEnvironment reads the API key from an environment variable.
	SecretKindEnvironment = "environment"
	emptySecret           = ""
)

var (
	// ErrInvalidInput marks a model profile request that cannot be accepted.
	ErrInvalidInput = errors.New("invalid model profile input")
	// ErrSecretRefInUse marks a keychain reference that already stores a key.
	ErrSecretRefInUse = fmt.Errorf("%w: keychain reference already in use", ErrInvalidInput)
	// ErrSecretUnavailable marks a secret that cannot be read or written.
	ErrSecretUnavailable = errors.New("model profile secret unavailable")

	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// CreateInput carries the fields accepted by Create. APIKey is write-only and
// is never part of a stored row or a response.
type CreateInput struct {
	Name       string
	BaseURL    string
	Model      string
	SecretKind string
	SecretRef  string
	APIKey     string
}

// Profile is the API representation of a model profile without its API key.
type Profile struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	BaseURL    string    `json:"base_url"`
	Model      string    `json:"model"`
	SecretKind string    `json:"secret_kind"`
	SecretRef  string    `json:"secret_ref"`
}

// ResolvedProfile is a model profile together with its API key for one call.
type ResolvedProfile struct {
	ID      uuid.UUID
	BaseURL string
	Model   string
	APIKey  string
}

// Resolver supplies model profiles to agent and shell tooling.
type Resolver interface {
	Resolve(ctx context.Context, id uuid.UUID) (ResolvedProfile, error)
	EnvironmentSecretNames(ctx context.Context) ([]string, error)
}

// Service implements model profile management and Resolver.
type Service struct {
	repo    Repository
	secrets SecretStore
}

var _ Resolver = (*Service)(nil)

// NewService builds a Service over the given repository and secret store.
func NewService(repo Repository, secrets SecretStore) *Service {
	return &Service{repo: repo, secrets: secrets}
}

// Create validates input, writes keychain secrets before the row and removes
// the secret again when the row cannot be saved.
func (s *Service) Create(ctx context.Context, input CreateInput) (Profile, error) {
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return Profile{}, err
	}
	row := persistence.ModelProfile{
		ID:         uuid.New(),
		Name:       normalized.Name,
		BaseURL:    normalized.BaseURL,
		Model:      normalized.Model,
		SecretKind: normalized.SecretKind,
		SecretRef:  normalized.SecretRef,
	}
	if row.SecretKind == SecretKindKeychain {
		if row.SecretRef == "" {
			row.SecretRef = "profile/" + row.ID.String()
		}
		if err = s.createKeychain(ctx, row, normalized.APIKey); err != nil {
			return Profile{}, err
		}
		return toProfile(row), nil
	}
	if err = s.repo.Create(ctx, row); err != nil {
		return Profile{}, fmt.Errorf("save model profile: %w", err)
	}
	return toProfile(row), nil
}

// List returns every model profile without API keys.
func (s *Service) List(ctx context.Context) ([]Profile, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list model profiles: %w", err)
	}
	profiles := make([]Profile, len(rows))
	for i, row := range rows {
		profiles[i] = toProfile(row)
	}
	return profiles, nil
}

// Resolve loads a profile and reads its API key from the configured secret
// source. The key only exists in the returned value.
func (s *Service) Resolve(ctx context.Context, id uuid.UUID) (ResolvedProfile, error) {
	row, err := s.repo.Get(ctx, id)
	if err != nil {
		return ResolvedProfile{}, fmt.Errorf("load model profile: %w", err)
	}
	apiKey, err := s.secretValue(row.SecretKind, row.SecretRef)
	if err != nil {
		return ResolvedProfile{}, err
	}
	return ResolvedProfile{
		ID:      row.ID,
		BaseURL: row.BaseURL,
		Model:   row.Model,
		APIKey:  apiKey,
	}, nil
}

// EnvironmentSecretNames lists unique environment variables referenced by
// environment backed profiles so shells can strip them from child processes.
func (s *Service) EnvironmentSecretNames(ctx context.Context) ([]string, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list model profiles: %w", err)
	}
	seen := make(map[string]struct{}, len(rows))
	names := []string{}
	for _, row := range rows {
		if row.SecretKind != SecretKindEnvironment {
			continue
		}
		if _, ok := seen[row.SecretRef]; ok {
			continue
		}
		seen[row.SecretRef] = struct{}{}
		names = append(names, row.SecretRef)
	}
	return names, nil
}

func (s *Service) createKeychain(
	ctx context.Context, row persistence.ModelProfile, apiKey string,
) error {
	if err := s.ensureSecretRefAvailable(row.SecretRef); err != nil {
		return err
	}
	apiKey = strings.TrimSpace(apiKey)
	if err := s.secrets.Set(row.SecretRef, apiKey); err != nil {
		return fmt.Errorf("%w: keychain write failed: %w", ErrSecretUnavailable, err)
	}
	if err := s.repo.Create(ctx, row); err != nil {
		if deleteErr := s.secrets.Delete(row.SecretRef); deleteErr != nil {
			return fmt.Errorf("save model profile: %w", errors.Join(err, deleteErr))
		}
		return fmt.Errorf("save model profile: %w", err)
	}
	return nil
}

func (s *Service) ensureSecretRefAvailable(ref string) error {
	_, err := s.secrets.Get(ref)
	if err == nil {
		return ErrSecretRefInUse
	}
	if errors.Is(err, ErrSecretNotFound) {
		return nil
	}
	return fmt.Errorf("%w: keychain read failed: %w", ErrSecretUnavailable, err)
}

func (s *Service) secretValue(kind, ref string) (string, error) {
	if kind == SecretKindEnvironment {
		value := os.Getenv(ref)
		if value == emptySecret {
			return emptySecret, fmt.Errorf(
				"%w: environment variable %s is empty", ErrSecretUnavailable, ref,
			)
		}
		return value, nil
	}
	if kind == SecretKindKeychain {
		value, err := s.secrets.Get(ref)
		if err != nil {
			return emptySecret, fmt.Errorf("%w: keychain read failed", ErrSecretUnavailable)
		}
		return value, nil
	}
	return emptySecret, fmt.Errorf("model profile has unsupported secret kind %q", kind)
}

func normalizeCreateInput(input CreateInput) (CreateInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.Model = strings.TrimSpace(input.Model)
	input.SecretRef = strings.TrimSpace(input.SecretRef)
	if input.Name == emptySecret || input.BaseURL == emptySecret ||
		input.Model == emptySecret {
		return CreateInput{}, fmt.Errorf(
			"%w: name, base url and model are required", ErrInvalidInput,
		)
	}
	switch input.SecretKind {
	case SecretKindKeychain:
		if strings.TrimSpace(input.APIKey) == "" {
			return CreateInput{}, fmt.Errorf("%w: api key is required", ErrInvalidInput)
		}
	case SecretKindEnvironment:
		if input.APIKey != emptySecret {
			return CreateInput{}, fmt.Errorf("%w: api key must be empty", ErrInvalidInput)
		}
		if !environmentNamePattern.MatchString(input.SecretRef) {
			return CreateInput{}, fmt.Errorf(
				"%w: invalid environment variable name", ErrInvalidInput,
			)
		}
	default:
		return CreateInput{}, fmt.Errorf("%w: unsupported secret kind", ErrInvalidInput)
	}
	return input, nil
}

func toProfile(row persistence.ModelProfile) Profile {
	return Profile{
		ID:         row.ID,
		Name:       row.Name,
		BaseURL:    row.BaseURL,
		Model:      row.Model,
		SecretKind: row.SecretKind,
		SecretRef:  row.SecretRef,
	}
}
