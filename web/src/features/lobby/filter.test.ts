import { describe, expect, it } from 'vitest'
import { scriptSummarySchema } from '../../types/schema'
import { NO_SERIES, filterScripts, scriptSeries, scriptTags } from './filter'

const scripts = [
  scriptSummarySchema.parse({
    id: 'k1',
    title: '游轮迷影',
    series: 'K的游戏',
    summary: '民国游轮上的血案',
    status: 'published',
    tags: ['民国', '6 人'],
  }),
  scriptSummarySchema.parse({
    id: 'ai1',
    title: '第六次谢幕',
    series: 'AI创作',
    summary: '剧场里的第六次谢幕',
    status: 'published',
    tags: ['悬疑', '6 人'],
  }),
  scriptSummarySchema.parse({
    id: 'other',
    title: '独立作品',
    summary: '没有系列的剧本',
    status: 'published',
    tags: ['实验'],
  }),
]

const filters = (series = '', tag = '', query = '') => ({ series, tag, query })

describe('script filters', () => {
  it('lists series and sorts tags by use count', () => {
    expect(scriptSeries(scripts)).toEqual(['AI创作', 'K的游戏'])
    expect(scriptTags(scripts)).toEqual(['6 人', '民国', '实验', '悬疑'])
  })

  it('filters by series, including the no-series bucket', () => {
    expect(filterScripts(scripts, filters('K的游戏')).map((s) => s.id)).toEqual(['k1'])
    expect(filterScripts(scripts, filters(NO_SERIES)).map((s) => s.id)).toEqual(['other'])
  })

  it('combines tag and keyword filters', () => {
    expect(filterScripts(scripts, filters('', '6 人', '游轮')).map((s) => s.id)).toEqual(['k1'])
    expect(filterScripts(scripts, filters('AI创作', '6 人', '')).map((s) => s.id)).toEqual(['ai1'])
    expect(filterScripts(scripts, filters('', '', '独立')).map((s) => s.id)).toEqual(['other'])
  })
})
