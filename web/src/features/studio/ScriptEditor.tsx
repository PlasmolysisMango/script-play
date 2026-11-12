import { useEffect, useState, type ReactNode } from 'react'
import { Field, Tag } from '../../components/ui'
import { MarkdownField } from '../../components/Markdown'
import {
  DIFFICULTIES,
  PHASE_ORDERS,
  PHASE_TYPES,
  SEARCH_MODES,
  blankClue,
  blankFact,
  blankPhase,
  blankRole,
  blankSecret,
  moveAt,
  orderHint,
  removeAt,
  replaceAt,
  secretIndex,
  splitIds,
  type Clue,
  type DrawRule,
  type Fact,
  type Phase,
  type Role,
  type ScriptBody,
  type Secret,
  type Truth,
} from './draft'

type Opt = { id: string; label: string }

/** 抽卡规则在表单里写成「卡类:张数」，逗号中英文都认，写错的那段直接忽略。 */
function drawsText(draws?: DrawRule[]): string {
  return (draws ?? []).map((d) => `${d.kind}:${d.count}`).join(', ')
}

function parseDraws(text: string): DrawRule[] {
  const out: DrawRule[] = []
  for (const part of text.split(/[,，、]/)) {
    const [kind, count] = part.split(/[:：]/)
    const n = Number((count ?? '').trim())
    if (!kind?.trim() || !Number.isFinite(n) || n <= 0) continue
    out.push({ kind: kind.trim(), count: Math.round(n) })
  }
  return out
}

/**
 * 「分类抽取」输入框。归本地草稿管，失焦时才把整串解析结果交上去。
 *
 * 直接受控于 `phase.draws` 会把每个中间态都当成新值：敲到「华丽探险:」时
 * `parseDraws` 还抽不出规则，回写的空数组立刻把输入框清掉，于是这个字段只能
 * 整段粘贴、不能逐字输入。
 */
function DrawsField({
  draws,
  disabled,
  onCommit,
}: {
  draws?: DrawRule[]
  disabled: boolean
  onCommit: (next: DrawRule[]) => void
}) {
  const committed = drawsText(draws)
  const [text, setText] = useState(committed)
  useEffect(() => setText(committed), [committed])
  return (
    <input
      className="input text-xs"
      value={text}
      placeholder="随身物品:1, 轮船线索:3"
      disabled={disabled}
      onChange={(e) => {
        setText(e.target.value)
        // 已经完整的规则照旧即时生效，半截的那条留到失焦再定。
        const next = parseDraws(e.target.value)
        if (next.length) onCommit(next)
      }}
      onBlur={() => {
        const next = parseDraws(text)
        setText(drawsText(next))
        onCommit(next)
      }}
    />
  )
}

export type ScriptEditorProps = {
  draft: ScriptBody
  disabled: boolean
  onChange: (next: ScriptBody) => void
  /** Uploads a picture for the open script and resolves to the URL to embed. */
  onUploadImage?: (file: File) => Promise<string | null>
}

/**
 * The whole script, editable by hand.
 *
 * Everything the JSON importer accepts has a control here — meta, the phase
 * chain, roles with their private book, clues with pictures, the fact table and
 * the solution — so an author never has to switch to raw JSON to add a role or
 * move a picture. JSON mode stays available for bulk edits and for scripts
 * produced elsewhere.
 *
 * Long lists (roles, clues) stay collapsed until touched: a K7-sized script is
 * several thousand words, and a page that renders all of it is unusable on a
 * phone.
 */
export function ScriptEditor({ draft, disabled, onChange, onUploadImage }: ScriptEditorProps) {
  const roles = draft.roles ?? []
  const clues = draft.clues ?? []
  const phases = draft.phases ?? []
  const facts = draft.facts ?? []
  const roleOpts: Opt[] = roles.filter((r) => r.id).map((r) => ({ id: r.id as string, label: r.name || (r.id as string) }))
  const clueOpts: Opt[] = clues.filter((c) => c.id).map((c) => ({ id: c.id as string, label: c.title || (c.id as string) }))
  const factOpts: Opt[] = facts.filter((f) => f.id).map((f) => ({ id: f.id as string, label: f.id as string }))
  const secrets = secretIndex(roles)

  return (
    <div className="space-y-5">
      <MetaSection
        meta={draft.meta ?? {}}
        roleCount={roles.length}
        disabled={disabled}
        onChange={(meta) => onChange({ ...draft, meta })}
      />
      <PhaseSection
        phases={phases}
        clueOpts={clueOpts}
        roles={roles}
        disabled={disabled}
        onUploadImage={onUploadImage}
        onChange={(next) => onChange({ ...draft, phases: next })}
      />
      <RoleSection
        roles={roles}
        facts={factOpts}
        disabled={disabled}
        onUploadImage={onUploadImage}
        onChange={(next) => onChange({ ...draft, roles: next })}
      />
      <ClueSection
        clues={clues}
        roles={roleOpts}
        secrets={secrets}
        facts={factOpts}
        disabled={disabled}
        onUploadImage={onUploadImage}
        onChange={(next) => onChange({ ...draft, clues: next })}
      />
      <FactSection facts={facts} disabled={disabled} onChange={(next) => onChange({ ...draft, facts: next })} />
      <TruthSection
        truth={draft.truth ?? {}}
        roles={roleOpts}
        disabled={disabled}
        onUploadImage={onUploadImage}
        onChange={(truth) => onChange({ ...draft, truth })}
      />
    </div>
  )
}

