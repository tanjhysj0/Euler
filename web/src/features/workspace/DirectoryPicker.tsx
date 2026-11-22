import { Alert, Breadcrumb, Button, Empty, Input, Modal, Spin, Typography, message } from 'antd'
import { useState } from 'react'
import { APIError } from '../../api/client'
import { useCreateDirectory, useDirectoryListing } from '../../api/queries'

interface DirectoryPickerProps {
  open: boolean
  // initialPath 为空表示从用户主目录开始浏览。
  initialPath: string
  confirmLoading?: boolean
  onCancel: () => void
  onSelect: (path: string) => void | Promise<void>
}

// FolderIcon 使用内联 SVG 展示目录图标，避免为了一个图标引入额外依赖。
export function FolderIcon() {
  return (
    <svg className="folder-icon" viewBox="0 0 16 16" aria-hidden="true" focusable="false">
      <path
        d="M1.5 3.75c0-.41.34-.75.75-.75h3.4c.2 0 .39.08.53.22L7.5 4.5h6.25c.41 0 .75.34.75.75v7c0 .41-.34.75-.75.75H2.25a.75.75 0 0 1-.75-.75v-8.5Z"
        fill="currentColor"
      />
    </svg>
  )
}

// splitBreadcrumb 把绝对路径拆成可点击的层级，根目录单独由调用方补上。
function splitBreadcrumb(path: string) {
  const parts = path.split('/').filter((part) => part !== '')
  return parts.map((name, index) => ({
    name,
    path: `/${parts.slice(0, index + 1).join('/')}`,
  }))
}

// DirectoryPicker 通过逐层列出子目录让用户选择工作目录，确认后才回传路径。
export default function DirectoryPicker({
  open,
  initialPath,
  confirmLoading = false,
  onCancel,
  onSelect,
}: DirectoryPickerProps) {
  const [path, setPath] = useState(initialPath)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [prevOpen, setPrevOpen] = useState(open)
  // 每次打开都从默认目录重新开始，避免保留上一次浏览位置。
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (open) {
      setPath(initialPath)
      setCreating(false)
      setNewName('')
    }
  }

  const createDirectory = useCreateDirectory()
  const listingQuery = useDirectoryListing(path, open)
  const listing = listingQuery.data
  const currentPath = listing?.current_path ?? path

  // 新建目录成功后直接进入该目录，方便继续确认或继续新建。
  async function submitNewDirectory() {
    const name = newName.trim()
    if (name === '' || !listing) {
      return
    }
    try {
      const created = await createDirectory.mutateAsync({ parentPath: currentPath, name })
      setCreating(false)
      setNewName('')
      setPath(created.path)
    } catch (err) {
      message.error(err instanceof APIError ? err.message : '创建目录失败')
    }
  }
  const crumbs = [
    {
      title: (
        <button type="button" className="dir-crumb" onClick={() => setPath('/')}>
          /
        </button>
      ),
    },
    ...splitBreadcrumb(currentPath).map((node) => ({
      title: (
        <button type="button" className="dir-crumb" onClick={() => setPath(node.path)}>
          {node.name}
        </button>
      ),
    })),
  ]

  function renderBody() {
    if (listingQuery.isPending) {
      return (
        <div className="dir-picker-state">
          <Spin size="small" />
        </div>
      )
    }
    if (listingQuery.isError) {
      const error = listingQuery.error
      return (
        <Alert
          type="error"
          showIcon
          title={error instanceof APIError ? error.message : '目录不可访问'}
        />
      )
    }
    if (!listing || listing.directories.length === 0) {
      return (
        <div className="dir-picker-state">
          <Empty description="没有子目录" image={Empty.PRESENTED_IMAGE_SIMPLE} />
        </div>
      )
    }
    return (
      <ul className="dir-picker-list">
        {listing.directories.map((directory) => (
          <li key={directory.path}>
            <button
              type="button"
              className="dir-picker-item"
              onClick={() => setPath(directory.path)}
            >
              <FolderIcon />
              <span className="dir-picker-name">{directory.name}</span>
            </button>
          </li>
        ))}
      </ul>
    )
  }

  return (
    <Modal
      open={open}
      title="选择工作目录"
      okText="选择当前目录"
      cancelText="取消"
      width={560}
      confirmLoading={confirmLoading}
      okButtonProps={{ disabled: !listing || listingQuery.isError }}
      onOk={() => void onSelect(currentPath)}
      onCancel={onCancel}
    >
      <div className="dir-picker">
        <div className="dir-picker-head">
          <Breadcrumb className="dir-picker-crumbs" items={crumbs} />
          <Button
            size="small"
            type="text"
            className="dir-picker-new-trigger"
            disabled={!listing}
            onClick={() => setCreating((value) => !value)}
          >
            新建目录
          </Button>
        </div>
        {creating && (
          <div className="dir-picker-new">
            <Input
              size="small"
              autoFocus
              placeholder="新目录名称"
              value={newName}
              onChange={(event) => setNewName(event.target.value)}
              onPressEnter={() => void submitNewDirectory()}
            />
            <Button
              size="small"
              type="primary"
              loading={createDirectory.isPending}
              disabled={newName.trim() === ''}
              onClick={() => void submitNewDirectory()}
            >
              创建
            </Button>
            <Button
              size="small"
              type="text"
              onClick={() => {
                setCreating(false)
                setNewName('')
              }}
            >
              取消
            </Button>
          </div>
        )}
        <div className="dir-picker-body">{renderBody()}</div>
        <Typography.Text type="secondary" className="dir-picker-current">
          {currentPath}
        </Typography.Text>
      </div>
    </Modal>
  )
}
