import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import MemberLevels from './MemberLevels.vue'

const api = vi.hoisted(() => ({
  getMemberLevels: vi.fn(),
  updateMemberLevel: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: api }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }),
}))
vi.mock('@/utils/notify', () => ({ notifyError: vi.fn(), notifySuccess: vi.fn() }))
vi.mock('@/utils/confirm', () => ({ confirmAction: vi.fn() }))
vi.mock('@/components/ui/dialog', () => {
  const wrapper = { template: '<div><slot /></div>' }
  return {
    Dialog: { props: ['open'], template: '<div v-if="open" role="dialog"><slot /></div>' },
    DialogHeader: wrapper,
    DialogScrollContent: wrapper,
    DialogTitle: wrapper,
  }
})
// Exercise the page's model adapter through the same events as the media picker,
// without uploading files or changing real member-level data.
vi.mock('@/components/admin/MediaPicker.vue', () => ({
  default: {
    props: ['modelValue'],
    emits: ['update:modelValue'],
    template: `<div data-testid="media-picker" :data-value="modelValue">
      <button type="button" data-testid="choose-image" @click="$emit('update:modelValue', '/uploads/level.png')">Choose image</button>
      <button v-if="modelValue" type="button" data-testid="remove-image" @click="$emit('update:modelValue', '')">Remove image</button>
    </div>`,
  },
}))

let app: App | undefined
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

async function mountPage(icon: string) {
  api.getMemberLevels.mockResolvedValue({ data: { data: [{
    id: 7, name: { 'zh-CN': '普通会员' }, slug: 'default', icon,
    discount_rate: 100, recharge_threshold: 0, spend_threshold: 0,
    is_default: true, sort_order: 0, is_active: true,
  }] } })
  api.updateMemberLevel.mockResolvedValue({ data: { data: {} } })
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(MemberLevels)
  app.mount(root)
  await settle()
  return root
}

async function clickText(root: HTMLElement, text: string) {
  const button = [...root.querySelectorAll('button')].find(item => item.textContent?.trim() === text)
  expect(button, `button ${text}`).toBeDefined()
  button!.click()
  await settle()
}

async function submit(root: HTMLElement) {
  root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await settle()
}

describe('member level icon editing', () => {
  it('preserves a legacy emoji when opening, cancelling, and changing only the name', async () => {
    const root = await mountPage('⭐')
    expect(root.textContent).not.toContain('⭐')
    expect(root.querySelector('svg.lucide-user-round')).not.toBeNull()
    await clickText(root, 'admin.common.edit')
    expect(root.querySelector('[data-testid="media-picker"]')?.getAttribute('data-value')).toBe('')
    await clickText(root, 'admin.common.cancel')
    expect(api.updateMemberLevel).not.toHaveBeenCalled()
    expect(root.querySelector('[role="dialog"]')).toBeNull()

    await clickText(root, 'admin.common.edit')
    const name = root.querySelector<HTMLInputElement>('input[placeholder="admin.memberLevels.form.namePlaceholder"]')!
    name.value = '新会员名称'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    await submit(root)
    expect(api.updateMemberLevel).toHaveBeenCalledExactlyOnceWith(7, expect.objectContaining({
      icon: '⭐', name: { 'zh-CN': '新会员名称' },
    }))
  })

  it('replaces the legacy icon with the selected image in the save request', async () => {
    const root = await mountPage('⭐')
    await clickText(root, 'admin.common.edit')
    root.querySelector<HTMLButtonElement>('[data-testid="choose-image"]')!.click()
    await nextTick()
    expect(root.querySelector('[data-testid="media-picker"]')?.getAttribute('data-value')).toBe('/uploads/level.png')
    await submit(root)
    expect(api.updateMemberLevel).toHaveBeenCalledExactlyOnceWith(7, expect.objectContaining({ icon: '/uploads/level.png' }))
  })

  it('saves an empty icon after removing the existing image', async () => {
    const root = await mountPage('/uploads/existing.png')
    await clickText(root, 'admin.common.edit')
    expect(root.querySelector('[data-testid="media-picker"]')?.getAttribute('data-value')).toBe('/uploads/existing.png')
    root.querySelector<HTMLButtonElement>('[data-testid="remove-image"]')!.click()
    await nextTick()
    expect(root.querySelector('[data-testid="media-picker"]')?.getAttribute('data-value')).toBe('')
    await submit(root)
    expect(api.updateMemberLevel).toHaveBeenCalledExactlyOnceWith(7, expect.objectContaining({ icon: '' }))
  })
})
