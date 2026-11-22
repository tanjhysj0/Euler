import { Alert, Button, Empty, Tree } from 'antd'
import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
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

// buildTree 把后端扁平路径列表组装成树；路径排序保证父目录先于子节点出现。
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
  return sortTree(roots)
}

// sortTree 递归排序：目录在前、文件在后，同类按名称排序，贴近 IDE 习惯。
function sortTree(nodes: TreeNode[]): TreeNode[] {
  nodes.sort((a, b) => {
    if (a.isLeaf !== b.isLeaf) {
      return a.isLeaf ? 1 : -1
    }
    return a.title.localeCompare(b.title)
  })
  for (const node of nodes) {
    if (node.children) {
      sortTree(node.children)
    }
  }
  return nodes
}

// collectDirKeys 收集全部目录 key，作为首次渲染的默认展开集合。
function collectDirKeys(nodes: TreeNode[]): string[] {
  const keys: string[] = []
  for (const node of nodes) {
    if (node.isLeaf) {
      continue
    }
    keys.push(node.key)
    if (node.children) {
      keys.push(...collectDirKeys(node.children))
    }
  }
  return keys
}

// FilePanel 以 IDE 风格展示工作区文件树：目录点击展开/收起，文件点击预览。
// 头部、树与预览区的颜色和间距由主题 CSS 统一接管。
export default function FilePanel({ workspaceId }: FilePanelProps) {
  const queryClient = useQueryClient()
  const filesQuery = useWorkspaceFiles(workspaceId)
  const [selected, setSelected] = useState<string | null>(null)
  // null 表示尚未手动操作，使用“全部展开”的默认值；操作后保持用户选择。
  const [expandedKeys, setExpandedKeys] = useState<string[] | null>(null)
  const tree = useMemo(() => buildTree(filesQuery.data ?? []), [filesQuery.data])

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
    return <Empty description="暂无文件，运行任务后产物会出现在这里" />
  }
  const effectiveExpanded = expandedKeys ?? collectDirKeys(tree)
  return (
    <div className="file-panel">
      <div className="file-panel-head">
        <span>工作区文件</span>
        <Button
          size="small"
          type="text"
          onClick={() => void queryClient.invalidateQueries({ queryKey: ['files', workspaceId] })}
        >
          刷新
        </Button>
      </div>
      <Tree
        className="file-tree"
        blockNode
        treeData={tree}
        expandedKeys={effectiveExpanded}
        selectedKeys={selected === null ? [] : [selected]}
        onExpand={(keys) => setExpandedKeys(keys.map(String))}
        onSelect={(keys, info) => {
          if (!info.node.isLeaf) {
            // 点击目录只切换展开状态，不把它当作预览目标。
            const key = String(info.node.key)
            setExpandedKeys(
              effectiveExpanded.includes(key)
                ? effectiveExpanded.filter((item) => item !== key)
                : [...effectiveExpanded, key],
            )
            return
          }
          if (keys.length > 0) {
            setSelected(String(keys[0]))
          }
        }}
      />
      {selected !== null && (
        <div className="file-preview">
          <span className="file-preview-path">{selected}</span>
          <FilePreview workspaceId={workspaceId} path={selected} />
        </div>
      )}
    </div>
  )
}
