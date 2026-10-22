// 统一抽取 API 错误文案。
//
// 背景（v21.131 测试轮，同时 DRY 掉 BackupSnapshots/BackupKey 两处重复实现）：
// 下载/导出类接口用 axios `responseType:'blob'`，出错时 `e.response.data` 是 **Blob** 而非
// JSON，直接取 `data.error` 取不到 → 永远弹笼统兜底文案，掩盖后端具体原因（如"远端已无可用副本"）。
// 本函数把 Blob 读回文本再 JSON.parse 抽 error；非 Blob 走常规 `data.error`/`e.message` 兜底。
export async function extractApiError(e, fallback = '操作失败') {
  const d = e?.response?.data
  if (typeof Blob !== 'undefined' && d instanceof Blob) {
    try {
      const parsed = JSON.parse(await d.text())
      return parsed?.error || fallback
    } catch {
      return fallback
    }
  }
  return d?.error || e?.message || fallback
}
