import { ref } from 'vue'
import { defineStore } from 'pinia'

export const usePaymentStepUpStore = defineStore('payment-step-up', () => {
  const open = ref(false)
  const code = ref('')
  let resolve: ((code: string | null) => void) | null = null
  const finish = (value: string | null) => {
    const pending = resolve
    resolve = null
    open.value = false
    code.value = ''
    pending?.(value)
  }
  const ask = () => {
    finish(null)
    open.value = true
    return new Promise<string | null>((done) => { resolve = done })
  }
  const confirm = () => { if (/^[0-9]{6}$/.test(code.value)) finish(code.value) }
  const cancel = () => finish(null)
  return { open, code, ask, confirm, cancel }
})
