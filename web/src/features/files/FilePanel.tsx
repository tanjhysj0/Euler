import { Alert, Empty, Space, Tree } from 'antd'
import { useState } from 'react'
import { APIError } from '../../api/client'
import { useWorkspaceFiles } from '../../api/queries'
import type { FileNode } from '../../api/types'
import FilePreview from './FilePreview'

interface FilePanelProps {
  workspaceId: string
}

interface TreeNode {
  key: string
  title: string
  isLeaf: boolean
  children?: TreeNode[]
}

// buildTree 把后端扁平路径列表组装成 antd Tree 数据；
// 按路径排序保证父目录先于子节点出现。
function buildTree(nodes: FileNode[]): TreeNode[] {
  const dirs = new Map<string, TreeNode>()
  const roots: TreeNode[] = []
  const sorted = [...nodes].sort((a, b) => a.path.localeCompare(b.path))
  for (const node of sorted) {
    const parts = node.path.split('/')
    const item: TreeNode = {
      key: node.path,
      title: parts[parts.length - 1],
      isLeaf: node.type === 'file',
    }
    if (node.type === 'dir') {
      item.children = []
      dirs.set(node.path, item)
    }
    const parent = dirs.get(parts.slice(0, -1).join('/'))
    if (parent?.children) {
      parent.children.push(item)
    } else {
      roots.push(item)
    }
  }
  return roots
}

// FilePanel 展示工作区产物文件树，选中文件后在下方预览。
export default function FilePanel({ workspaceId }: FilePanelProps) {
  const filesQuery = useWorkspaceFiles(workspaceId)
  const [selected, setSelected] = useState<string | null>(null)
  if (filesQuery.isError) {
    const error = filesQuery.error
    return (
      <Alert
        type="error"
        showIcon
        title={error instanceof APIError ? error.message : '加载文件树失败'}
      />
    )
  }
  if (!filesQuery.data) {
    return <Empty description="加载文件树中…" />
  }
  if (filesQuery.data.length === 0) {
    return <Empty description="工作区暂无文件" />
  }
  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Tree
        treeData={buildTree(filesQuery.data)}
        defaultExpandAll
        onSelect={(keys, info) => {
          if (info.node.isLeaf && keys.length > 0) {
            setSelected(String(keys[0]))
          }
        }}
      />
      {selected !== null && <FilePreview workspaceId={workspaceId} path={selected} />}
    </Space>
  )
}
