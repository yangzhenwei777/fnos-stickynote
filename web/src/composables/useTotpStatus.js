import { ref } from 'vue'

/**
 * useTotpStatus — 两步验证真实启用态（v21.151，生产 Bug1 回归点）。
 *
 * 背景：ProfileView 此前只在本次会话内临时维护「已启用」态（开启/关闭成功后置位），
 * 页面刷新后一律回到「未启用」——用户再点「开启两步验证」被服务端正确拒绝（已启用），
 * 却只看到笼统的「操作无效」，误以为两步验证被关掉。
 * 本组合式函数从 GET /auth/totp-status 拉取单一事实源；失败回退调用方给的本地值
 * （登录响应缓存的 user.totp_enabled），宁保守也不把已启用误显示成未启用。
 *
 * @param {object} apiClient - axios 实例（测试可注入 mock）
 * @param {boolean} fallback - 拉取失败时的回退值
 */
export function useTotpStatus (apiClient, fallback = false) {
  const totpEnabled = ref(!!fallback)

  async function loadTotpStatus () {
    try {
      const { data } = await apiClient.get('/auth/totp-status')
      totpEnabled.value = !!(data && data.totp_enabled)
    } catch {
      /* 网络失败保留回退值 */
    }
  }

  return { totpEnabled, loadTotpStatus }
}
