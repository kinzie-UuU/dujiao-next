<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminPayment } from '@/api/types'
import { manualPaymentSettlement, validManualPaymentEvidence } from '@/utils/manualPaymentEvidence'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { DialogContent, DialogOverlay, DialogPortal } from 'reka-ui'

const props = defineProps<{ payment: AdminPayment | null }>()
const emit = defineEmits<{ close: []; confirmed: [id: number] }>()
const { t } = useI18n()
const amount = ref('')
const currency = ref('')
const verified = ref(false)
const submitting = ref(false)
const error = ref('')
const settlement = computed(() => props.payment ? manualPaymentSettlement(props.payment) : null)
watch(() => props.payment, () => {
  amount.value = ''
  currency.value = settlement.value?.currency || ''
  verified.value = false
  error.value = ''
}, { immediate: true })
const close = () => { if (!submitting.value) emit('close') }
const submit = async () => {
  const payment = props.payment
  if (!payment || submitting.value) return
  const received = amount.value.trim()
  const unit = currency.value.trim().toUpperCase()
  if (!settlement.value?.amount || !validManualPaymentEvidence(received, unit, payment.channel_type, verified.value)) {
    error.value = t('admin.payments.receiptInvalid')
    return
  }
  submitting.value = true
  error.value = ''
  try {
    await adminAPI.confirmManualPayment(payment.id, { amount: received, currency: unit, payer_verified: true })
    emit('confirmed', payment.id)
  } catch (err: any) {
    error.value = err?.message || t('admin.payments.manualConfirmFailed')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Dialog :open="!!payment" @update:open="value => { if (!value) close() }">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-[190] bg-black/80" />
      <DialogContent class="fixed left-1/2 top-1/2 z-[191] grid max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 gap-4 overflow-y-auto rounded-lg border bg-background p-5 shadow-lg sm:p-6" @escape-key-down="event => { if (submitting) event.preventDefault() }" @interact-outside="event => event.preventDefault()">
        <DialogHeader>
          <DialogTitle>{{ t('admin.payments.manualConfirm') }}</DialogTitle>
          <DialogDescription>{{ t('admin.payments.receiptDialogHint') }}</DialogDescription>
        </DialogHeader>
        <dl class="space-y-2 rounded-md border bg-muted/40 p-3 text-sm">
          <div class="flex justify-between gap-4"><dt class="shrink-0 text-muted-foreground">{{ t('admin.payments.receiptReference') }}</dt><dd class="break-all text-right">{{ payment?.order_no || payment?.recharge_no || payment?.payment_no || payment?.id }}</dd></div>
          <div class="flex justify-between gap-4"><dt class="shrink-0 text-muted-foreground">{{ t('admin.payments.receiptChannel') }}</dt><dd class="break-all text-right">{{ payment?.channel_name || payment?.channel_type }}</dd></div>
          <div class="flex justify-between gap-4"><dt class="text-muted-foreground">{{ t('admin.payments.receiptExpected') }}</dt><dd class="font-semibold">{{ settlement?.amount || '—' }} {{ settlement?.currency }}</dd></div>
        </dl>
        <form class="space-y-4" @submit.prevent="submit">
          <div class="grid grid-cols-[minmax(0,1fr)_6rem] gap-3">
            <div class="space-y-2"><label for="manual-received-amount" class="text-sm font-medium">{{ t('admin.payments.receiptAmountLabel') }}</label><Input id="manual-received-amount" v-model="amount" inputmode="decimal" autocomplete="off" :disabled="submitting" /></div>
            <div class="space-y-2"><label for="manual-received-currency" class="text-sm font-medium">{{ t('admin.payments.receiptCurrencyLabel') }}</label><Input id="manual-received-currency" v-model="currency" maxlength="8" :disabled="submitting" /></div>
          </div>
          <label class="flex cursor-pointer items-start gap-2 text-sm leading-6"><input v-model="verified" type="checkbox" class="mt-1 h-4 w-4 shrink-0 accent-primary" :disabled="submitting" /> <span>{{ t('admin.payments.receiptVerifiedLabel') }}</span></label>
          <p v-if="!settlement?.amount" role="alert" class="text-sm text-destructive">{{ t('admin.payments.receiptQuoteMissing') }}</p>
          <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
          <DialogFooter class="gap-2"><Button type="button" variant="outline" :disabled="submitting" @click="close">{{ t('admin.payments.receiptCancel') }}</Button><Button type="submit" :disabled="submitting || !settlement?.amount">{{ t(submitting ? 'admin.payments.manualConfirming' : 'admin.payments.manualConfirm') }}</Button></DialogFooter>
        </form>
      </DialogContent>
    </DialogPortal>
  </Dialog>
</template>
