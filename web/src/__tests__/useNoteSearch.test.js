/**
 * useNoteSearch 搜索/标签筛选/防抖测试（v21.58 B-2）
 * 无外部依赖，纯逻辑测试。
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useNoteSearch } from '@/composables/useNoteSearch'

beforeEach(() => {
  vi.useFakeTimers()
})

describe('useNoteSearch 搜索与标签', () => {
  it('showShortQueryHint：1-2 字为 true，3 字及以上为 false', () => {
    const { searchQuery, showShortQueryHint } = useNoteSearch(() => {})

    searchQuery.value = 'ab'
    expect(showShortQueryHint.value).toBe(true)
    searchQuery.value = 'abc'
    expect(showShortQueryHint.value).toBe(false)
  })

  it('toggleTag → 切换 activeTag 并触发 onSearch（再次点击取消）', () => {
    const onSearch = vi.fn()
    const { toggleTag, activeTag } = useNoteSearch(onSearch)

    toggleTag('工作')
    expect(activeTag.value).toBe('工作')
    expect(onSearch).toHaveBeenCalledWith('', '工作')

    toggleTag('工作')
    expect(activeTag.value).toBe('')
  })

  it('clearTag → 清空 activeTag 并触发 onSearch', () => {
    const onSearch = vi.fn()
    const { toggleTag, clearTag, activeTag } = useNoteSearch(onSearch)

    toggleTag('工作')
    clearTag()
    expect(activeTag.value).toBe('')
    expect(onSearch).toHaveBeenLastCalledWith('', '')
  })

  it('debouncedSearch → 300ms 防抖后才触发 onSearch', async () => {
    const onSearch = vi.fn()
    const { searchQuery, debouncedSearch } = useNoteSearch(onSearch)

    searchQuery.value = 'hello'
    debouncedSearch()
    expect(onSearch).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(300)
    expect(onSearch).toHaveBeenCalledWith('hello', '')
  })
})
