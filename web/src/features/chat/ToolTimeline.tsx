import { Alert, Button, Card, Empty, Space, Tag, Typography } from 'antd'
import type { RunSnapshot, ToolCallStatus } from '../../api/types'
import { useRunStore } from '../../stores/run'
import ApprovalCard from './ApprovalCard'

interface ToolTimelineProps {
  snapshot: RunSnapshot | undefined
  onDecide: (approvalId: string, approved: boolean, reason: string) => Promise<void>
}

const statusLabels: Record<ToolCallStatus, { label: string; color: string }> = {
  pending: { label: '等待中', color: 'default' },
  waiting_approval: { label: '等待审批', color: 'warning' },
  running: { label: '执行中', color: 'processing' },
  succeeded: { label: '成功', color: 'success' },
  failed: { label: '失败', color: 'error' },
  rejected: { label: '已拒绝', color: 'warning' },
  cancelled: { label: '已取消', color: 'default' },
}

// summarize 把参数/结果 JSON 截断为摘要，避免长时间线撑爆界面。
function summarize(value: unknown): string {
  if (value === null || value === undefined) {
    return ''
  }
  const text = typeof value === 'string' ? value : JSON.stringify(value)
  return text.length > 200 ? `${text.slice(0, 200)}…` : text
}

// ToolTimeline 展示工具调用卡片与待审批请求；拒绝的工具显示 rejected，
// 不把整个 Run 标红为失败。
export default function ToolTimeline({ snapshot, onDecide }: ToolTimelineProps) {
  const collapsed = useRunStore((state) => state.timelineCollapsed)
  const setCollapsed = useRunStore((state) => state.setTimelineCollapsed)
  if (!snapshot || (snapshot.tool_calls.length === 0 && snapshot.approvals.length === 0)) {
    return null
  }
  const pendingApprovals = snapshot.approvals.filter((approval) => approval.status === 'pending')
  return (
    <Space orientation="vertical" size="small" style={{ width: '100%' }}>
      <Button size="small" onClick={() => setCollapsed(!collapsed)}>
        {collapsed ? '展开工具时间线' : '收起工具时间线'}
      </Button>
      {!collapsed && (
        <>
          {pendingApprovals.map((approval) => (
            <ApprovalCard
              key={approval.id}
              approval={approval}
              name={snapshot.tool_calls.find((tool) => tool.id === approval.tool_call_id)?.name}
              onDecide={onDecide}
            />
          ))}
          {snapshot.tool_calls.length === 0 && pendingApprovals.length === 0 && (
            <Empty description="暂无工具调用" />
          )}
          {snapshot.tool_calls.map((tool) => {
            // 后端新增状态时兜底展示原始值，避免整条时间线崩溃。
            const status = statusLabels[tool.status] ?? { label: tool.status, color: 'default' }
            const args = summarize(tool.arguments_json)
            const result = summarize(tool.result_json)
            return (
              <Card
                key={tool.id}
                size="small"
                title={tool.name}
                extra={<Tag color={status.color}>{status.label}</Tag>}
              >
                <Space orientation="vertical" size="small" style={{ width: '100%' }}>
                  {args && <Typography.Text type="secondary">参数：{args}</Typography.Text>}
                  {result && <Typography.Text type="secondary">结果：{result}</Typography.Text>}
                  {tool.error_message && (
                    <Alert type="error" showIcon title={tool.error_message} />
                  )}
                </Space>
              </Card>
            )
          })}
        </>
      )}
    </Space>
  )
}
