import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { useApp } from '../../store/app'
import { Empty, Field, SectionTitle, Spinner, Tag } from '../../components/ui'
import { ScriptEditor } from './ScriptEditor'
import { blankScript, type ScriptBody } from './draft'
import { validationErrorSchema, type RawMaterial, type ScriptSummary, type ValidationError } from '../../types/schema'

type EditorMode = 'form' | 'json'

/** The editor buffer is text; the form view only needs it parsed. */
function parseDraft(text: string): ScriptBody | null {
  if (!text.trim()) return null
  try {
    const value: unknown = JSON.parse(text)
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null
    return value as ScriptBody
  } catch {
    return null
  }
}

/**
 * Script studio: the server archives raw material but never parses it, so a
 * script starts either as a pasted standard JSON or as an empty skeleton. Both
 * become a draft that must be reviewed and published before a room can use it.
 *
 * Everything the schema holds is editable here. The JSON view stays for bulk
 * edits and for scripts produced elsewhere; the visual view is the normal path,
 * because adding a role or swapping a clue picture must never require typing
 * braces.
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

  // Load the author's draft into the editor once it arrives.
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
  // skeleton carries the phases so the editor has something to edit.
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

  // Pictures are uploaded as soon as they are picked: the author sees the real
  // file in the real place, and the body only ever stores the returned URL.
  const uploadImage = async (file: File): Promise<string | null> => {
    if (!selected) return null
    try {
      const saved = await api.uploadScriptImage(selected, file)
      notify(`图片已上传：${saved.name}`)
      return saved.url
    } catch (err) {
      notify(err instanceof ApiError ? err.message : '图片上传失败', 'error')
      return null
    }
  }

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

  const remove = async () => {
    const title = selectedSummary?.title || '这份剧本'
    if (!window.confirm(`删除《${title}》？\n\n${isDraft ? '草稿删掉就没了。' : '进行中的对局不受影响，但之后不能再用它建房。'}`)) return
    try {
      await api.deleteScript(selected)
      setSelected('')
      refreshScripts()
      notify(isDraft ? '草稿已删除' : '剧本已删除')
    } catch (err) {
      notify(err instanceof ApiError ? err.message : '删除失败', 'error')
    }
  }

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
                <button type="button" className="btn-ghost" disabled={!editable} onClick={() => void remove()}>
                  删除剧本
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
                          ['form', '可视化'],
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
                {isDraft ? '编写草稿' : '编辑剧本'}
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
                <ScriptEditor
                  draft={draft}
                  disabled={!editable}
                  onChange={(next) => setEditor(JSON.stringify(next, null, 2))}
                  onUploadImage={editable ? uploadImage : undefined}
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