// ---- scaffolding ----

function Block({ title, aside, hint, children }: { title: string; aside?: ReactNode; hint?: string; children: ReactNode }) {
  return (
    <section className="rounded-xl border border-line/70 bg-ink/25 p-3">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-xs font-semibold text-slate-200">{title}</h4>
        {aside}
      </div>
      {hint ? <p className="mb-2 text-[11px] leading-relaxed text-slate-500">{hint}</p> : null}
      {children}
    </section>
  )
}

/** One collapsed row in a collection; expanding shows its fields. */
function Item({
  open,
  title,
  meta,
  onToggle,
  onUp,
  onDown,
  canUp,
  canDown,
  onDelete,
  disabled,
  extra,
  children,
}: {
  open: boolean
  title: string
  meta?: string
  onToggle: () => void
  onUp: () => void
  onDown: () => void
  canUp: boolean
  canDown: boolean
  onDelete: () => void
  disabled: boolean
  extra?: ReactNode
  children: ReactNode
}) {
  return (
    <li className="rounded-lg border border-line/70 bg-panel/40">
      <div className="flex items-center gap-1 p-1.5">
        <button type="button" className="min-w-0 flex-1 text-left" onClick={onToggle} aria-expanded={open}>
          <span className="flex items-center gap-1.5">
            <span className="shrink-0 text-[10px] text-slate-500">{open ? '▾' : '▸'}</span>
            <span className="truncate text-xs font-medium text-strong">{title || '未命名'}</span>
          </span>
          {meta ? <span className="mt-0.5 block truncate pl-4 text-[11px] text-slate-500">{meta}</span> : null}
        </button>
        {extra}
        <button type="button" className="btn-ghost px-1.5 py-0.5 text-[11px]" disabled={disabled || !canUp} onClick={onUp} title="上移">
          ↑
        </button>
        <button type="button" className="btn-ghost px-1.5 py-0.5 text-[11px]" disabled={disabled || !canDown} onClick={onDown} title="下移">
          ↓
        </button>
        <button type="button" className="btn-ghost px-1.5 py-0.5 text-[11px] text-rose-300" disabled={disabled} onClick={onDelete}>
          删
        </button>
      </div>
      {open ? <div className="space-y-2.5 border-t border-line/60 p-2.5">{children}</div> : null}
    </li>
  )
}

/** A set of toggle chips: ids the author picks from what the script already has. */
function IdChips({
  label,
  hint,
  options,
  value,
  onChange,
  disabled,
}: {
  label: string
  hint?: string
  options: Opt[]
  value: string[]
  onChange: (next: string[]) => void
  disabled?: boolean
}) {
  const has = (id: string) => value.includes(id)
  return (
    <div>
      <span className="label">{label}</span>
      {options.length === 0 ? (
        <p className="text-[11px] text-slate-500">{hint ?? '还没有可选的条目'}</p>
      ) : (
        <div className="flex flex-wrap gap-1">
          {options.map((o) => (
            <button
              key={o.id}
              type="button"
              disabled={disabled}
              title={o.id}
              className={`chip text-[11px] transition ${
                has(o.id) ? 'border-accent/60 bg-accent/15 text-strong' : 'text-slate-400 hover:text-strong'
              }`}
              onClick={() => onChange(has(o.id) ? value.filter((v) => v !== o.id) : [...value, o.id])}
            >
              {o.label}
            </button>
          ))}
        </div>
      )}
      {hint && options.length > 0 ? <span className="mt-1 block text-[11px] text-slate-500">{hint}</span> : null}
    </div>
  )
}

/** Editable rows of plain strings: goals, timeline beats. */
function LinesField({
  label,
  hint,
  value,
  onChange,
  disabled,
  placeholder,
  addLabel = '添加一行',
}: {
  label: string
  hint?: string
  value: string[]
  onChange: (next: string[]) => void
  disabled?: boolean
  placeholder?: string
  addLabel?: string
}) {
  return (
    <div>
      <span className="label">{label}</span>
      <div className="space-y-1">
        {value.map((line, i) => (
          <div key={i} className="flex items-center gap-1">
            <input
              className="input text-xs"
              value={line}
              placeholder={placeholder}
              disabled={disabled}
              onChange={(e) => onChange(replaceAt(value, i, e.target.value))}
            />
            <button
              type="button"
              className="btn-ghost shrink-0 px-1.5 py-0.5 text-[11px] text-rose-300"
              disabled={disabled}
              onClick={() => onChange(removeAt(value, i))}
            >
              删
            </button>
          </div>
        ))}
        <button type="button" className="btn-ghost text-[11px]" disabled={disabled} onClick={() => onChange([...value, ''])}>
          + {addLabel}
        </button>
      </div>
      {hint ? <span className="mt-1 block text-[11px] text-slate-500">{hint}</span> : null}
    </div>
  )
}

function Count({ n, unit }: { n: number; unit: string }) {
  return <Tag>{n} {unit}</Tag>
}

// ---- sections ----

