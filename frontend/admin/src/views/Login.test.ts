import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import Login from './Login.vue'

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  getPublicConfig: vi.fn(),
  push: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({
  useAdminAuthStore: () => ({
    login: mocks.login,
    loading: false,
    challengeExpiresAt: '2099-01-01T00:00:00Z',
    clearChallenge: vi.fn(),
  }),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { getPublicConfig: mocks.getPublicConfig } }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: mocks.push }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/favicon', () => ({ applySiteIcon: vi.fn() }))
vi.mock('@/components/captcha/ImageCaptcha.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/captcha/TurnstileCaptcha.vue', () => ({ default: { template: '<div />' } }))

let app: App | undefined

beforeEach(() => {
  mocks.getPublicConfig.mockResolvedValue({ data: { data: { captcha: { provider: 'none' } } } })
  mocks.login.mockResolvedValue({ requiresTotp: true })
})

afterEach(() => {
  app?.unmount()
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

async function settle() {
  await Promise.resolve()
  await Promise.resolve()
  await nextTick()
}

async function mountPage() {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(Login)
  app.mount(root)
  await settle()
  return root
}

async function fill(root: HTMLElement, id: string, value: string) {
  const input = root.querySelector<HTMLInputElement>(`#${id}`)!
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
}

async function submit(root: HTMLElement) {
  root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await settle()
}

describe('admin login input validation', () => {
  it.each(['', '   '])('rejects username %j before requesting authentication', async (username) => {
    const root = await mountPage()
    await fill(root, 'username', username)
    await fill(root, 'password', 'valid-password')
    await submit(root)
    expect(root.querySelector('[role="alert"]')?.textContent).toBe('admin.login.errors.usernameRequired')
    expect(mocks.login).not.toHaveBeenCalled()
    expect(mocks.push).not.toHaveBeenCalled()
  })

  it('rejects an empty password before requesting authentication', async () => {
    const root = await mountPage()
    await fill(root, 'username', 'admin')
    await submit(root)
    expect(root.querySelector('[role="alert"]')?.textContent).toBe('admin.login.errors.passwordRequired')
    expect(mocks.login).not.toHaveBeenCalled()
    expect(mocks.push).not.toHaveBeenCalled()
  })

  it.each(['  secret phrase  ', '   '])('trims the username but preserves password %j', async (password) => {
    const root = await mountPage()
    // A prior validation error must clear when corrected and proceeding to 2FA.
    await submit(root)
    await fill(root, 'username', '  admin  ')
    await fill(root, 'password', password)
    await submit(root)
    expect(mocks.login).toHaveBeenCalledExactlyOnceWith({
      username: 'admin', password, captcha_payload: undefined,
    })
    expect(root.querySelector('#totp')).not.toBeNull()
    expect(root.querySelector('[role="alert"]')).toBeNull()
    expect(mocks.push).not.toHaveBeenCalled()
  })

  it('passes password-manager autocomplete hints to the actual input elements', async () => {
    const root = await mountPage()
    expect(root.querySelector('#username')?.getAttribute('autocomplete')).toBe('username')
    expect(root.querySelector('#password')?.getAttribute('autocomplete')).toBe('current-password')
    expect(root.querySelector('#password')?.getAttribute('type')).toBe('password')
  })
})
