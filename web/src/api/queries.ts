import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { APIError, apiFetch, apiRawFetch } from './client'
import type {
  ApprovalDecisionInput,
  ApprovalDecisionRecord,
  CreateModelProfileInput,
  CreateSessionInput,
  CreateSessionResult,
  DirectoryListing,
  FileNode,
  MessagePage,
  MessageView,
  ModelProfile,
  RunSnapshot,
  SessionView,
  StartRunResult,
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

// 目录浏览只调用 /fs/directories，不读取文件内容。
export function useDirectoryListing(path: string, enabled: boolean) {
  return useQuery({
    queryKey: ['fs-directories', path],
    queryFn: () =>
      apiFetch<DirectoryListing>(`/fs/directories?path=${encodeURIComponent(path)}`),
    enabled,
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
