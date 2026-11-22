import { beforeEach, describe, expect, it } from 'vitest'
import {
  DEFAULT_THEME,
  THEME_STORAGE_KEY,
  applyTheme,
  readStoredTheme,
  useThemeStore,
} from './themeStore'

describe('themeStore', () => {
  beforeEach(() => {
    window.localStorage.clear()
    document.documentElement.removeAttribute('data-theme')
    document.documentElement.style.colorScheme = ''
    useThemeStore.setState({ theme: DEFAULT_THEME })
  })

  it('无存储时使用默认主题', () => {
    expect(readStoredTheme()).toBe(DEFAULT_THEME)
  })

  it('忽略非法主题值', () => {
    window.localStorage.setItem(THEME_STORAGE_KEY, 'neon')
    expect(readStoredTheme()).toBe(DEFAULT_THEME)
  })

  it('读取已保存的合法主题', () => {
    window.localStorage.setItem(THEME_STORAGE_KEY, 'paper')
    expect(readStoredTheme()).toBe('paper')
  })

  it('applyTheme 同步 data-theme 与 color-scheme', () => {
    applyTheme('midnight')
    expect(document.documentElement.dataset.theme).toBe('midnight')
    expect(document.documentElement.style.colorScheme).toBe('dark')
  })

  it('setTheme 持久化并立即生效', () => {
    useThemeStore.getState().setTheme('celadon')
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('celadon')
    expect(document.documentElement.dataset.theme).toBe('celadon')
    expect(useThemeStore.getState().theme).toBe('celadon')
  })
})
