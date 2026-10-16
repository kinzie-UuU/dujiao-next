<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { usePaymentStepUpStore } from '@/stores/paymentStepUp'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { DialogContent, DialogOverlay, DialogPortal } from 'reka-ui'
const store = usePaymentStepUpStore()
const { locale } = useI18n()
const text = computed(() => String(locale.value).startsWith('en')
  ? { title: 'Verify payment settings change', description: 'Enter a new 6-digit authenticator code. Bind an authenticator in security settings first. Recovery codes cannot authorize payment changes. Each code can be used only once.', code: 'Authenticator code', cancel: 'Cancel', submit: 'Verify and save' }
  : String(locale.value) === 'zh-TW'
    ? { title: '驗證收款設定變更', description: '請輸入新的六位驗證器碼。請先在安全設定綁定驗證器；恢復碼不能用於收款變更，每個驗證碼只能使用一次。', code: '驗證器碼', cancel: '取消', submit: '驗證並提交' }
    : { title: '验证收款设置变更', description: '请输入新的六位验证器码。请先在安全设置绑定验证器；恢复码不能用于收款变更，每个验证码只能使用一次。', code: '验证器码', cancel: '取消', submit: '验证并提交' })
</script>
<template>
  <Dialog :open="store.open" @update:open="value => { if (!value) store.cancel() }">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-[200] bg-black/80" />
      <DialogContent class="fixed left-1/2 top-1/2 z-[201] grid w-[calc(100%-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 gap-4 rounded-lg border bg-background p-6 shadow-lg">
        <DialogHeader><DialogTitle>{{ text.title }}</DialogTitle><DialogDescription>{{ text.description }}</DialogDescription></DialogHeader>
        <form class="space-y-4" @submit.prevent="store.confirm">
          <label for="payment-totp" class="block text-sm">{{ text.code }}</label>
          <Input id="payment-totp" v-model="store.code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" pattern="[0-9]{6}" required />
          <DialogFooter><Button type="button" variant="outline" @click="store.cancel">{{ text.cancel }}</Button><Button type="submit" :disabled="!/^\d{6}$/.test(store.code)">{{ text.submit }}</Button></DialogFooter>
        </form>
      </DialogContent>
    </DialogPortal>
  </Dialog>
</template>
