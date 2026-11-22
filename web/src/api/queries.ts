import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { APIError, apiFetch, apiRawFetch } from './client'
import type {
  ApprovalDecisionInput,
  ApprovalDecisionRecord,
  CreateModelProfileInput,
  CreateSessionInput,
  CreateSessionResult,
  DeleteModelProfileResult,
  DirectoryListing,
  FileNode,
  ListModelsInput,
  MessagePage,
  MessageView,
  ModelListResult,
  ModelProfile,
  RunSnapshot,
  SessionView,
  StartRunResult,
  UpdateModelProfileInput,
  WorkspaceDirectory,
} from './types'

export interface SessionListFilter {
  q?: string
  workspacePath?: string
  activeRun?: boolean
}

export function sessionListPath(filter: SessionListFilter = {}): string {
  const params = new URLSearchParams()
  if (filter.q) {
    params.set('q', filter.q)
  }
  if (filter.workspacePath) {
    params.set('workspace_path', filter.workspacePath)
  }
  if (filter.activeRun) {
    params.set('active_run', 'true')
  }
  const query = params.toString()
  return query ? `/sessions?${query}` : '/sessions'
}

export function useSessionList(filter: SessionListFilter = {}, enabled = true) {
  return useQuery({
    queryKey: ['sessions', filter],
    queryFn: () => apiFetch<SessionView[]>(sessionListPath(filter)),
    enabled,
  })
}

export function useSession(id: string | undefined) {
  return useQuery({
    queryKey: ['sessions', 'detail', id],
    queryFn: () => apiFetch<SessionView>(`/sessions/${id}`),
    enabled: Boolean(id),
  })
}

export function useCreateSession() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateSessionInput) =>
      apiFetch<CreateSessionResult>('/sessions', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['sessions'] }),
  })
}

// useUpdateSessionModel 切换会话的模型配置；沿用后端返回的会话视图刷新缓存。
export function useUpdateSessionModel(sessionId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (profileId: string) =>
      apiFetch<SessionView>(`/sessions/${sessionId}`, {
        method: 'PATCH',
        body: JSON.stringify({ model_profile_id: profileId }),
      }),
    onSuccess: (session) => {
      queryClient.setQueryData(['sessions', 'detail', sessionId], session)
      void queryClient.invalidateQueries({ queryKey: ['sessions'] })
    },
  })
}

// useDirectoryListing 逐层浏览本机目录，path 为空时后端回退到用户主目录。
export function useDirectoryListing(path: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['fs', 'directories', path ?? ''],
    queryFn: () => {
      const query = path ? `?path=${encodeURIComponent(path)}` : ''
      return apiFetch<DirectoryListing>(`/fs/directories${query}`)
    },
    enabled,
    // 目录内容变化频繁，弹窗重新打开时需要拿到最新结果。
    staleTime: 0,
  })
}

// useCreateDirectory 在指定父目录下创建一个单层子目录，成功后刷新该目录列表。
export function useCreateDirectory() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ parentPath, name }: { parentPath: string; name: string }) =>
      apiFetch<WorkspaceDirectory>('/fs/directories', {
        method: 'POST',
        body: JSON.stringify({ parent_path: parentPath, name }),
      }),
    onSuccess: (_directory, variables) => {
      void queryClient.invalidateQueries({
        queryKey: ['fs', 'directories', variables.parentPath],
      })
    },
  })
}

// useUpdateSessionWorkspace 切换会话的工作目录；历史消息保持不变，
// 后续 Run 与文件树使用新目录。
export function useUpdateSessionWorkspace(sessionId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (workspacePath: string) =>
      apiFetch<SessionView>(`/sessions/${sessionId}`, {
        method: 'PATCH',
        body: JSON.stringify({ workspace_path: workspacePath }),
      }),
    onSuccess: (session) => {
      queryClient.setQueryData(['sessions', 'detail', sessionId], session)
      void queryClient.invalidateQueries({ queryKey: ['sessions'] })
      void queryClient.invalidateQueries({ queryKey: ['files'] })
    },
  })
}

// useDeleteSession 删除会话；后端要求会话没有进行中的 Run。
export function useDeleteSession() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (sessionId: string) =>
      apiFetch<void>(`/sessions/${sessionId}`, { method: 'DELETE' }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['sessions'] }),
  })
}

export function useModelProfiles() {
  return useQuery({
    queryKey: ['model-profiles'],
    queryFn: () => apiFetch<ModelProfile[]>('/model-profiles'),
  })
}

