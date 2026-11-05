import { describe, expect, it } from 'vitest'
import { parseInline, parseRichText } from './richtext'

describe('parseInline', () => {
  it('marks bold runs and keeps plain text', () => {
    expect(parseInline('前**加粗**后')).toEqual([
      { text: '前', bold: false },
      { text: '加粗', bold: true },
      { text: '后', bold: false },
    ])
  })

  it('keeps an unpaired marker instead of swallowing the sentence', () => {
    const parts = parseInline('写到一半**断了')
    expect(parts).toEqual([
      { text: '写到一半', bold: false },
      { text: '**', bold: false },
      { text: '断了', bold: false },
    ])
  })
})

describe('parseRichText', () => {
  it('splits headings, images, diary stamps and paragraphs', () => {
    const blocks = parseRichText(
      ['## 档案读取', '', '你是星火号的船长。', '', '![星火号内部示意图](/story/starfire-map.jpg)', '', '**18:23** 生物学家离席。', '', '### 第一步·自我介绍'].join('\n'),
    )
    expect(blocks.map((b) => b.kind)).toEqual(['h2', 'p', 'image', 'time', 'h3'])
    const time = blocks[3]
    expect(time.kind === 'time' && time.time).toBe('18:23')
    expect(time.kind === 'time' && time.parts).toEqual([{ text: '生物学家离席。', bold: false }])
    const image = blocks[2]
    expect(image.kind === 'image' && image.src).toBe('/story/starfire-map.jpg')
    expect(image.kind === 'image' && image.alt).toBe('星火号内部示意图')
  })

  it('reads a date-and-clock stamp as the first diary row', () => {
    const blocks = parseRichText('**9月8日18:15** 你们像往常一样聚在餐厅吃晚饭。')
    expect(blocks[0].kind).toBe('time')
    expect(blocks[0].kind === 'time' && blocks[0].time).toBe('9月8日 18:15')
    expect(blocks[0].kind === 'time' && blocks[0].parts[0].text).toBe('你们像往常一样聚在餐厅吃晚饭。')
  })

  it('keeps a bolded sentence out of the timeline', () => {
    const blocks = parseRichText('**你们像往常一样聚在餐厅吃晚饭**。')
    expect(blocks[0].kind).toBe('p')
  })

  it('keeps a paragraph together but starts a new one on a blank line', () => {
    const blocks = parseRichText('第一段。\n还是第一段。\n\n第二段。')
    expect(blocks).toHaveLength(2)
    expect(blocks[0].kind === 'p' && blocks[0].parts[0].text).toBe('第一段。\n还是第一段。')
  })
})
