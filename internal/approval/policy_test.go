package approval

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// newPolicyRequest 构造一个满足 Gate 校验要求的最小审批请求。
func newPolicyRequest(name string) Request {
	return Request{
		RunID:      uuid.New(),
		ToolCallID: uuid.New(),
		Name:       name,
	}
}

func TestAutoApproveApprovesEveryRequest(t *testing.T) {
	gate := AutoApprove{}
	decision, err := gate.Require(context.Background(), newPolicyRequest("shell"))
	require.NoError(t, err)
	require.True(t, decision.Approved)
	require.Equal(t, StatusApproved, decision.Status)
}

func TestDenyAllRejectsEveryRequest(t *testing.T) {
	gate := DenyAll{}
	decision, err := gate.Require(context.Background(), newPolicyRequest("file_write"))
	require.NoError(t, err)
	require.False(t, decision.Approved)
	require.Equal(t, StatusRejected, decision.Status)
}

func TestScriptedApprovesOnlyNamedTools(t *testing.T) {
	gate := NewScripted(map[string]bool{"shell": true})
	allowed, err := gate.Require(context.Background(), newPolicyRequest("shell"))
	require.NoError(t, err)
	require.True(t, allowed.Approved)
	require.Equal(t, StatusApproved, allowed.Status)

	denied, err := gate.Require(context.Background(), newPolicyRequest("file_delete"))
	require.NoError(t, err)
	require.False(t, denied.Approved)
	require.Equal(t, StatusRejected, denied.Status)
}

func TestScriptedRecordsEveryRequest(t *testing.T) {
	gate := NewScripted(map[string]bool{"shell": true, "file_patch": true})
	requests := []Request{
		newPolicyRequest("shell"),
		newPolicyRequest("file_patch"),
		newPolicyRequest("file_delete"),
	}
	for _, request := range requests {
		_, err := gate.Require(context.Background(), request)
		require.NoError(t, err)
	}
	require.Equal(t, requests, gate.Requests())
}

func TestPoliciesRejectInvalidRequest(t *testing.T) {
	invalid := Request{}
	_, err := AutoApprove{}.Require(context.Background(), invalid)
	require.ErrorIs(t, err, ErrInvalidRequest)
	_, err = DenyAll{}.Require(context.Background(), invalid)
	require.ErrorIs(t, err, ErrInvalidRequest)
	_, err = NewScripted(nil).Require(context.Background(), invalid)
	require.ErrorIs(t, err, ErrInvalidRequest)
}
