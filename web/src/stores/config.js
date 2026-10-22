import { defineStore } from 'pinia'
import { ref } from 'vue'
import api from '../api/request'

export const useConfigStore = defineStore('config', () => {
  const siteTitle = ref('Sticky note')
  const allowRegister = ref(true)

  async function fetchPublicConfig() {
    try {
      const { data } = await api.get('/public-config')
      siteTitle.value = data.site_title || 'Sticky note'
      allowRegister.value = data.allow_register === 'true'
    } catch {}
  }

  return { siteTitle, allowRegister, fetchPublicConfig }
})
