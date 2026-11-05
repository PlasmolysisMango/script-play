import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { useApp } from '../../store/app'
import { Empty, Field, SectionTitle, Spinner, Tag } from '../../components/ui'
import { validationErrorSchema, type RawMaterial, type ScriptSummary, type ValidationError } from '../../types/schema'

type EditorMode = 'form' | 'json'

/** Loose views of docs/script.schema.json: the form only owns the fields it edits. */
type PhaseDraft = {
  id?: string
  type?: string
  name?: string
  round?: number
  dmScript?: string
  cluePool?: string[]
  perPlayerPick?: number
  speechPerPlayer?: number
  /** pick = 玩家自选，random = 点「随机抽取」逐条抽 */
  searchMode?: string
  /** round_robin / random / host_assigned */
  order?: string
  timeoutSec?: number
}

type MetaDraft = {
  title?: string
  author?: string
  summary?: string
  difficulty?: string
  tags?: string[]
  playerCount?: { min?: number; max?: number }
  durationMin?: number
}

type ScriptDraft = { meta?: MetaDraft; phases?: PhaseDraft[] } & Record<string, unknown>

function parseDraft(text: string): ScriptDraft | null {
  if (!text.trim()) return null
  try {
    const value: unknown = JSON.parse(text)
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null
    return value as ScriptDraft
  } catch {
    return null
  }
}

/**
 * Script studio: the server archives raw material but never parses it, so a
 * script starts either as a pasted standard JSON or as an empty skeleton. Both
 * become a draft that must be reviewed and published before a room can use it.
 */
