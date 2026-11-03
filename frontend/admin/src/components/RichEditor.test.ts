import { afterEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type App } from 'vue'
import i18n from '@/i18n'
import RichEditor from './RichEditor.vue'

vi.mock('@/components/admin/MediaPicker.vue', () => ({ default: { render: () => null } }))

let app: App | undefined
afterEach(() => { app?.unmount(); document.body.replaceChildren() })

it('loads saved rich text without updating the model and preserves edits across source mode', async () => {
  const model = ref('<p>First product</p>')
  const update = vi.fn((value: string) => { model.value = value })
  const root = document.body.appendChild(document.createElement('div'))
  app = createApp(() => h(RichEditor, { modelValue: model.value, 'onUpdate:modelValue': update }))
  app.use(i18n).mount(root)
  await nextTick()

  model.value = '<p><strong>Second product</strong></p>'
  await nextTick()
  expect(root.querySelector('.ProseMirror strong')?.textContent).toBe('Second product')
  expect(update).not.toHaveBeenCalled()

  const toggle = root.querySelector<HTMLButtonElement>('button[title="代码模式"]')!
  toggle.click()
  await nextTick()
  const input = root.querySelector('textarea')!
  input.value = '<p>Updated description</p>'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await nextTick()
  toggle.click()
  await nextTick()
  expect(root.querySelector('.ProseMirror')?.textContent).toBe('Updated description')
  expect(model.value).toBe('<p>Updated description</p>')
})
