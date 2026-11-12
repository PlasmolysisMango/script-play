import type { ScriptSummary } from '../../types/schema'

/** The select value for scripts that do not belong to a named series. */
export const NO_SERIES = '__none__'

export type ScriptFilters = {
  series: string
  tag: string
  query: string
}

export function scriptSeries(scripts: ScriptSummary[]): string[] {
  return [...new Set(scripts.map((s) => s.series).filter(Boolean))].sort((a, b) => a.localeCompare(b, 'zh-CN'))
}

export function scriptTags(scripts: ScriptSummary[]): string[] {
  const counts = new Map<string, number>()
  for (const script of scripts) {
    for (const tag of script.tags) counts.set(tag, (counts.get(tag) ?? 0) + 1)
  }
  return [...counts.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0], 'zh-CN'))
    .map(([tag]) => tag)
}

export function filterScripts(scripts: ScriptSummary[], filters: ScriptFilters): ScriptSummary[] {
  const query = filters.query.trim().toLowerCase()
  return scripts.filter((script) => {
    if (filters.series === NO_SERIES) {
      if (script.series) return false
    } else if (filters.series && script.series !== filters.series) {
      return false
    }
    if (filters.tag && !script.tags.includes(filters.tag)) return false
    if (query) {
      const haystack = [script.title, script.summary, script.series, ...script.tags].join(' ').toLowerCase()
      if (!haystack.includes(query)) return false
    }
    return true
  })
}