function MetaSection({
  meta,
  roleCount,
  disabled,
  onChange,
}: {
  meta: NonNullable<ScriptBody['meta']>
  roleCount: number
  disabled: boolean
  onChange: (next: NonNullable<ScriptBody['meta']>) => void
}) {
  const count = meta.playerCount ?? {}
  const patch = (p: Partial<typeof meta>) => onChange({ ...meta, ...p })
  return (
    <Block title="基础信息">
      <div className="grid gap-2 sm:grid-cols-2">
        <Field label="剧本标题">
          <input className="input text-xs" value={meta.title ?? ''} disabled={disabled} onChange={(e) => patch({ title: e.target.value })} />
        </Field>
        <Field label="作者">
          <input className="input text-xs" value={meta.author ?? ''} disabled={disabled} onChange={(e) => patch({ author: e.target.value })} />
        </Field>
        <Field label="系列" hint="如「K的游戏」「AI创作」；没有特定系列就留空">
          <input className="input text-xs" value={meta.series ?? ''} disabled={disabled} onChange={(e) => patch({ series: e.target.value })} />
        </Field>
        <Field label="最少人数">
          <input
            type="number"
            min={2}
            className="input text-xs"
            value={count.min ?? 0}
            disabled={disabled}
            onChange={(e) => patch({ playerCount: { ...count, min: Number(e.target.value) || 0 } })}
          />
        </Field>
        <Field label="最多人数" hint={roleCount > 0 ? `不能超过角色数（${roleCount}）` : '不能超过角色数'}>
          <input
            type="number"
            min={2}
            max={roleCount > 0 ? roleCount : undefined}
            className="input text-xs"
            value={count.max ?? 0}
            disabled={disabled}
            onChange={(e) => patch({ playerCount: { ...count, max: Number(e.target.value) || 0 } })}
          />
        </Field>
        <Field label="难度">
          <select className="input text-xs" value={meta.difficulty ?? ''} disabled={disabled} onChange={(e) => patch({ difficulty: e.target.value })}>
            <option value="">不标注</option>
            {DIFFICULTIES.map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="时长（分钟）">
          <input
            type="number"
            min={0}
            className="input text-xs"
            value={meta.durationMin ?? 0}
            disabled={disabled}
            onChange={(e) => patch({ durationMin: Number(e.target.value) || 0 })}
          />
        </Field>
        <div className="sm:col-span-2">
          <Field label="标签" hint="逗号或顿号分隔，大厅里用于筛选">
            <input
              className="input text-xs"
              value={(meta.tags ?? []).join('、')}
              disabled={disabled}
              onChange={(e) => patch({ tags: splitIds(e.target.value) })}
            />
          </Field>
        </div>
        <div className="sm:col-span-2">
          <Field label="简介" hint="大厅列表里的一句话">
            <textarea
              className="input text-xs"
              rows={3}
              value={meta.summary ?? ''}
              disabled={disabled}
              onChange={(e) => patch({ summary: e.target.value })}
            />
          </Field>
        </div>
      </div>
    </Block>
  )
}

function PhaseSection({
  phases,
  clueOpts,
  roles,
  disabled,
  onUploadImage,
  onChange,
}: {
  phases: Phase[]
  clueOpts: Opt[]
  roles: Role[]
  disabled: boolean
  onUploadImage?: (file: File) => Promise<string | null>
  onChange: (next: Phase[]) => void
}) {
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const patch = (index: number, p: Partial<Phase>) => onChange(replaceAt(phases, index, { ...phases[index], ...p }))
  // Switching the type also fills the fields the engine reads for it, so a card
  // switched to "搜索" never imports with a zero pick count.
  const setType = (index: number, type: string) => {
    const phase = phases[index] ?? {}
    if (type === 'search') {
      patch(index, { type, perPlayerPick: phase.perPlayerPick ?? 2, searchMode: phase.searchMode ?? 'pick', order: phase.order ?? 'round_robin' })
      return
    }
    if (type === 'discussion') {
      patch(index, { type, speechPerPlayer: phase.speechPerPlayer ?? 1, order: phase.order ?? 'round_robin' })
      return
    }
    patch(index, { type })
  }
  return (
    <Block
      title="阶段流程"
      aside={
        <div className="flex items-center gap-2">
          <Count n={phases.length} unit="段" />
          <button
            type="button"
            className="btn-ghost text-[11px]"
            disabled={disabled}
            onClick={() => {
              const next = [...phases, blankPhase(phases)]
              onChange(next)
              setOpen((o) => ({ ...o, [String(next[next.length - 1].id)]: true }))
            }}
          >
            + 添加阶段
          </button>
        </div>
      }
      hint="校验要求：第一段必须是 reading，最后必须有一个 reveal；搜证段要填搜证次数，讨论段要填发言次数。"
    >
      <ul className="space-y-1.5">
        {phases.map((phase, index) => {
          const key = String(phase.id ?? index)
          const isOpen = !!open[key]
          return (
            <Item
              key={key}
              open={isOpen}
              title={`${index + 1}. ${phase.name || '未命名阶段'}`}
              meta={`${phase.type ?? '?'}${phase.round ? ` · 第 ${phase.round} 轮` : ''}${phase.perPlayerPick ? ` · 每人搜 ${phase.perPlayerPick} 次` : ''}${
                phase.speechPerPlayer ? ` · 每人发言 ${phase.speechPerPlayer} 次` : ''
              }`}
              onToggle={() => setOpen((o) => ({ ...o, [key]: !isOpen }))}
              onUp={() => onChange(moveAt(phases, index, -1))}
              onDown={() => onChange(moveAt(phases, index, 1))}
              canUp={index > 0}
              canDown={index < phases.length - 1}
              onDelete={() => onChange(removeAt(phases, index))}
              disabled={disabled}
            >
              <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                <Field label="类型">
                  <select className="input text-xs" value={phase.type ?? ''} disabled={disabled} onChange={(e) => setType(index, e.target.value)}>
                    {PHASE_TYPES.map(([value, label]) => (
                      <option key={value} value={value}>
                        {label}（{value}）
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label="名称">
                  <input className="input text-xs" value={phase.name ?? ''} disabled={disabled} onChange={(e) => patch(index, { name: e.target.value })} />
                </Field>
                <Field label="阶段 ID" hint="改动会让引用它的地方失效">
                  <input
                    className="input font-mono text-xs"
                    value={phase.id ?? ''}
                    disabled={disabled}
                    onChange={(e) => patch(index, { id: e.target.value })}
                  />
                </Field>
                <Field label="轮次">
                  <input
                    type="number"
                    min={0}
                    className="input text-xs"
                    value={phase.round ?? 0}
                    disabled={disabled}
                    onChange={(e) => patch(index, { round: Number(e.target.value) || 0 })}
                  />
                </Field>
                {phase.type === 'search' || phase.type === 'discussion' ? (
                  <Field label="顺序策略" hint={orderHint(phase.order)}>
                    <select
                      className="input text-xs"
                      value={phase.order || 'round_robin'}
                      disabled={disabled}
                      onChange={(e) => patch(index, { order: e.target.value })}
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
                  <>
                    <Field label="搜证方式">
                      <select
                        className="input text-xs"
                        value={phase.searchMode || 'pick'}
                        disabled={disabled}
                        onChange={(e) => patch(index, { searchMode: e.target.value })}
                      >
                        {SEARCH_MODES.map(([value, label]) => (
                          <option key={value} value={value}>
                            {label}
                          </option>
                        ))}
                      </select>
                    </Field>
                    <Field label="每人搜证次数">
                      <input
                        type="number"
                        min={1}
                        className="input text-xs"
                        value={phase.perPlayerPick ?? 1}
                        disabled={disabled}
                        onChange={(e) => patch(index, { perPlayerPick: Number(e.target.value) || 1 })}
                      />
                    </Field>
                    <Field label="限定角色" hint="只让这些角色占回合，如「查理的记忆」只由黑猫抽取；留空 = 全场轮转">
                      <div className="flex flex-wrap gap-2">
                        {roles.map((r) => {
                          const on = (phase.onlyRoleIds ?? []).includes(r.id ?? '')
                          return (
                            <label
                              key={r.id}
                              className="flex items-center gap-1 rounded-md border border-line/60 px-2 py-1 text-xs text-slate-300"
                            >
                              <input
                                type="checkbox"
                                className="h-3.5 w-3.5 accent-[rgb(var(--accent))]"
                                checked={on}
                                disabled={disabled}
                                onChange={(e) => {
                                  const cur = phase.onlyRoleIds ?? []
                                  patch(index, {
                                    onlyRoleIds: e.target.checked
                                      ? [...cur, r.id ?? ''].filter(Boolean)
                                      : cur.filter((id) => id !== r.id),
                                  })
                                }}
                              />
                              {r.name}
                            </label>
                          )
                        })}
                      </div>
                    </Field>
                    <Field
                      label="分类抽取"
                      hint="留空 = 从线索池自由抽；填写后按顺序按类抽，如 随身物品:1、轮船线索:3"
                    >
                      <DrawsField
                        draws={phase.draws}
                        disabled={disabled}
                        onCommit={(draws) => patch(index, { draws })}
                      />
                    </Field>
                    <div className="flex items-end pb-1">
                      <label className="flex items-center gap-2 text-xs text-slate-300">
                        <input
                          type="checkbox"
                          className="h-4 w-4 accent-[rgb(var(--accent))]"
                          checked={!!phase.revealOnFind}
                          disabled={disabled}
                          onChange={(e) => patch(index, { revealOnFind: e.target.checked })}
                        />
                        搜到的线索立即公开
                      </label>
                    </div>
                  </>
                ) : null}
                {phase.type === 'discussion' ? (
                  <Field label="每人发言次数">
                    <input
                      type="number"
                      min={1}
                      className="input text-xs"
                      value={phase.speechPerPlayer ?? 1}
                      disabled={disabled}
                      onChange={(e) => patch(index, { speechPerPlayer: Number(e.target.value) || 1 })}
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
                    onChange={(e) => patch(index, { timeoutSec: Number(e.target.value) || 0 })}
                  />
                </Field>
              </div>
              {phase.type === 'search' ? (
                <IdChips
                  label="线索池"
                  hint="不选 = 本段可搜全部线索"
                  options={clueOpts}
                  value={phase.cluePool ?? []}
                  disabled={disabled}
                  onChange={(cluePool) => patch(index, { cluePool })}
                />
              ) : null}
              <MarkdownField
                label="主持人手册"
                hint="只进主持人视图，不会发给 agent"
                value={phase.dmScript ?? ''}
                disabled={disabled}
                rows={4}
                onChange={(dmScript) => patch(index, { dmScript })}
                onUpload={disabled ? undefined : onUploadImage}
              />
            </Item>
          )
        })}
      </ul>
      {phases.length === 0 ? (
        <p className="text-xs text-slate-500">还没有阶段。至少要有「阅读剧本 … 揭晓」这条链，引擎才能跑完整局。</p>
      ) : null}
    </Block>
  )
}

function RoleSection({
  roles,
  facts,
  disabled,
  onUploadImage,
  onChange,
}: {
  roles: Role[]
  facts: Opt[]
  disabled: boolean
  onUploadImage?: (file: File) => Promise<string | null>
  onChange: (next: Role[]) => void
}) {
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const patch = (index: number, p: Partial<Role>) => onChange(replaceAt(roles, index, { ...roles[index], ...p }))
  const patchPrivate = (index: number, p: Partial<NonNullable<Role['private']>>) =>
    patch(index, { private: { ...roles[index].private, ...p } })
  const patchPublic = (index: number, p: Partial<NonNullable<Role['public']>>) =>
    patch(index, { public: { ...roles[index].public, ...p } })
  return (
    <Block
      title="角色与角色本"
      aside={
        <div className="flex items-center gap-2">
          <Count n={roles.length} unit="人" />
          <button
            type="button"
            className="btn-ghost text-[11px]"
            disabled={disabled}
            onClick={() => {
              const next = [...roles, blankRole(roles)]
              onChange(next)
              setOpen((o) => ({ ...o, [String(next[next.length - 1].id)]: true }))
            }}
          >
            + 添加角色
          </button>
        </div>
      }
      hint="私密剧本只有该角色（及其 AI 座位）能看到；公开介绍开局就摊在桌上。角色本支持 Markdown。"
    >
      <ul className="space-y-1.5">
        {roles.map((role, index) => {
          const key = String(role.id ?? index)
          const isOpen = !!open[key]
          const secrets = role.private?.secrets ?? []
          return (
            <Item
              key={key}
              open={isOpen}
              title={role.name || '未命名角色'}
              meta={`${role.id ?? '无 id'} · 秘密 ${secrets.length} · 目标 ${(role.private?.goals ?? []).length}`}
              onToggle={() => setOpen((o) => ({ ...o, [key]: !isOpen }))}
              onUp={() => onChange(moveAt(roles, index, -1))}
              onDown={() => onChange(moveAt(roles, index, 1))}
              canUp={index > 0}
              canDown={index < roles.length - 1}
              onDelete={() => onChange(removeAt(roles, index))}
              disabled={disabled}
            >
              <div className="grid gap-2 sm:grid-cols-2">
                <Field label="角色名">
                  <input className="input text-xs" value={role.name ?? ''} disabled={disabled} onChange={(e) => patch(index, { name: e.target.value })} />
                </Field>
                <Field label="角色 ID" hint="真相、线索归属都用它引用">
                  <input
                    className="input font-mono text-xs"
                    value={role.id ?? ''}
                    disabled={disabled}
                    onChange={(e) => patch(index, { id: e.target.value })}
                  />
                </Field>
                <Field label="性格预设 ID" hint="留空 = AI 随机挑一个">
                  <input
                    className="input font-mono text-xs"
                    value={role.personaId ?? ''}
                    disabled={disabled}
                    onChange={(e) => patch(index, { personaId: e.target.value })}
                  />
                </Field>
                <Field label="楼层" hint="「不能抽自己楼层」靠它配对，不分区就留空">
                  <input
                    className="input text-xs"
                    value={role.floor ?? ''}
                    placeholder="如 二楼"
                    disabled={disabled}
                    onChange={(e) => patch(index, { floor: e.target.value })}
                  />
                </Field>
                <Field label="公开人设">
                  <input
                    className="input text-xs"
                    value={role.public?.appearance ?? ''}
                    placeholder="一句话，别人眼里的他"
                    disabled={disabled}
                    onChange={(e) => patchPublic(index, { appearance: e.target.value })}
                  />
                </Field>
                <div className="sm:col-span-2">
                  <Field label="公开介绍" hint="开局所有人可见">
                    <textarea
                      className="input text-xs"
                      rows={2}
                      value={role.public?.intro ?? ''}
                      disabled={disabled}
                      onChange={(e) => patchPublic(index, { intro: e.target.value })}
                    />
                  </Field>
                </div>
              </div>

              <MarkdownField
                label="私密剧本"
                hint="支持 ## 标题、**加粗**、*斜体*、- 列表、> 引用、```代码```、![图](url)、**18:23** 时间戳"
                value={role.private?.script ?? ''}
                disabled={disabled}
                rows={10}
                minHeight="14rem"
                placeholder={'## 你的身份\n你是星火号的船长……\n\n**9月8日18:15** 你听见货舱传来巨响。'}
                onChange={(script) => patchPrivate(index, { script })}
                onUpload={disabled ? undefined : onUploadImage}
              />

              <div>
                <div className="mb-1 flex items-center justify-between">
                  <span className="label mb-0">秘密（可主动透露的把柄）</span>
                  <button
                    type="button"
                    className="btn-ghost text-[11px]"
                    disabled={disabled}
                    onClick={() => patchPrivate(index, { secrets: [...secrets, blankSecret(secrets)] })}
                  >
                    + 添加秘密
                  </button>
                </div>
                <div className="space-y-1">
                  {secrets.map((secret, j) => (
                    <SecretRow
                      key={`${secret.id ?? 'secret'}-${j}`}
                      secret={secret}
                      disabled={disabled}
                      onChange={(next) => patchPrivate(index, { secrets: replaceAt(secrets, j, next) })}
                      onDelete={() => patchPrivate(index, { secrets: removeAt(secrets, j) })}
                    />
                  ))}
                  {secrets.length === 0 ? <p className="text-[11px] text-slate-500">还没有秘密。</p> : null}
                </div>
              </div>

              <LinesField
                label="本局目标"
                value={role.private?.goals ?? []}
                disabled={disabled}
                placeholder="例如：拿回船票，别让人发现你改过日志"
                onChange={(goals) => patchPrivate(index, { goals })}
              />

              <IdChips
                label="初始已知事实"
                hint="勾选后这条事实会写进该角色的提示词"
                options={facts}
                value={role.private?.knownFactIds ?? []}
                disabled={disabled}
                onChange={(knownFactIds) => patchPrivate(index, { knownFactIds })}
              />
            </Item>
          )
        })}
      </ul>
      {roles.length === 0 ? (
        <p className="text-xs text-slate-500">还没有角色。玩家数上限不能超过角色数，所以先加人再发行。</p>
      ) : null}
    </Block>
  )
}

function SecretRow({
  secret,
  disabled,
  onChange,
  onDelete,
}: {
  secret: Secret
  disabled: boolean
  onChange: (next: Secret) => void
  onDelete: () => void
}) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_4.5rem_auto] items-center gap-1 sm:grid-cols-[7rem_minmax(0,1fr)_4.5rem_auto]">
      <input
        className="input font-mono text-[11px] sm:col-auto"
        value={secret.id ?? ''}
        placeholder="secret id"
        disabled={disabled}
        onChange={(e) => onChange({ ...secret, id: e.target.value })}
      />
      <input
        className="input text-xs"
        value={secret.text ?? ''}
        placeholder="瞒着别人的事"
        disabled={disabled}
        onChange={(e) => onChange({ ...secret, text: e.target.value })}
      />
      <input
        type="number"
        step="0.1"
        min={0}
        max={1}
        className="input text-xs"
        title="revealCost：0 轻松说出口，1 死也不说"
        value={secret.revealCost ?? 0.5}
        disabled={disabled}
        onChange={(e) => onChange({ ...secret, revealCost: Number(e.target.value) || 0 })}
      />
      <button type="button" className="btn-ghost px-1.5 py-0.5 text-[11px] text-rose-300" disabled={disabled} onClick={onDelete}>
        删
      </button>
    </div>
  )
}

function ClueSection({
  clues,
  roles,
  secrets,
  facts,
  disabled,
  onUploadImage,
  onChange,
}: {
  clues: Clue[]
  roles: Opt[]
  secrets: { id: string; roleName: string; text: string }[]
  facts: Opt[]
  disabled: boolean
  onUploadImage?: (file: File) => Promise<string | null>
  onChange: (next: Clue[]) => void
}) {
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const patch = (index: number, p: Partial<Clue>) => onChange(replaceAt(clues, index, { ...clues[index], ...p }))
  const patchLinks = (index: number, p: Partial<NonNullable<Clue['links']>>) =>
    patch(index, { links: { ...clues[index].links, ...p } })
  return (
    <Block
      title="线索"
      aside={
        <div className="flex items-center gap-2">
          <Count n={clues.length} unit="条" />
          <button
            type="button"
            className="btn-ghost text-[11px]"
            disabled={disabled}
            onClick={() => {
              const next = [...clues, blankClue(clues)]
              onChange(next)
              setOpen((o) => ({ ...o, [String(next[next.length - 1].id)]: true }))
            }}
          >
            + 添加线索
          </button>
        </div>
      }
      hint="「卡背」是搜证时只露出来的那一行，别写内容；「内容」是拿到手才看得到的证据。图片会存到服务端，剧本里只留相对路径。"
    >
      <ul className="space-y-1.5">
        {clues.map((clue, index) => {
          const key = String(clue.id ?? index)
          const isOpen = !!open[key]
          // Collapsed rows are how an author scans fifty cards: the badges must
          // say at a glance what the card is gated by.
          const badges = [
            clue.isPublic ? '公共' : '',
            clue.mustReveal ? '必公开' : '',
            clue.ownerRoleId ? '私人物品' : '',
            clue.subjectRoleId ? '搜身' : '',
            clue.requiresItems?.length ? '需物品' : '',
            clue.unlockedBy?.length ? '有前提' : '',
            clue.keywords?.length ? '关键词' : '',
            clue.imageUrl ? '有图' : '',
          ].filter(Boolean)
          return (
            <Item
              key={key}
              open={isOpen}
              title={clue.title || '未命名线索'}
              meta={`${clue.id ?? '无 id'}${badges.map((b) => ` · ${b}`).join('')}`}
              onToggle={() => setOpen((o) => ({ ...o, [key]: !isOpen }))}
              onUp={() => onChange(moveAt(clues, index, -1))}
              onDown={() => onChange(moveAt(clues, index, 1))}
              canUp={index > 0}
              canDown={index < clues.length - 1}
              onDelete={() => onChange(removeAt(clues, index))}
              disabled={disabled}
            >
              <div className="grid gap-2 sm:grid-cols-2">
                <Field label="标题" hint="搜到之后才知道这是证据">
                  <input className="input text-xs" value={clue.title ?? ''} disabled={disabled} onChange={(e) => patch(index, { title: e.target.value })} />
                </Field>
                <Field label="线索 ID">
                  <input
                    className="input font-mono text-xs"
                    value={clue.id ?? ''}
                    disabled={disabled}
                    onChange={(e) => patch(index, { id: e.target.value })}
                  />
                </Field>
                <Field label="地点">
                  <input className="input text-xs" value={clue.location ?? ''} disabled={disabled} onChange={(e) => patch(index, { location: e.target.value })} />
                </Field>
                <Field label="楼层" hint="与角色楼层相同的座位抽不到它">
                  <input
                    className="input text-xs"
                    value={clue.floor ?? ''}
                    placeholder="如 二楼"
                    disabled={disabled}
                    onChange={(e) => patch(index, { floor: e.target.value })}
                  />
                </Field>
                <Field label="卡背" hint="搜证选项上露出的那一行，如「船长 07」">
                  <input className="input text-xs" value={clue.back ?? ''} disabled={disabled} onChange={(e) => patch(index, { back: e.target.value })} />
                </Field>
                <Field label="归属角色" hint="私人物品只有他能搜到">
                  <select
                    className="input text-xs"
                    value={clue.ownerRoleId ?? ''}
                    disabled={disabled}
                    onChange={(e) => patch(index, { ownerRoleId: e.target.value })}
                  >
                    <option value="">无（公共位置）</option>
                    {roles.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.label}
                      </option>
                    ))}
                  </select>
                </Field>
                <div className="flex items-end pb-1">
                  <label className="flex items-center gap-2 text-xs text-slate-300">
                    <input
                      type="checkbox"
                      className="h-4 w-4 accent-[rgb(var(--accent))]"
                      checked={!!clue.isPublic}
                      disabled={disabled}
                      onChange={(e) => patch(index, { isPublic: e.target.checked })}
                    />
                    开局即公共线索
                  </label>
                </div>
                <div className="flex items-end pb-1">
                  <label className="flex items-center gap-2 text-xs text-slate-300">
                    <input
                      type="checkbox"
                      className="h-4 w-4 accent-[rgb(var(--accent))]"
                      checked={!!clue.mustReveal}
                      disabled={disabled}
                      onChange={(e) => patch(index, { mustReveal: e.target.checked })}
                    />
                    搜到必须公开
                  </label>
                </div>
                <div className="flex items-end pb-1">
                  <label className="flex items-center gap-2 text-xs text-slate-300">
                    <input
                      type="checkbox"
                      className="h-4 w-4 accent-[rgb(var(--accent))]"
                      checked={!!clue.extraDraw}
                      disabled={disabled}
                      onChange={(e) => patch(index, { extraDraw: e.target.checked })}
                    />
                    可以再抽一张
                  </label>
                </div>
                <Field label="卡类" hint="「随身物品」「轮船线索」这类卡面分类，阶段分类抽卡时靠它配对">
                  <input
                    className="input text-xs"
                    value={clue.kind ?? ''}
                    disabled={disabled}
                    onChange={(e) => patch(index, { kind: e.target.value })}
                  />
                </Field>
                <Field label="搜身角色" hint="这条线索说的是谁：本人抽不到，别人照常可搜">
                  <select
                    className="input text-xs"
                    value={clue.subjectRoleId ?? ''}
                    disabled={disabled}
                    onChange={(e) => patch(index, { subjectRoleId: e.target.value })}
                  >
                    <option value="">无</option>
                    {roles.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.label}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label="获得物品" hint="搜到即放进该座位物品栏，如「钳子」">
                  <input
                    className="input text-xs"
                    value={clue.grantsItem ?? ''}
                    disabled={disabled}
                    onChange={(e) => patch(index, { grantsItem: e.target.value })}
                  />
                </Field>
                <Field label="需要物品" hint="顿号分隔；缺物品搜不到（深入调查的门槛）">
                  <input
                    className="input text-xs"
                    value={(clue.requiresItems ?? []).join('、')}
                    disabled={disabled}
                    onChange={(e) => patch(index, { requiresItems: splitIds(e.target.value) })}
                  />
                </Field>
                <Field label="关键词" hint="顿号分隔；有人说出其中一个，持有者才看到这条">
                  <input
                    className="input text-xs"
                    value={(clue.keywords ?? []).join('、')}
                    disabled={disabled}
                    onChange={(e) => patch(index, { keywords: splitIds(e.target.value) })}
                  />
                </Field>
              </div>

              <IdChips
                label="前提线索"
                hint="必须先搜到这些线索，本条才会出现在搜证列表"
                options={clues
                  .filter((c) => c.id && c.id !== clue.id)
                  .map((c) => ({ id: String(c.id), label: c.title || String(c.id) }))}
                value={clue.unlockedBy ?? []}
                disabled={disabled}
                onChange={(unlockedBy) => patch(index, { unlockedBy })}
              />

              <MarkdownField
                label="线索内容"
                hint="拿到手才可见，支持 Markdown 与插图"
                value={clue.content ?? ''}
                disabled={disabled}
                rows={5}
                onChange={(content) => patch(index, { content })}
                onUpload={disabled ? undefined : onUploadImage}
              />

              <div>
                <span className="label">线索配图</span>
                <div className="flex flex-wrap items-center gap-2">
                  <label
                    className={`btn-ghost cursor-pointer text-[11px] ${disabled || !onUploadImage ? 'pointer-events-none opacity-50' : ''}`}
                  >
                    上传图片
                    <input
                      type="file"
                      accept="image/png,image/jpeg,image/webp,image/gif"
                      className="hidden"
                      disabled={disabled}
                      onChange={(e) => {
                        const file = e.target.files?.[0]
                        e.target.value = ''
                        if (!file || !onUploadImage) return
                        void onUploadImage(file).then((url) => {
                          if (url) patch(index, { imageUrl: url })
                        })
                      }}
                    />
                  </label>
                  {clue.imageUrl ? (
                    <>
                      <button type="button" className="btn-ghost text-[11px] text-rose-300" disabled={disabled} onClick={() => patch(index, { imageUrl: '' })}>
                        移除
                      </button>
                      <span className="break-all font-mono text-[10px] text-slate-500">{clue.imageUrl}</span>
                    </>
                  ) : (
                    <span className="text-[11px] text-slate-500">也可以直接填外链</span>
                  )}
                </div>
                <input
                  className="input mt-1 font-mono text-[11px]"
                  placeholder="/media/… 或 https://…"
                  value={clue.imageUrl ?? ''}
                  disabled={disabled}
                  onChange={(e) => patch(index, { imageUrl: e.target.value })}
                />
                {clue.imageUrl ? (
                  <img
                    src={clue.imageUrl}
                    alt={clue.title ?? ''}
                    className="mt-2 max-h-48 w-auto rounded-lg border border-line/70 bg-panel"
                    loading="lazy"
                  />
                ) : null}
              </div>

              <IdChips
                label="指向角色"
                options={roles}
                value={clue.links?.roles ?? []}
                disabled={disabled}
                onChange={(next) => patchLinks(index, { roles: next })}
              />
              <IdChips
                label="指向秘密"
                options={secrets.map((s) => ({ id: s.id, label: `${s.roleName} · ${s.text.slice(0, 12) || s.id}` }))}
                value={clue.links?.secrets ?? []}
                disabled={disabled}
                onChange={(next) => patchLinks(index, { secrets: next })}
              />
              <IdChips
                label="指向事实"
                options={facts}
                value={clue.links?.facts ?? []}
                disabled={disabled}
                onChange={(next) => patchLinks(index, { facts: next })}
              />
            </Item>
          )
        })}
      </ul>
      {clues.length === 0 ? <p className="text-xs text-slate-500">还没有线索。搜证阶段没有线索可选，玩家会卡住。</p> : null}
    </Block>
  )
}

