// 导出结果的「跳过项」文案拼装 —— 双端唯一实现（开发纪律 A-15.2）。
//
// 背景（2026-10-30 实锤）：Wails 桥没有 HTTP 响应头，Web 走 X-Folder-Skipped、
// 桌面走 body 的 folders_skipped；此前两端各写一套字符串，桌面分支漏了
// 「N 条加密文件夹中的笔记」，用户报障。现在：**取值各走各的，文案只此一份**。

/** 返回跳过项短语数组，如 ['2 篇加密笔记', '3 条加密文件夹中的笔记']；无跳过返回 [] */
export function buildSkippedParts (encryptedSkipped = 0, folderSkipped = 0) {
  const parts = []
  const enc = Number(encryptedSkipped) || 0
  const folder = Number(folderSkipped) || 0
  if (enc > 0) parts.push(`${enc} 篇加密笔记`)
  if (folder > 0) parts.push(`${folder} 条加密文件夹中的笔记`)
  return parts
}

/** 拼成「跳过 A、B」；无跳过返回空串（调用方走各自的"全部导出/已保存"文案） */
export function joinSkipped (parts) {
  return parts && parts.length ? `跳过 ${parts.join('、')}` : ''
}

/** 桌面「导出全部/批量」 toast（body 字段口径：skipped + folders_skipped） */
export function buildDesktopExportMessage ({ exported, skipped, foldersSkipped, savedPath } = {}) {
  const base = `已导出 ${Number(exported) || 0} 篇笔记`
  const skipText = joinSkipped(buildSkippedParts(skipped, foldersSkipped))
  return skipText ? `${base}，${skipText}` : `${base}，已保存到 ${savedPath || '所选位置'}`
}

/** Web「导出全部」 toast（响应头口径：X-Encrypted-Skipped + X-Folder-Skipped） */
export function buildWebExportAllMessage ({ encryptedSkipped, folderSkipped } = {}) {
  const skipText = joinSkipped(buildSkippedParts(encryptedSkipped, folderSkipped))
  return skipText ? `导出成功，已${skipText}` : '导出成功，已导出全部笔记'
}
