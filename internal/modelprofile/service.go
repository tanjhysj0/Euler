// Package modelprofile 管理兼容 OpenAI 的模型配置，并将 API key
// 保存在系统密钥环或环境变量中。
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
	// SecretKindKeychain 将 API key 存储在系统密钥环中。
	SecretKindKeychain = "keychain"
	// SecretKindEnvironment 从环境变量读取 API key。
	SecretKindEnvironment = "environment"
	emptySecret           = ""
)

var (
	// ErrInvalidInput 标记无法被接受的模型配置请求。
	ErrInvalidInput = errors.New("invalid model profile input")
	// ErrSecretRefInUse 标记已经存有密钥的 keychain 引用。
	ErrSecretRefInUse = fmt.Errorf("%w: keychain reference already in use", ErrInvalidInput)
	// ErrSecretUnavailable 标记无法读取或写入的密钥。
	ErrSecretUnavailable = errors.New("model profile secret unavailable")
	// ErrNotFound 标记不存在的模型配置。
	ErrNotFound = errors.New("model profile not found")
	// ErrLastProfile 标记删除最后一个模型配置的请求。
	ErrLastProfile = errors.New("at least one model profile is required")
	// ErrModelListUnavailable 标记远程模型列表获取失败。
	ErrModelListUnavailable = errors.New("model list unavailable")

	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// CreateInput 承载 Create 接受的字段。APIKey 只用于写入，
// 绝不会出现在存储的记录行或响应中。
type CreateInput struct {
	Name       string
	BaseURL    string
	Model      string
	SecretKind string
	SecretRef  string
	APIKey     string
}

// Profile 是模型配置的 API 表示形式，不包含其 API key。
type Profile struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	BaseURL    string    `json:"base_url"`
	Model      string    `json:"model"`
	SecretKind string    `json:"secret_kind"`
	SecretRef  string    `json:"secret_ref"`
}

// ResolvedProfile 是模型配置连同其 API key，供单次调用使用。
type ResolvedProfile struct {
	ID      uuid.UUID
	BaseURL string
	Model   string
	APIKey  string
}

// Resolver 向 agent 和 shell 工具提供模型配置。
type Resolver interface {
	Resolve(ctx context.Context, id uuid.UUID) (ResolvedProfile, error)
	EnvironmentSecretNames(ctx context.Context) ([]string, error)
}

// Service 实现模型配置管理和 Resolver。
type Service struct {
	repo    Repository
	secrets SecretStore
}

var _ Resolver = (*Service)(nil)

// NewService 基于给定的仓库和密钥存储构建 Service。
func NewService(repo Repository, secrets SecretStore) *Service {
	return &Service{repo: repo, secrets: secrets}
}

// Create 校验输入，在写入记录行之前先写入 keychain 密钥，
// 当记录行无法保存时会再次删除该密钥。
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

// List 返回所有模型配置，不包含 API key。
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

// Resolve 加载模型配置，并从配置的密钥来源读取其 API key。
// 该 key 只存在于返回值中。
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

// EnvironmentSecretNames 列出环境变量型模型配置引用的去重环境变量名，
// 以便 shell 从子进程中剔除这些变量。
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