function FactSection({ facts, disabled, onChange }: { facts: Fact[]; disabled: boolean; onChange: (next: Fact[]) => void }) {
  return (
    <Block
      title="事实表"
      aside={
        <button type="button" className="btn-ghost text-[11px]" disabled={disabled} onClick={() => onChange([...facts, blankFact(facts)])}>
          + 添加事实
        </button>
      }
      hint="中性、可被引用的短句，用来喂提示词和做防剧透检测；不是给玩家看的线索。"
    >
      <div className="space-y-1">
        {facts.map((fact, i) => (
          <div key={`${fact.id ?? 'fact'}-${i}`} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-1 sm:grid-cols-[7rem_minmax(0,1fr)_auto]">
            <input
              className="input font-mono text-[11px]"
              value={fact.id ?? ''}
              placeholder="fact id"
              disabled={disabled}
              onChange={(e) => onChange(replaceAt(facts, i, { ...fact, id: e.target.value }))}
            />
            <input
              className="input text-xs"
              value={fact.text ?? ''}
              placeholder="一条不容争辩的事实"
              disabled={disabled}
              onChange={(e) => onChange(replaceAt(facts, i, { ...fact, text: e.target.value }))}
            />
            <button type="button" className="btn-ghost px-1.5 py-0.5 text-[11px] text-rose-300" disabled={disabled} onClick={() => onChange(removeAt(facts, i))}>
              删
            </button>
          </div>
        ))}
        {facts.length === 0 ? <p className="text-[11px] text-slate-500">还没有事实。可以留空。</p> : null}
      </div>
    </Block>
  )
}

