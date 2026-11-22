import { theme as antdTheme } from 'antd'
import type { ThemeConfig } from 'antd'

// ThemeId 是工作台支持的视觉主题标识，data-theme 与 localStorage 共用该值。
export type ThemeId = 'graphite' | 'paper' | 'celadon' | 'midnight'

export interface ThemeSpec {
  id: ThemeId
  label: string
  description: string
  // dark 决定 color-scheme、滚动条与弹层基础色。
  dark: boolean
  // swatches 用于切换器里的主题色点：[强调色, 背景色, 文字色]。
  swatches: [string, string, string]
  antd: ThemeConfig
}

const sans =
  '"SF Pro Text", -apple-system, BlinkMacSystemFont, "PingFang SC", "Segoe UI", sans-serif'
const mono = '"SF Mono", Menlo, Consolas, "JetBrains Mono", monospace'

// 四套主题共享同一套 Ant Design token 结构，只替换色板与圆角。
function buildConfig(
  dark: boolean,
  primary: string,
  background: string,
  sidebar: string,
  surface: string,
  text: string,
  border: string,
  radius: number,
): ThemeConfig {
  return {
    algorithm: dark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
    token: {
      colorPrimary: primary,
      colorInfo: primary,
      colorLink: primary,
      colorBgBase: background,
      colorTextBase: text,
      colorBorder: border,
      borderRadius: radius,
      fontFamily: sans,
      fontFamilyCode: mono,
    },
    components: {
      Layout: { bodyBg: background, siderBg: sidebar, headerBg: background },
      Table: { headerBg: 'transparent', colorBgContainer: 'transparent', borderColor: border },
      Modal: { contentBg: surface, headerBg: surface },
      Dropdown: { colorBgElevated: surface },
    },
  }
}

// THEMES 是主题注册表；新增主题时同步补充 index.css 中的同名 data-theme 变量。
export const THEMES: Record<ThemeId, ThemeSpec> = {
  graphite: {
    id: 'graphite',
    label: '石墨',
    description: '深色 Linear 风，适合长时间编码',
    dark: true,
    swatches: ['#7C8CFF', '#0B0D11', '#E8ECF2'],
    antd: buildConfig(
      true,
      '#7C8CFF',
      '#0B0D11',
      '#0C0F14',
      '#141920',
      '#E8ECF2',
      'rgba(255, 255, 255, 0.12)',
      8,
    ),
  },
  paper: {
    id: 'paper',
    label: '纸白',
    description: '暖白纸张与衬线标题，安静偏阅读',
    dark: false,
    swatches: ['#B4532A', '#F6F2EA', '#24211C'],
    antd: buildConfig(
      false,
      '#B4532A',
      '#F6F2EA',
      '#F1ECE2',
      '#FFFDF9',
      '#24211C',
      '#E2DBCE',
      4,
    ),
  },
  celadon: {
    id: 'celadon',
    label: '青瓷',
    description: '浅色青绿与柔和气泡，轻盈通透',
    dark: false,
    swatches: ['#2E7D63', '#F1F5F3', '#17231E'],
    antd: buildConfig(
      false,
      '#2E7D63',
      '#F1F5F3',
      '#E8EFEB',
      '#FFFFFF',
      '#17231E',
      '#D9E4DF',
      12,
    ),
  },
  midnight: {
    id: 'midnight',
    label: '午夜霓虹',
    description: '近黑终端风与等宽字体，技术氛围最强',
    dark: true,
    swatches: ['#22E6A4', '#07090A', '#E6F2EC'],
    antd: buildConfig(
      true,
      '#22E6A4',
      '#07090A',
      '#090B0D',
      '#0C0F11',
      '#E6F2EC',
      '#1A2225',
      4,
    ),
  },
}

// THEME_LIST 保持切换器的展示顺序，默认主题排在最前。
export const THEME_LIST: ThemeSpec[] = [
  THEMES.graphite,
  THEMES.paper,
  THEMES.celadon,
  THEMES.midnight,
]

export function isThemeId(value: unknown): value is ThemeId {
  return typeof value === 'string' && value in THEMES
}
