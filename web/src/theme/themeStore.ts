import { create } from 'zustand'
import { THEMES, isThemeId } from './themes'
import type { ThemeId } from './themes'

export const THEME_STORAGE_KEY = 'euler.theme'
export const DEFAULT_THEME: ThemeId = 'graphite'

// readStoredTheme 读取本地选择；存储不可用或值非法时回退默认主题。
export function readStoredTheme(): ThemeId {
  if (typeof window === 'undefined') {
    return DEFAULT_THEME
  }
  try {
    const value = window.localStorage.getItem(THEME_STORAGE_KEY)
    return isThemeId(value) ? value : DEFAULT_THEME
  } catch {
    return DEFAULT_THEME
  }
}

// applyTheme 把主题写到根节点，CSS 变量与 color-scheme 都依赖该属性。
export function applyTheme(theme: ThemeId): void {
  if (typeof document === 'undefined') {
    return
  }
  document.documentElement.dataset.theme = theme
  document.documentElement.style.colorScheme = THEMES[theme].dark ? 'dark' : 'light'
}

interface ThemeState {
  theme: ThemeId
  setTheme: (theme: ThemeId) => void
}

export const useThemeStore = create<ThemeState>((set) => ({
  theme: readStoredTheme(),
  setTheme(theme) {
    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, theme)
    } catch {
      // 隐私模式或存储被禁用时仍允许本次会话切换。
    }
    applyTheme(theme)
    set({ theme })
  },
}))