function TruthSection({
  truth,
  roles,
  disabled,
  onUploadImage,
  onChange,
}: {
  truth: Truth
  roles: Opt[]
  disabled: boolean
  onUploadImage?: (file: File) => Promise<string | null>
  onChange: (next: Truth) => void
}) {
  const patch = (p: Partial<Truth>) => onChange({ ...truth, ...p })
  return (
    <Block title="真相" hint="只有主持人和复盘能看见，任何角色的提示词里都不会出现。">
      <div className="grid gap-2 sm:grid-cols-2">
        <Field label="凶手角色">
          <select className="input text-xs" value={truth.killerRoleId ?? ''} disabled={disabled} onChange={(e) => patch({ killerRoleId: e.target.value })}>
            <option value="">未指定</option>
            {roles.map((r) => (
              <option key={r.id} value={r.id}>
                {r.label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="手法">
          <input className="input text-xs" value={truth.method ?? ''} disabled={disabled} onChange={(e) => patch({ method: e.target.value })} />
        </Field>
        <div className="sm:col-span-2">
          <Field label="动机">
            <textarea className="input text-xs" rows={2} value={truth.motive ?? ''} disabled={disabled} onChange={(e) => patch({ motive: e.target.value })} />
          </Field>
        </div>
      </div>
      <div className="mt-2">
        <LinesField
          label="时间线"
          value={truth.timeline ?? []}
          disabled={disabled}
          placeholder="18:15 船长听见货舱巨响"
          onChange={(timeline) => patch({ timeline })}
        />
      </div>
      <div className="mt-2">
        <MarkdownField
          label="复盘全文"
          hint="复盘页直接渲染，可用标题、列表、插图"
          value={truth.summary ?? ''}
          disabled={disabled}
          rows={6}
          onChange={(summary) => patch({ summary })}
          onUpload={disabled ? undefined : onUploadImage}
        />
      </div>
    </Block>
  )
}
