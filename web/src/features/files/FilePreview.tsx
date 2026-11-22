import { Alert, Button, Empty, Space } from 'antd'
import { APIError } from '../../api/client'
import { useFileContent } from '../../api/queries'
import MarkdownView from '../../components/MarkdownView'

interface FilePreviewProps {
  workspaceId: string
  path: string
}

const imageExtensions = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'])

// contentURL 指向受保护的 content API；预览与下载共用，下载追加 download=true。
function contentURL(workspaceId: string, path: string): string {
  return `/api/v1/workspaces/${workspaceId}/files/content?path=${encodeURIComponent(path)}`
}

function isTextType(contentType: string): boolean {
  return (
    contentType.startsWith('text/') ||
    contentType.includes('json') ||
    contentType.includes('xml') ||
    contentType.includes('javascript')
  )
}

// FilePreview 按类型预览产物：图片直接走受保护 URL，Markdown 用消息渲染器，
// 文本用代码视图，其余（含超限错误）提供下载链接。
export default function FilePreview({ workspaceId, path }: FilePreviewProps) {
  const extension = path.split('.').pop()?.toLowerCase() ?? ''
  const isImage = imageExtensions.has(extension)
  const contentQuery = useFileContent(workspaceId, path, !isImage)
  const url = contentURL(workspaceId, path)
  const download = (
    <Button className="file-download" href={`${url}&download=true`}>
      下载
    </Button>
  )
  if (isImage) {
    return (
      <Space className="file-image" orientation="vertical" style={{ width: '100%' }}>
        <img src={url} alt={path} style={{ maxWidth: '100%' }} />
        {download}
      </Space>
    )
  }
  if (contentQuery.isError) {
    const error = contentQuery.error
    return (
      <Alert
        type="error"
        showIcon
        title={error instanceof APIError ? error.message : '读取文件失败'}
        action={download}
      />
    )
  }
  if (!contentQuery.data) {
    return <Empty description="加载文件中…" />
  }
  if (extension === 'md') {
    return <MarkdownView content={contentQuery.data.text} />
  }
  if (isTextType(contentQuery.data.contentType)) {
    return (
      <pre className="file-text-preview" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
        {contentQuery.data.text}
      </pre>
    )
  }
  return download
}
