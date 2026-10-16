<script setup lang="ts">
import { RouterView } from 'vue-router'
import NoticeHost from '@/components/NoticeHost.vue'
import PaymentStepUpDialogHost from '@/components/PaymentStepUpDialogHost.vue'
import ConfirmDialogHost from '@/components/ConfirmDialogHost.vue'
</script>

<template>
  <RouterView />
  <NoticeHost />
  <ConfirmDialogHost />
  <PaymentStepUpDialogHost />
</template>