export function useCreateModelProfile() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateModelProfileInput) =>
      apiFetch<ModelProfile>('/model-profiles', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['model-profiles'] }),
  })
}

// useUpdateModelProfile 修改模型配置；api_key 留空时后端保留原密钥。
export function useUpdateModelProfile() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateModelProfileInput }) =>
      apiFetch<ModelProfile>(`/model-profiles/${id}`, {
        method: 'PUT',
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['model-profiles'] })
      void queryClient.invalidateQueries({ queryKey: ['sessions'] })
    },
  })
}

// useDeleteModelProfile 删除模型配置；后端会把引用它的会话改绑到默认模型。
export function useDeleteModelProfile() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<DeleteModelProfileResult>(`/model-profiles/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['model-profiles'] })
      void queryClient.invalidateQueries({ queryKey: ['sessions'] })
    },
  })
}

// useListAvailableModels 使用表单里的 Base URL 与 API Key 远程拉取模型列表。
export function useListAvailableModels() {
  return useMutation({
    mutationFn: (input: ListModelsInput) =>
      apiFetch<ModelListResult>('/model-profiles/models', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
  })
}

// useWorkspaceFiles 拉取工作区产物文件树；文件修改与 Run 终态事件会失效该查询。
export function useWorkspaceFiles(workspaceId: string) {
  return useQuery({
    queryKey: ['files', workspaceId],
    queryFn: () => apiFetch<FileNode[]>(`/workspaces/${workspaceId}/files`),
  })
}

export interface FileContent {
  contentType: string
  text: string
}

// useFileContent 通过受保护 content API 读取预览文本；图片不走该查询。
export function useFileContent(workspaceId: string, path: string, enabled: boolean) {
  return useQuery({
    queryKey: ['files', workspaceId, 'content', path],
    queryFn: async (): Promise<FileContent> => {
      const response = await apiRawFetch(
        `/workspaces/${workspaceId}/files/content?path=${encodeURIComponent(path)}`,
      )
      return {
        contentType: response.headers.get('content-type') ?? '',
        text: await response.text(),
      }
    },
    enabled,
  })
}

// useMessages 按 session_seq 分页拉取全部历史消息，next_after 为 0 表示没有下一页。
export function useMessages(sessionId: string) {
  return useQuery({
    queryKey: ['messages', sessionId],
    queryFn: async () => {
      const messages: MessageView[] = []
      let after = 0
      for (;;) {
        const page = await apiFetch<MessagePage>(
          `/sessions/${sessionId}/messages?after=${after}&limit=200`,
        )
        messages.push(...page.messages)
        if (!page.next_after) {
          return messages
        }
        after = page.next_after
      }
    },
  })
}

export function useRunSnapshot(runId: string | undefined) {
  return useQuery({
    queryKey: ['runs', runId],
    queryFn: () => apiFetch<RunSnapshot>(`/runs/${runId}`),
    enabled: Boolean(runId),
  })
}

export function useStartRun(sessionId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (content: string) =>
      apiFetch<StartRunResult>(`/sessions/${sessionId}/runs`, {
        method: 'POST',
        body: JSON.stringify({ content }),
      }),
    onSuccess: (result) => {
      queryClient.setQueryData(['runs', result.run.id], result)
      void queryClient.invalidateQueries({ queryKey: ['messages', sessionId] })
      void queryClient.invalidateQueries({ queryKey: ['sessions'] })
    },
  })
}

// 取消成功后立即用返回的快照更新缓存并重取，保证界面马上反映终态。
export function useCancelRun(runId: string | undefined) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => apiFetch<RunSnapshot>(`/runs/${runId}/cancel`, { method: 'POST' }),
    onSuccess: (snapshot) => {
      queryClient.setQueryData(['runs', runId], snapshot)
      void queryClient.invalidateQueries({ queryKey: ['runs', runId] })
      void queryClient.invalidateQueries({ queryKey: ['messages'] })
      void queryClient.invalidateQueries({ queryKey: ['sessions'] })
    },
  })
}

// 审批决策成功或 409（已被处理）都重取 Run 快照，让权威状态接管界面。
export function useApprovalDecision(runId: string | undefined) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: ApprovalDecisionInput) =>
      apiFetch<ApprovalDecisionRecord>(`/approvals/${input.approvalId}/decision`, {
        method: 'POST',
        body: JSON.stringify({ approved: input.approved, reason: input.reason ?? '' }),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['runs', runId] })
    },
    onError: (error) => {
      if (error instanceof APIError && error.status === 409) {
        void queryClient.invalidateQueries({ queryKey: ['runs', runId] })
      }
    },
  })
}
