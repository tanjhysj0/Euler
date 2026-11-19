import '@testing-library/jest-dom/vitest'

// jsdom 缺少 ResizeObserver，AntD 的部分组件依赖它。
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

if (typeof window.ResizeObserver === 'undefined') {
  window.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver
}

// jsdom 未实现 matchMedia，AntD 的响应式观察器依赖它。
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }),
})

// jsdom 不会派发 animationend/transitionend，AntD 的入场动画会停留在
// 起始样式（如 .ant-zoom-appear 的 opacity:0）。测试环境全局禁用动画，
// 并把入场类的起始样式恢复为可见。
const disableMotion = document.createElement('style')
disableMotion.textContent = [
  '*, *::before, *::after { animation: none !important; transition: none !important; }',
  '.ant-zoom-appear, .ant-zoom-enter, .ant-fade-appear, .ant-fade-enter {',
  '  opacity: 1 !important; transform: none !important;',
  '}',
].join('\n')
document.head.appendChild(disableMotion)
