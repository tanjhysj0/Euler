import { Button, Dropdown } from 'antd'
import type { MenuProps } from 'antd'
import { THEMES, THEME_LIST, isThemeId } from '../theme/themes'
import { useThemeStore } from '../theme/themeStore'

// ThemeSwitcher 在侧栏底部提供四套主题的即时切换，选择会写入 localStorage。
export default function ThemeSwitcher() {
  const theme = useThemeStore((state) => state.theme)
  const setTheme = useThemeStore((state) => state.setTheme)
  const current = THEMES[theme]

  const items: MenuProps['items'] = THEME_LIST.map((item) => ({
    key: item.id,
    label: (
      <span className="theme-option">
        <i className="theme-option-swatch" style={{ background: item.swatches[0] }} />
        <span className="theme-option-text">
          <strong>{item.label}</strong>
          <small>{item.description}</small>
        </span>
      </span>
    ),
  }))

  return (
    <Dropdown
      trigger={['click']}
      placement="topLeft"
      menu={{
        items,
        selectedKeys: [theme],
        onClick: ({ key }) => {
          if (isThemeId(key)) {
            setTheme(key)
          }
        },
      }}
    >
      <Button type="text" size="small" className="theme-switcher" aria-label="切换主题">
        <span className="theme-swatch" style={{ background: current.swatches[0] }} />
        <span className="theme-name">{current.label}</span>
        <span className="theme-caret">⌄</span>
      </Button>
    </Dropdown>
  )
}
