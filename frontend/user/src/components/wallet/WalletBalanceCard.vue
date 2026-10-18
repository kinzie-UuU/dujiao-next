<template>
  <div>
    <PanelHeading :title="t('personalCenter.wallet.title')" :description="t('personalCenter.wallet.subtitle')" :icon="Wallet">
      <template #actions>
        <Badge variant="accent" size="sm">{{ t('personalCenter.tabs.wallet') }}</Badge>
      </template>
    </PanelHeading>

    <Alert v-if="alert" class="mb-5" :variant="pageAlertVariant(alert.level)" :class="pageAlertToneClass(alert.level)">
      <AlertDescription>{{ alert.message }}</AlertDescription>
    </Alert>

    <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
      <StatCard :label="t('personalCenter.wallet.balanceLabel')" :value="balanceDisplay" :icon="Banknote" tone="accent" mono />
      <StatCard :label="t('personalCenter.wallet.transactionsLabel')" :value="totalTransactions" :icon="ReceiptText" tone="info" mono />
      <StatCard
        :label="t('personalCenter.wallet.currentPageLabel')"
        :value="t('orders.pageInfo', { page: currentPage, total: totalPages })"
        :icon="Layers"
        tone="neutral"
      />
    </div>
    <div v-if="debtDisplay" role="alert" class="mt-4 flex gap-3 rounded-xl border border-amber-500 bg-amber-50 p-4 text-amber-950 dark:border-amber-500 dark:bg-amber-950 dark:text-amber-100">
      <TriangleAlert class="mt-0.5 size-5 shrink-0" aria-hidden="true" />
      <div>
        <p class="font-semibold">{{ t('personalCenter.wallet.debtTitle', { amount: debtDisplay }) }}</p>
        <p class="mt-1 text-sm leading-6">{{ t('personalCenter.wallet.debtDescription') }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Wallet, Banknote, ReceiptText, Layers, TriangleAlert } from 'lucide-vue-next'
import { pageAlertVariant, pageAlertToneClass, type PageAlert } from '../../utils/alerts'
import PanelHeading from '../shared/PanelHeading.vue'
import StatCard from '../shared/StatCard.vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'

defineProps<{
  alert: PageAlert | null
  balanceDisplay: string
  debtDisplay?: string
  totalTransactions: number
  currentPage: number
  totalPages: number
}>()

const { t } = useI18n()
</script>