export function StudioPage() {
  const notify = useApp((s) => s.notify)
  const queryClient = useQueryClient()

  const [jsonText, setJsonText] = useState('')
  const [pasting, setPasting] = useState(false)
  const [selected, setSelected] = useState('')
  const [editor, setEditor] = useState('')
  const [mode, setMode] = useState<EditorMode>('form')
  const [errors, setErrors] = useState<ValidationError[]>([])

  const scripts = useQuery({ queryKey: ['scripts'], queryFn: api.listScripts })
  const materials = useQuery({ queryKey: ['materials'], queryFn: api.materials })
  const detail = useQuery({
    queryKey: ['script', selected],
    queryFn: () => api.getScript(selected),
    enabled: !!selected,
  })

  const refreshScripts = () => queryClient.invalidateQueries({ queryKey: ['scripts'] })

  const selectedSummary = useMemo(
    () => (scripts.data ?? []).find((s) => s.id === selected),
    [scripts.data, selected],
  )
  const isDraft = selectedSummary?.status === 'draft'
  // Every script is editable, draft or not: saving a published one is what
  // produces the next version, so the studio never has to fork first.
  const editable = !!selectedSummary && selectedSummary.owner !== false

  // Load the author's draft into the JSON editor once it arrives.
  useEffect(() => {
    const body = detail.data?.script.body
    if (!body) return
    setEditor(JSON.stringify(body, null, 2))
  }, [detail.data])

  const applyCreated = (id: string | undefined, verrs: ValidationError[] | undefined, msg: string) => {
    refreshScripts()
    setErrors(verrs ?? [])
    if (id) setSelected(id)
    notify(verrs?.length ? `${msg}，但有 ${verrs.length} 处待补充` : msg)
  }

  const importJSON = useMutation({
    mutationFn: (body: unknown) => api.createScript(body),
    onSuccess: (res) => applyCreated((res.script as { id?: string }).id, res.errors, '草稿已建立'),
    onError: (err) => notify(err instanceof ApiError ? err.message : '导入失败', 'error'),
  })

  // A blank draft is the normal path once the server stops parsing: the
  // skeleton carries the five phases so the editor has something to edit.
  const blank = useMutation({
    mutationFn: () => api.createScript(blankScript()),
    onSuccess: (res) => applyCreated((res.script as { id?: string }).id, res.errors, '已新建空白草稿'),
    onError: (err) => notify(err instanceof ApiError ? err.message : '创建失败', 'error'),
  })

  const upload = useMutation({
    mutationFn: (files: File[]) => api.uploadMaterials(files),
    onSuccess: (saved) => {
      queryClient.invalidateQueries({ queryKey: ['materials'] })
      notify(`已归档 ${saved.length} 份原件，可用路径在下方`)
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : '上传失败', 'error'),
  })

  const removeMaterial = useMutation({
    mutationFn: (id: string) => api.deleteMaterial(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['materials'] })
      notify('素材已删除')
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : '删除失败', 'error'),
  })

  const save = useMutation({
    mutationFn: async () => {
      let parsed: unknown
      try {
        parsed = JSON.parse(editor)
      } catch {
        throw new Error('JSON 解析失败，请检查括号和引号')
      }
      return api.updateScript(selected, parsed)
    },
    onSuccess: (res) => {
      setErrors(res.errors ?? [])
      refreshScripts()
      queryClient.invalidateQueries({ queryKey: ['script', selected] })
      if (isDraft) {
        notify(res.errors?.length ? '已保存，仍有校验问题' : '草稿已保存')
        return
      }
      const version = (res.script as { version?: number } | undefined)?.version
      notify(version ? `已保存并发布 v${version}，新开的房间用这一版` : '已保存并发布新版本')
    },
    onError: (err) => {
      // A published script only accepts a revision that passes validation, so
      // the errors come back as 422 instead of a saved-but-broken body.
      if (err instanceof ApiError && err.status === 422) {
        const parsed = extractErrors(err.payload)
        setErrors(parsed)
        notify(parsed.length ? `还有 ${parsed.length} 处校验未通过，未保存` : '校验未通过，未保存', 'error')
        return
      }
      notify(err instanceof ApiError ? err.message : '保存失败', 'error')
    },
  })

  // Publishing must send what is in the editor, not the stored body: otherwise
  // the last edits are silently dropped while the version still goes up.
  const publish = useMutation({
    mutationFn: async () => {
      let parsed: unknown
      try {
        parsed = JSON.parse(editor)
      } catch {
        throw new Error('JSON 解析失败，请检查括号和引号')
      }
      const saved = await api.updateScript(selected, parsed)
      setErrors(saved.errors ?? [])
      if (saved.errors?.length) return { published: false, errors: saved.errors }
      await api.publishScript(selected)
      return { published: true, errors: [] as ValidationError[] }
    },
    onSuccess: (res) => {
      refreshScripts()
      queryClient.invalidateQueries({ queryKey: ['script', selected] })
      if (!res.published) {
        notify(`还有 ${res.errors.length} 处校验未通过`, 'error')
        return
      }
      setErrors([])
      notify('剧本已发布，可以在大厅建房了')
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 422) {
        const parsed = extractErrors(err.payload)
        setErrors(parsed)
        notify(parsed.length ? `还有 ${parsed.length} 处校验未通过` : err.message, 'error')
        return
      }
      notify(err instanceof Error ? err.message : '发布失败', 'error')
    },
  })

  const fork = useMutation({
    mutationFn: () => api.forkScript(selected),
    onSuccess: (res) => {
      const id = (res.script as { id?: string }).id
      refreshScripts()
      if (id) setSelected(id)
      setErrors([])
      notify('已另存为新草稿，改完发布就是新版本')
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : '另存草稿失败', 'error'),
  })

  const draft = useMemo(() => parseDraft(editor), [editor])

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-[340px_minmax(0,1fr)]">
      <aside className="scroll-thin min-w-0 space-y-4 self-start lg:max-h-[calc(100vh-6rem)] lg:overflow-y-auto">
        <div className="sheet">
          <SectionTitle aside={<button type="button" className="btn-ghost text-xs" onClick={() => refreshScripts()}>刷新</button>}>
            剧本库
          </SectionTitle>
          {scripts.isLoading ? <Spinner /> : null}
          {scripts.data?.length === 0 ? <Empty>还没有剧本，先建一份空白草稿或粘贴标准 JSON。</Empty> : null}
          <ul className="space-y-0">
            {(scripts.data ?? []).map((s) => (
              <ScriptRow
                key={s.id}
                script={s}
                active={s.id === selected}
                onSelect={() => {
                  setSelected(s.id)
                  setErrors([])
                }}
              />
            ))}
          </ul>
        </div>

        <div className="card space-y-3">
          <SectionTitle>新建剧本</SectionTitle>
          <p className="text-xs leading-relaxed text-slate-500">
            服务端不解析原始剧本：上传的原件只归档到收件目录，由开发者转成标准 JSON（格式见 docs/script.schema.json）后再导入。
          </p>
          <div className="flex gap-2">
            <button
              type="button"
              className="btn-primary flex-1"
              disabled={blank.isPending}
              onClick={() => blank.mutate()}
            >
              {blank.isPending ? '创建中…' : '新建空白草稿'}
            </button>
            <button type="button" className="btn-ghost" onClick={() => setPasting((v) => !v)}>
              {pasting ? '收起 JSON' : '粘贴 JSON'}
            </button>
          </div>

          {pasting ? (
            <div className="space-y-2">
              <Field label="标准剧本 JSON" hint="角色、线索、真相改完再发布">
                <textarea
                  className="input min-h-[220px] font-mono text-xs"
                  value={jsonText}
                  onChange={(e) => setJsonText(e.target.value)}
                  placeholder={'{\n  "schemaVersion": 1,\n  "meta": { "title": "...", "playerCount": {"min":4,"max":6} },\n  ...\n}'}
                />
              </Field>
              <button
                type="button"
                className="btn-primary w-full"
                disabled={importJSON.isPending || !jsonText.trim()}
                onClick={() => {
                  try {
                    importJSON.mutate(JSON.parse(jsonText))
                  } catch {
                    notify('JSON 解析失败，请检查括号和引号', 'error')
                  }
                }}
              >
                {importJSON.isPending ? '导入中…' : '导入为草稿'}
              </button>
            </div>
          ) : null}
        </div>

        <MaterialPanel
          dir={materials.data?.dir ?? ''}
          items={materials.data?.materials ?? []}
          loading={materials.isLoading}
          busy={upload.isPending}
          onUpload={(files) => upload.mutate(files)}
          onDelete={(m) => removeMaterial.mutate(m.id)}
        />
      </aside>

      <section className="space-y-4">
        {!selected ? (
          <div className="grid h-full min-h-[40vh] place-items-center">
            <Empty>先挑一份剧本开始校对，或从剧本库新建一份草稿。</Empty>
          </div>
        ) : (
          <>
            <div className="card">
              <SectionTitle
                aside={
                  <div className="flex items-center gap-2">
                    <Tag tone={isDraft ? 'amber' : 'green'}>{isDraft ? '草稿' : '已发布'}</Tag>
                    <Tag>v{selectedSummary?.version ?? 1}</Tag>
                  </div>
                }
              >
                {selectedSummary?.title || '未命名剧本'}
              </SectionTitle>
              <p className="text-xs text-slate-500">
                来源：{selectedSummary?.source} · {selectedSummary?.roles ?? 0} 角色 · {selectedSummary?.clues ?? 0} 线索 ·
                {selectedSummary?.playerCount ? ` ${selectedSummary.playerCount.min}-${selectedSummary.playerCount.max} 人` : ' 人数未定'}
              </p>
              {errors.length > 0 ? (
                <ul className="mt-3 space-y-1 rounded-xl border border-rose-500/40 bg-rose-950/30 p-3 text-xs text-rose-100">
                  {errors.map((e, i) => (
                    <li key={`${e.path}-${i}`}>
                      <span className="font-mono text-rose-300">{e.path || 'root'}</span>：{e.message}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="mt-3 rounded-xl border border-line/60 bg-ink/40 p-3 text-xs text-slate-400">
                  {isDraft
                    ? '当前没有校验错误。发布前请通读一遍，尤其是角色本与线索归属（含剧透）。'
                    : '已发布 · 可直接修改：保存即发布新版本（版本号 +1），新开的房间用新版本，进行中的对局仍用开局那一版。已发布剧本只接受能通过校验的内容。'}
                </p>
              )}
              <div className="mt-3 flex flex-wrap gap-2">
                {isDraft ? (
                  <>
                    <button type="button" className="btn-primary" disabled={!editable || save.isPending} onClick={() => save.mutate()}>
                      {save.isPending ? '保存中…' : '保存草稿'}
                    </button>
                    <button
                      type="button"
                      className="btn-warm"
                      disabled={!editable || publish.isPending}
                      onClick={() => publish.mutate()}
                    >
                      {publish.isPending ? '发布中…' : '校验并发布'}
                    </button>
                  </>
                ) : (
                  <button type="button" className="btn-primary" disabled={!editable || save.isPending} onClick={() => save.mutate()}>
                    {save.isPending ? '保存中…' : `保存并发布 v${(selectedSummary?.version ?? 1) + 1}`}
                  </button>
                )}
                <button type="button" className="btn-ghost" disabled={!editable || fork.isPending} onClick={() => fork.mutate()}>
                  {fork.isPending ? '复制中…' : '另存为副本'}
                </button>
                <button
                  type="button"
                  className="btn-ghost"
                  disabled={!isDraft || !editable}
                  onClick={() => {
                    void (async () => {
                      try {
                        await api.deleteScript(selected)
                        setSelected('')
                        refreshScripts()
                        notify('草稿已删除')
                      } catch (err) {
                        notify(err instanceof ApiError ? err.message : '删除失败', 'error')
                      }
                    })()
                  }}
                >
                  删除草稿
                </button>
              </div>
            </div>

            <div className="card">
              <SectionTitle
                aside={
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-slate-500">
                      {isDraft ? '改完保存再发布' : editable ? '可直接编辑 · 保存即新版本' : '只读'}
                    </span>
                    <div className="flex rounded-lg border border-line p-0.5 text-xs">
                      {(
                        [
                          ['form', '结构化'],
                          ['json', 'JSON'],
                        ] as [EditorMode, string][]
                      ).map(([key, label]) => (
                        <button
                          key={key}
                          type="button"
                          className={`rounded px-2 py-0.5 transition ${
                            mode === key ? 'bg-accent/20 text-strong' : 'text-slate-400 hover:text-strong'
                          }`}
                          onClick={() => setMode(key)}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                  </div>
                }
              >
                {isDraft ? '校对草稿' : '编辑剧本'}
              </SectionTitle>
              {detail.isLoading ? <Spinner /> : null}
              {mode === 'json' ? (
                <textarea
                  className="input min-h-[420px] font-mono text-xs leading-relaxed"
                  value={editor}
                  onChange={(e) => setEditor(e.target.value)}
                  spellCheck={false}
                  disabled={!editable}
                />
              ) : draft ? (
                <ScriptForm
                  draft={draft}
                  disabled={!editable}
                  onChange={(next) => setEditor(JSON.stringify(next, null, 2))}
                />
              ) : (
                <p className="rounded-xl border border-rose-500/40 bg-rose-950/30 p-3 text-xs text-rose-100">
                  JSON 暂时无法解析，先切到「JSON」模式修好括号和引号。
                </p>
              )}
            </div>
          </>
        )}
      </section>
    </div>
  )
}

function ScriptRow({ script, active, onSelect }: { script: ScriptSummary; active: boolean; onSelect: () => void }) {
  return (
    // The rule lives on the row, not on the button: a lone button inside its
    // <li> is always :last-child, so `last:border-0` would erase every divider.
    <li className="border-b border-line last:border-0">
      <button
        type="button"
        onClick={onSelect}
        className={`w-full px-1 py-2 text-left text-sm transition ${
          active ? 'bg-accent/10 text-strong' : 'hover:bg-ink/40'
        }`}
      >
        <div className="flex items-center justify-between gap-2">
          <span className="min-w-0 truncate">{script.title || '未命名剧本'}</span>
          <Tag tone={script.status === 'published' ? 'green' : 'amber'}>{script.status === 'published' ? '已发布' : '草稿'}</Tag>
        </div>
        <p className="mt-1 text-xs text-slate-500 break-words">
          {script.source} · v{script.version} · {script.roles} 角色
          {script.warnings > 0 ? ` · ${script.warnings} 处待修` : ''}
        </p>
      </button>
    </li>
  )
}

/**
 * Raw material panel: the originals themselves. Nothing is parsed here, so the
 * only thing worth showing is where each file landed on the server — that path
 * is what a developer opens when converting it into the standard JSON.
 */
function MaterialPanel({
  dir,
  items,
  loading,
  busy,
  onUpload,
  onDelete,
}: {
  dir: string
  items: RawMaterial[]
  loading: boolean
  busy: boolean
  onUpload: (files: File[]) => void
  onDelete: (m: RawMaterial) => void
}) {
  const [files, setFiles] = useState<File[]>([])
  return (
    <div className="card space-y-3">
      <SectionTitle aside={<Tag>{items.length} 份</Tag>}>原始素材</SectionTitle>
      <p className="text-xs leading-relaxed text-slate-500">
        上传剧本压缩包、docx、pdf 或照片，原样存到服务端收件目录：
        {dir ? <code className="ml-1 break-all font-mono text-[11px] text-slate-400">{dir}</code> : ' data/inbox'}
        。服务端只归档，不解析也不解包。
      </p>
      <input
        type="file"
        multiple
        className="input text-xs file:mr-3 file:rounded-lg file:border-0 file:bg-accent/20 file:px-2 file:py-1 file:text-accent"
        onChange={(e) => setFiles(Array.from(e.target.files ?? []))}
      />
      {files.length > 0 ? (
        <>
          <ul className="space-y-1 text-xs text-slate-500">
            {files.map((f) => (
              <li key={f.name}>
                {f.name} · {formatSize(f.size)}
              </li>
            ))}
          </ul>
          <button type="button" className="btn-primary w-full" disabled={busy} onClick={() => onUpload(files)}>
            {busy ? '上传中…' : `上传 ${files.length} 个文件`}
          </button>
        </>
      ) : null}
      {loading ? <Spinner /> : null}
      {!loading && items.length === 0 ? <Empty>还没有上传过原件。</Empty> : null}
      <ul className="space-y-2">
        {items.map((m) => (
          <li key={m.id} className="rounded-xl border border-line/60 px-3 py-2 text-xs">
            <div className="flex items-start justify-between gap-2">
              <span className="truncate text-slate-200" title={m.name}>
                {m.name}
              </span>
              <button
                type="button"
                className="btn-ghost shrink-0 text-[11px] text-rose-300"
                onClick={() => onDelete(m)}
              >
                删除
              </button>
            </div>
            <p className="mt-1 break-all font-mono text-[11px] leading-relaxed text-slate-500">{m.path || '—'}</p>
            <p className="text-[11px] text-slate-500">{formatSize(m.size)}</p>
          </li>
        ))}
      </ul>
    </div>
  )
}

function formatSize(bytes: number): string {
  if (bytes <= 0) return '—'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

/**
 * The skeleton a new draft starts from: metadata plus the phases the engine
 * expects. Reading comes first because every script opens with the role books
 * in hand; roles and clues stay empty for the developer to fill in.
 */
function blankScript() {
  return {
    schemaVersion: 1,
    meta: { title: '未命名剧本', playerCount: { min: 4, max: 6 }, tags: [] },
    roles: [],
    clues: [],
    phases: [
      { id: 'p1', type: 'reading', name: '阅读剧本' },
      { id: 'p2', type: 'discussion', name: '自我介绍与案情讨论', round: 1, speechPerPlayer: 1, order: 'round_robin' },
      { id: 'p3', type: 'search', name: '第一轮搜证', round: 1, perPlayerPick: 2, order: 'round_robin', searchMode: 'pick' },
      { id: 'p4', type: 'discussion', name: '第一轮公聊', round: 1, speechPerPlayer: 2, order: 'round_robin' },
      { id: 'p5', type: 'vote', name: '投票' },
      { id: 'p6', type: 'reveal', name: '复盘' },
    ],
  }
}

const PHASE_TYPES = [
  ['reading', '阅读剧本'],
  ['opening', '开场'],
  ['search', '搜证'],
  ['discussion', '讨论'],
  ['vote', '投票'],
  ['reveal', '揭晓'],
] as const

const PHASE_ORDERS = [
  ['round_robin', '轮流（按入座）'],
  ['random', '随机'],
  ['host_assigned', '主持人指定'],
] as const

const SEARCH_MODES = [
  ['pick', '自选（看标题挑）'],
  ['random', '随机抽取（抽到什么算什么）'],
] as const

/** One line of what the chosen order means at the table. */
function orderHint(order?: string): string {
  switch (order) {
    case 'random':
      return '阶段开始时随机排一次，之后按这个顺序进行'
    case 'host_assigned':
      return '阶段开始由主持人一次性排好整轮顺序'
    default:
      return '按入座顺序轮流，主持人优先'
  }
}

function ScriptForm({
  draft,
  disabled,
  onChange,
}: {
  draft: ScriptDraft
  disabled: boolean
  onChange: (next: ScriptDraft) => void
}) {
  const meta = draft.meta ?? {}
  const count = meta.playerCount ?? {}
  const phases = Array.isArray(draft.phases) ? draft.phases : []

  const patchMeta = (patch: Partial<MetaDraft>) => onChange({ ...draft, meta: { ...meta, ...patch } })
  const setPhases = (next: PhaseDraft[]) => onChange({ ...draft, phases: next })
  const patchPhase = (index: number, patch: Partial<PhaseDraft>) =>
    setPhases(phases.map((phase, i) => (i === index ? { ...phase, ...patch } : phase)))
  // Switching the type also fills the fields the engine reads for it, so a card
  // switched to "搜索" never imports with a zero pick count.
  const setType = (index: number, type: string) => {
    const phase = phases[index] ?? {}
    if (type === 'search') {
      patchPhase(index, {
        type,
        perPlayerPick: phase.perPlayerPick ?? 2,
        searchMode: phase.searchMode ?? 'pick',
        order: phase.order ?? 'round_robin',
      })
      return
    }
    if (type === 'discussion') {
      patchPhase(index, { type, speechPerPlayer: phase.speechPerPlayer ?? 1, order: phase.order ?? 'round_robin' })
      return
    }
    patchPhase(index, { type })
  }
  const move = (index: number, delta: number) => {
    const target = index + delta
    if (target < 0 || target >= phases.length) return
    const next = phases.slice()
    next[index] = phases[target]
    next[target] = phases[index]
    setPhases(next)
  }

  return (
    <div className="space-y-5">
      <div>
        <h4 className="mb-2 text-xs font-semibold text-slate-300">基础信息</h4>
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label="标题">
            <input
              className="input text-xs"
              value={meta.title ?? ''}
              disabled={disabled}
              onChange={(e) => patchMeta({ title: e.target.value })}
            />
          </Field>
          <Field label="作者">
            <input
              className="input text-xs"
              value={meta.author ?? ''}
              disabled={disabled}
              onChange={(e) => patchMeta({ author: e.target.value })}
            />
          </Field>
          <div className="sm:col-span-2">
            <Field label="简介">
              <textarea
                className="input min-h-[64px] text-xs"
                value={meta.summary ?? ''}
                disabled={disabled}
                onChange={(e) => patchMeta({ summary: e.target.value })}
              />
            </Field>
          </div>
          <Field label="人数下限">
            <input
              type="number"
              min={1}
              className="input text-xs"
              value={count.min ?? 0}
              disabled={disabled}
              onChange={(e) => patchMeta({ playerCount: { ...count, min: Number(e.target.value) || 0 } })}
            />
          </Field>
          <Field label="人数上限">
            <input
              type="number"
              min={1}
              className="input text-xs"
              value={count.max ?? 0}
              disabled={disabled}
              onChange={(e) => patchMeta({ playerCount: { ...count, max: Number(e.target.value) || 0 } })}
            />
          </Field>
          <Field label="难度">
            <input
              className="input text-xs"
              value={meta.difficulty ?? ''}
              disabled={disabled}
              onChange={(e) => patchMeta({ difficulty: e.target.value })}
            />
          </Field>
          <Field label="时长（分钟）">
            <input
              type="number"
              min={0}
              className="input text-xs"
              value={meta.durationMin ?? 0}
              disabled={disabled}
              onChange={(e) => patchMeta({ durationMin: Number(e.target.value) || 0 })}
            />
          </Field>
          <div className="sm:col-span-2">
            <Field label="标签" hint="用逗号分隔">
              <input
                className="input text-xs"
                value={(meta.tags ?? []).join('、')}
                disabled={disabled}
                onChange={(e) =>
                  patchMeta({ tags: e.target.value.split(/[，,、]/).map((s) => s.trim()).filter(Boolean) })
                }
              />
            </Field>
          </div>
        </div>
      </div>

      <div>
        <div className="mb-2 flex items-center justify-between">
          <h4 className="text-xs font-semibold text-slate-300">阶段与轮次</h4>
          <button
            type="button"
            className="btn-ghost text-[11px]"
            disabled={disabled}
            onClick={() =>
              setPhases([
                ...phases,
                {
                  id: `phase_${phases.length + 1}`,
                  type: 'discussion',
                  name: '新阶段',
                  round: 1,
                  order: 'round_robin',
                  speechPerPlayer: 1,
                },
              ])
            }
          >
            + 添加阶段
          </button>
        </div>
        {phases.length === 0 ? (
          <p className="text-xs text-slate-500">还没有阶段。校验要求第一个阶段是 reading，并且至少有一个 reveal。</p>
        ) : null}
        <div className="space-y-2">
          {phases.map((phase, index) => (
            <div key={`${phase.id ?? 'phase'}-${index}`} className="rounded-xl border border-line/70 bg-ink/30 p-3">
              <div className="mb-2 flex items-center justify-between gap-2">
                <span className="font-mono text-[11px] text-slate-500">
                  #{index + 1} · {phase.id ?? '未命名'}
                </span>
                <div className="flex gap-1">
                  <button
                    type="button"
                    className="btn-ghost text-[11px]"
                    disabled={disabled || index === 0}
                    onClick={() => move(index, -1)}
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    className="btn-ghost text-[11px]"
                    disabled={disabled || index === phases.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    ↓
                  </button>
                  <button
                    type="button"
                    className="btn-ghost text-[11px] text-rose-300"
                    disabled={disabled}
                    onClick={() => setPhases(phases.filter((_, i) => i !== index))}
                  >
                    删除
                  </button>
                </div>
              </div>
              <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
                <Field label="类型">
                  <select
                    className="input text-xs"
                    value={phase.type ?? ''}
                    disabled={disabled}
                    onChange={(e) => setType(index, e.target.value)}
                  >
                    {PHASE_TYPES.map(([value, label]) => (
                      <option key={value} value={value}>
                        {label}（{value}）
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label="名称">
                  <input
                    className="input text-xs"
                    value={phase.name ?? ''}
                    disabled={disabled}
                    onChange={(e) => patchPhase(index, { name: e.target.value })}
                  />
                </Field>
                <Field label="轮次">
                  <input
                    type="number"
                    min={0}
                    className="input text-xs"
                    value={phase.round ?? 0}
                    disabled={disabled}
                    onChange={(e) => patchPhase(index, { round: Number(e.target.value) || 0 })}
                  />
                </Field>
                {phase.type === 'search' || phase.type === 'discussion' ? (
                  <Field label="顺序策略" hint={orderHint(phase.order)}>
                    <select
                      className="input text-xs"
                      value={phase.order || 'round_robin'}
                      disabled={disabled}
                      onChange={(e) => patchPhase(index, { order: e.target.value })}
                    >
                      {PHASE_ORDERS.map(([value, label]) => (
                        <option key={value} value={value}>
                          {label}
                        </option>
                      ))}
                    </select>
                  </Field>
                ) : null}
                {phase.type === 'search' ? (
                  <Field label="搜证方式">
                    <select
                      className="input text-xs"
                      value={phase.searchMode || 'pick'}
                      disabled={disabled}
                      onChange={(e) => patchPhase(index, { searchMode: e.target.value })}
                    >
                      {SEARCH_MODES.map(([value, label]) => (
                        <option key={value} value={value}>
                          {label}
                        </option>
                      ))}
                    </select>
                  </Field>
                ) : null}
                {phase.type === 'search' ? (
                  <Field label="每人搜证次数">
                    <input
                      type="number"
                      min={1}
                      className="input text-xs"
                      value={phase.perPlayerPick ?? 1}
                      disabled={disabled}
                      onChange={(e) => patchPhase(index, { perPlayerPick: Number(e.target.value) || 1 })}
                    />
                  </Field>
                ) : null}
                {phase.type === 'discussion' ? (
                  <Field label="每人发言次数">
                    <input
                      type="number"
                      min={1}
                      className="input text-xs"
                      value={phase.speechPerPlayer ?? 1}
                      disabled={disabled}
                      onChange={(e) => patchPhase(index, { speechPerPlayer: Number(e.target.value) || 1 })}
                    />
                  </Field>
                ) : null}
                <Field label="超时（秒）" hint="0 = 服务端默认">
                  <input
                    type="number"
                    min={0}
                    className="input text-xs"
                    value={phase.timeoutSec ?? 0}
                    disabled={disabled}
                    onChange={(e) => patchPhase(index, { timeoutSec: Number(e.target.value) || 0 })}
                  />
                </Field>
                <Field label="阶段 ID">
                  <input
                    className="input font-mono text-xs"
                    value={phase.id ?? ''}
                    disabled={disabled}
                    onChange={(e) => patchPhase(index, { id: e.target.value })}
                  />
                </Field>
              </div>
              {phase.type === 'search' ? (
                <div className="mt-2">
                  <Field label="线索池" hint="线索 ID，用逗号分隔">
                    <input
                      className="input font-mono text-xs"
                      value={(phase.cluePool ?? []).join(', ')}
                      disabled={disabled}
                      onChange={(e) => {
                        const parts = e.target.value
                          .split(/[，,]/)
                          .map((s) => s.trim())
                          .filter(Boolean)
                        patchPhase(index, { cluePool: parts.length ? parts : undefined })
                      }}
                    />
                  </Field>
                </div>
              ) : null}
              <div className="mt-2">
                <Field label="主持人手册" hint="只进主持人视图，不会发给 agent">
                  <textarea
                    className="input min-h-[60px] text-xs"
                    value={phase.dmScript ?? ''}
                    disabled={disabled}
                    onChange={(e) => patchPhase(index, { dmScript: e.target.value })}
                  />
                </Field>
              </div>
            </div>
          ))}
        </div>
        <p className="mt-2 text-[11px] leading-relaxed text-slate-500">
          角色、线索、真相和事实表结构复杂，请切到「JSON」模式编辑；这里用于快速调阶段顺序、轮次、搜证方式、顺序策略与时限。
        </p>
      </div>
    </div>
  )
}

function extractErrors(payload: unknown): ValidationError[] {
  if (!payload || typeof payload !== 'object' || !('errors' in payload)) return []
  const raw = (payload as { errors?: unknown }).errors
  if (!Array.isArray(raw)) return []
  const out: ValidationError[] = []
  for (const item of raw) {
    const parsed = validationErrorSchema.safeParse(item)
    if (parsed.success) out.push(parsed.data)
  }
  return out
}
