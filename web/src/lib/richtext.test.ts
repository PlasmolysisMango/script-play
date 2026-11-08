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

  it('keeps the period of day inside the stamp', () => {
    const blocks = parseRichText('**早上 10：00** 你去敲斯星眠的门。')
    expect(blocks[0].kind).toBe('time')
    expect(blocks[0].kind === 'time' && blocks[0].time).toBe('早上 10:00')
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

describe('markdown blocks', () => {
  it('reads lists, quotes and fenced code', () => {
    const blocks = parseRichText(
      ['线索：', '', '- 甲的口供', '- 乙的口供', '', '> 他一直在说谎。', '', '```ts', 'const x = 1', '```'].join('\n'),
    )
    expect(blocks.map((b) => b.kind)).toEqual(['p', 'ul', 'quote', 'code'])
    const list = blocks[1]
    expect(list.kind === 'ul' && list.items.length).toBe(2)
    expect(list.kind === 'ul' && list.items[1][0].text).toBe('乙的口供')
    const quote = blocks[2]
    expect(quote.kind === 'quote' && quote.parts[0].text).toBe('他一直在说谎。')
    const code = blocks[3]
    expect(code.kind === 'code' && code.text).toBe('const x = 1')
    expect(code.kind === 'code' && code.lang).toBe('ts')
  })

  it('numbers an ordered list from its first item', () => {
    const blocks = parseRichText('3. 第一件事\n4. 第二件事')
    expect(blocks[0]).toMatchObject({ kind: 'ol', start: 3 })
    expect(blocks[0].kind === 'ol' && blocks[0].items.length).toBe(2)
  })

  it('ends an open list when a heading arrives', () => {
    const blocks = parseRichText('- 甲\n## 第二节')
    expect(blocks.map((b) => b.kind)).toEqual(['ul', 'h2'])
  })

  it('does not confuse a rule with a bullet', () => {
    expect(parseRichText('---')[0].kind).toBe('rule')
    expect(parseRichText('- 甲')[0].kind).toBe('ul')
  })

  it('reads a printed profile grid as a table', () => {
    const blocks = parseRichText(
      ['| 性别 | 年龄 | 身高 | 体重 |', '| --- | --- | --- | --- |', '| 男 | 50 岁左右 | 177cm | 65kg |'].join('\n'),
    )
    expect(blocks.map((b) => b.kind)).toEqual(['table'])
    const table = blocks[0]
    expect(table.kind === 'table' && table.head.map((c) => c[0].text)).toEqual(['性别', '年龄', '身高', '体重'])
    expect(table.kind === 'table' && table.rows[0].map((c) => c[0].text)).toEqual(['男', '50 岁左右', '177cm', '65kg'])
  })

  it('keeps a pipe line literal when no rule follows it', () => {
    const blocks = parseRichText('| 不是表格的一行 |')
    expect(blocks.map((b) => b.kind)).toEqual(['p'])
  })
})

describe('markdown inline', () => {
  it('marks code, italic and links, keeping bold alongside', () => {
    expect(parseInline('看 `船票` 和 *潮汐* 与 [地图](/story/map.jpg)')).toEqual([
      { text: '看 ', bold: false },
      { text: '船票', bold: false, code: true },
      { text: ' 和 ', bold: false },
      { text: '潮汐', bold: false, italic: true },
      { text: ' 与 ', bold: false },
      { text: '地图', bold: false, href: '/story/map.jpg' },
    ])
  })

  it('keeps a lone asterisk literal', () => {
    expect(parseInline('他说*然后走了')).toEqual([{ text: '他说*然后走了', bold: false }])
  })
})
