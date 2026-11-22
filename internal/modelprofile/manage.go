package modelprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

const (
	modelListSuffix   = "/models"
	modelListTimeout  = 15 * time.Second
	modelListMaxBytes = 1 << 20
	noRebound         = 0
)

// modelRow 是兼容 OpenAI 的 /models 响应中的一个模型条目。
type modelRow struct {
	ID string `json:"id"`
}

// updateInput 承载 Update 接受的字段。APIKey 为空表示保留原密钥。
type updateInput struct {
	Name      string
	BaseURL   string
	Model     string
	SecretRef string
	APIKey    string
}

// listModelsInput 描述获取远程模型列表所需的连接参数。APIKey 为空时
// 依次回退到 profileID 指向的已存密钥或环境变量。
type listModelsInput struct {
	BaseURL    string
	APIKey     string
	SecretKind string
	SecretRef  string
	ProfileID  uuid.UUID
}

// Update 修改模型配置。名称、Base URL 与模型不能为空；keychain 配置的
// APIKey 留空时保留原有密钥。
func (s *Service) Update(
	ctx context.Context, id uuid.UUID, input updateInput,
) (Profile, error) {
	row, err := s.repo.Get(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("load model profile: %w", err)
	}
	name := strings.TrimSpace(input.Name)
	baseURL := strings.TrimSpace(input.BaseURL)
	model := strings.TrimSpace(input.Model)
	if name == emptySecret || baseURL == emptySecret || model == emptySecret {
		return Profile{}, fmt.Errorf(
			"%w: name, base url and model are required", ErrInvalidInput,
		)
	}
	row.Name, row.BaseURL, row.Model = name, baseURL, model
	if err := s.applyUpdateSecret(row, input); err != nil {
		return Profile{}, err
	}
	if err := s.repo.Update(ctx, row); err != nil {
		return Profile{}, fmt.Errorf("save model profile: %w", err)
	}
	return toProfile(row), nil
}

// Delete 删除模型配置并返回改绑到默认配置的会话数。
func (s *Service) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	rebound, err := s.repo.Delete(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return noRebound, ErrNotFound
	}
	if err != nil {
		return noRebound, fmt.Errorf("delete model profile: %w", err)
	}
	return rebound, nil
}

// ListModels 使用给定的 Base URL 与凭据拉取兼容 OpenAI 的 /models 列表。
func (s *Service) ListModels(
	ctx context.Context, input listModelsInput,
) ([]string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if baseURL == emptySecret {
		return nil, fmt.Errorf("%w: base url is required", ErrInvalidInput)
	}
	apiKey, err := s.modelListKey(ctx, input)
	if err != nil {
		return nil, err
	}
	return fetchModelIDs(ctx, baseURL, apiKey)
}

func (s *Service) applyUpdateSecret(
	row persistence.ModelProfile, input updateInput,
) error {
	if row.SecretKind == SecretKindEnvironment {
		if strings.TrimSpace(input.APIKey) != emptySecret {
			return fmt.Errorf("%w: api key must be empty", ErrInvalidInput)
		}
		ref := strings.TrimSpace(input.SecretRef)
		if ref == emptySecret {
			return nil
		}
		if !environmentNamePattern.MatchString(ref) {
			return fmt.Errorf("%w: invalid environment variable name", ErrInvalidInput)
		}
		row.SecretRef = ref
		return nil
	}
	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == emptySecret {
		return nil
	}
	if err := s.secrets.Set(row.SecretRef, apiKey); err != nil {
		return fmt.Errorf("%w: keychain write failed: %w", ErrSecretUnavailable, err)
	}
	return nil
}

// modelListKey 解析拉取模型列表使用的 API key，优先使用请求内联值。
func (s *Service) modelListKey(
	ctx context.Context, input listModelsInput,
) (string, error) {
	if apiKey := strings.TrimSpace(input.APIKey); apiKey != emptySecret {
		return apiKey, nil
	}
	if input.ProfileID != uuid.Nil {
		row, err := s.repo.Get(ctx, input.ProfileID)
		if err != nil {
			return emptySecret, fmt.Errorf("%w: load model profile failed", ErrSecretUnavailable)
		}
		return s.secretValue(row.SecretKind, row.SecretRef)
	}
	if input.SecretKind == SecretKindEnvironment && input.SecretRef != emptySecret {
		value := os.Getenv(input.SecretRef)
		if value == emptySecret {
			return emptySecret, fmt.Errorf(
				"%w: environment variable %s is empty", ErrSecretUnavailable, input.SecretRef,
			)
		}
		return value, nil
	}
	return emptySecret, fmt.Errorf("%w: api key is required", ErrInvalidInput)
}

func fetchModelIDs(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, baseURL+modelListSuffix, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: build request failed", ErrModelListUnavailable)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: modelListTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: request failed", ErrModelListUnavailable)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			slog.Warn("close model list response body failed", "error", closeErr)
		}
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf(
			"%w: upstream status %d", ErrModelListUnavailable, response.StatusCode,
		)
	}
	var payload struct {
		Data []modelRow `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, modelListMaxBytes))
	if err = decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: decode failed", ErrModelListUnavailable)
	}
	return uniqueModelIDs(payload.Data), nil
}

func uniqueModelIDs(rows []modelRow) []string {
	seen := make(map[string]struct{}, len(rows))
	models := []string{}
	for _, row := range rows {
		if row.ID == emptySecret {
			continue
		}
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		models = append(models, row.ID)
	}
	slices.Sort(models)
	return models
}
