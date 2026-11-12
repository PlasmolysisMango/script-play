# script-play

用 LLM Agent 替补剧本杀里的一个或多个玩家：真人用浏览器进房（可语音发言），AI 座位扮演角色同局，
**服务端负责信息边界**——每个 Agent 的 prompt 只由「自己的角色本 + 自己掌握的证据 + 公共信息」组装，
不是靠提示词自律。协议细节见 [docs/protocol.md](docs/protocol.md)，剧本格式见
[docs/script.schema.json](docs/script.schema.json)。

## 快速开始

```bash
# 后端（用 mock provider，不调用任何模型；去掉这个变量则默认走 qodercli）
cd server && SP_LLM_PROVIDER=mock go run ./cmd/server   # http://127.0.0.1:8080

# 前端
cd web && npm install && npm run dev        # http://127.0.0.1:5173，/api 与 /ws 已代理到 8080
```

浏览器打开 5173 → 注册 → 大厅「一键示例剧本」→ 建房 → 座位面板添加 AI → 开始。
只想验证对局流程时用 `SP_LLM_PROVIDER=mock`；接真实模型见下一节。

剧本库由管理员维护，首个管理员用环境变量引导（不设则**没有**管理员，大厅也无法建房）：

```bash
cd server && SP_ADMIN_USERNAME=admin SP_ADMIN_PASSWORD='换成你的密码' go run ./cmd/server
```

## 剧本库与管理台

只有 `role = admin` 的账号能新建、编辑、发布剧本；普通账号只能在大厅选已发布的剧本建房。
顶栏只有「大厅」与「管理台」两项，**剧本工作台是管理台里的一个入口**：管理台左侧点「进入剧本工作台」，
工作台左上角「← 返回管理台」回去。进了房间 / 对局 / 复盘，顶栏只留一个「返回大厅」。

**服务端不解析原始剧本。** 它只负责：归档你上传的原件、存草稿、校验发布。把 docx / pdf / 压缩包 / 照片
变成结构化剧本这一步由开发者线下完成。

1. **上传原件**：剧本工作台的「原始素材」直接传 zip、docx、pdf、照片，文件原样落到服务端
   `data/inbox/<时间戳>-raw_<随机>.<ext>`，面板上直接显示每份文件的服务端绝对路径。不解压、不 OCR、不调用
   LLM。
2. **转成标准 JSON**：按 `docs/script.schema.json` 把原件写成剧本 JSON。仓库里已转好的，一本一个目录：
   `scripts/<剧本名>/script.json`，配图放同目录 `images/`（例如 `scripts/K7-星火号/`）。剧本正文与它的图片
   放一起，换机器只搬 `scripts/` 即可。字段、默认值与校验规则见[导入剧本规范](#导入剧本规范)。
3. **录入**：三种入口，最终落到同一份结构、走同一套校验——
   - 工作台「新建空白草稿」拿一份带默认阶段的骨架（reading → 讨论 → 搜证 → 公聊 → 投票 → 复盘），
     在「可视化」模式里逐字段填；
   - 「粘贴 JSON」整份导入，之后照样可以在可视化模式里继续改；
   - `POST /api/scripts` 直接提交 JSON（脚本化批量导入）。
   格式不对也能存：导入返回的 `errors` 就是待修清单。
4. **可视化编辑**：左侧点中任一剧本即可编辑，草稿与已发布没有区别（已发布剧本保存即发布新版本，
   版本号 +1；进行中的对局仍用开局时那一版，新开的房间用新版本；已发布剧本只接受能通过校验的内容）。
   编辑器分六段，覆盖 schema 的全部字段，列表项都能增删与上下移动：
   - **基础信息**：标题、作者、简介、人数上下限、难度、时长、标签；
   - **阶段**：类型、名称、轮次、顺序策略、搜证方式、搜证/发言次数、超时、线索池、主持人手册；
   - **角色**：公开人设、公开介绍、私密剧本、秘密表（`revealCost`）、本局目标、初始已知事实；
   - **线索**：标题、卡背、地点、归属角色、是否公开、正文、配图、指向的角色/秘密/事实；
   - **事实表**与**真相**（凶手、手法、动机、时间线、复盘全文）。
   角色本、线索正文、真相、主持人手册都是 Markdown 输入框，带「编辑 / 预览」切换和「插图」按钮；
   「JSON」模式保留，用来整份替换或粘贴大段文本。
5. **配图**：两种来源——随剧本固化的图放 `scripts/<剧本名>/images/`，正文写
   `/script-assets/<剧本名>/images/<文件名>`；编辑器里「上传」的图调 `POST /api/scripts/{id}/media`，
   落到 `data/media/<剧本 id>/`，写进 `imageUrl` 的是 `/media/<剧本 id>/<文件名>`。两者都是站内相对路径，
   换域名或端口都不用改剧本；外部图床的绝对 URL 也可以直接填。单张上限 8 MB，只收 png/jpg/jpeg/webp/gif，
   服务端按扩展名发 `Content-Type` 并加 `nosniff`，不会把改名成 `.png` 的 HTML 当图片发出去。
6. **发布**：「校验并发布」跑完整规则，失败返回 422 + 逐字段 `errors`，草稿原样保留。

正文支持的 Markdown 子集（`web/src/lib/richtext.ts`，零依赖；未识别的标记原样显示，不会被吞掉）：

| 写法 | 效果 |
|---|---|
| `## 标题` / `### 小标题` | 段标题 / 小标题 |
| `**粗体**`、`*斜体*`、`` `代码` `` | 行内强调 |
| `[文字](https://…)` | 链接（新窗口打开） |
| `- 条目` / `1. 条目` | 无序 / 有序列表 |
| `> 引用` | 引用块 |
| ` ```lang ` … ` ``` ` | 代码块 |
| `---` | 分隔线 |
| `![说明](图片地址)` | 独占一行的配图（地址可以是 `/script-assets/…`、`/media/…` 或外链） |
| `**21:30** 做了什么` | 时间线条目（角色本与复盘里按行排版） |

阶段策略（都是剧本字段，对局里只读）：

- `order`：`round_robin` 按入座顺序（主持人优先）、`random` 阶段开始时随机排一次、
  `host_assigned` 阶段开始时挂起，等主持人在对局页一次性排好整轮顺序；
- `searchMode`：`pick` 看标题自选、`random` 只给「随机抽取」按钮，抽到什么算什么（线索内容仍只有抽取者可见）。

两个策略对搜证与讨论阶段都生效；一次行动一轮，配额用尽才轮到下一位。

管理台（导航栏「管理台」）：

- 账号：列表（含角色、剧本数、注册时间、最近登录），可为任意账号重置为一次性临时密码。
  密码是 bcrypt 单向哈希，**看不到原密码**，这是唯一可行的「找回」方式；
- 房间：列表（剧本、房主、状态、人数），可「结束」（保留复盘）或「删除」（连记录一起删）。

## 导入剧本规范

一份剧本 = 一个 JSON 对象，schema 的权威定义在 [`docs/script.schema.json`](docs/script.schema.json)，
校验规则在 `server/internal/domain/validate.go`。服务端只做三件事：存草稿、校验、存已发布版本——
它**不解压、不 OCR、不调用 LLM**，docx / pdf / 照片必须先由人转成下面的结构。

### 1. 三条录入路径，一套校验

| 路径 | 入口 | 适用 |
|---|---|---|
| 可视化编辑 | 管理台「进入剧本工作台」→「新建空白草稿」→ 六个分区逐字段填 | 手写新剧本、边校对边改、需要上传配图 |
| 粘贴 JSON | 工作台「粘贴 JSON」→「导入为草稿」 | 已有转写好的 `scripts/<剧本名>/script.json` |
| HTTP | `POST /api/scripts`，请求体即剧本 JSON | 脚本化批量导入、CI 造数据 |

三者都落进同一种记录，导入后可以在「可视化」与「JSON」两个模式之间来回切换，不会丢字段。

转写完成的文件先在本机跑一遍引擎校验，比在工作台里等 422 快：

```bash
make check-scripts                                  # scripts/*/script.json 全部校验
cd server && go run ./cmd/scriptcheck ../scripts/K11-再见苏西/script.json
```

### 2. 顶层结构与体积

```json
{
  "schemaVersion": 1,
  "meta": { "title": "...", "playerCount": { "min": 4, "max": 6 } },
  "truth": { "killerRoleId": "r2" },
  "phases": [],
  "roles": [],
  "clues": [],
  "facts": []
}
```

| 约束 | 值 |
|---|---|
| `schemaVersion` | 必须是 `1`；缺省按 1 处理，其他值报错 |
| 请求体 | ≤ **8 MB**（`POST /api/scripts` 与 `PUT /api/scripts/{id}` 均如此） |
| 图片 | 单张 ≤ **8 MB**，只接受 `.png` `.jpg` `.jpeg` `.webp` `.gif` |
| 未知字段 | 报错：schema 是 `additionalProperties: false`，拼错的字段名不会被静默丢弃 |
| 必填顶层键 | `schemaVersion` `meta` `truth` `phases` `roles` `clues`（`facts` 可省） |

### 3. 字段与默认值

保存前 `normalize` 会补齐下列值，所以「最小可用剧本」比 schema 看起来宽松：

| 位置 | 缺省时自动填 |
|---|---|
| `roles[].id` | `r1`、`r2`…（按数组顺序） |
| `roles[].personaId` | `guarded`（性格预设，留空即用这个） |
| `roles[].private.script` | 空白时退化为 `public.intro` |
| `roles[].private.secrets[].id` | `<角色 id>_s<序号>` |
| `roles[].private.secrets[].revealCost` | `0.7`（0~1，越大越难撬开） |
| `clues[].id` | `c1`、`c2`… |
| `phases[].id` / `name` | `p1`… / 用 `type` 当名字 |
| `phases` 整个为空 | 生成六段：阅读 → 讨论 → 搜证 → 公聊 → 投票 → 复盘；搜证池 = 全部线索 |
| `phases[].searchMode` / `order`（search 段） | `pick` / `round_robin` |
| `phases[].order`（discussion 段） | `round_robin` |
| `meta.playerCount.min` / `max` | `2` / 角色数 |

**字段清单**（`—` 表示可省略）：

| 分组 | 字段 |
|---|---|
| `meta` | `title`、`author`—、`summary`—、`difficulty`—（`easy`/`medium`/`hard`/空）、`tags`—、`playerCount{min,max}`、`durationMin`— |
| `phases[]` | `id`、`type`（`reading`/`opening`/`search`/`discussion`/`vote`/`reveal`）、`name`、`round`—、`dmScript`—（主持人手册，Markdown）、`cluePool`—（线索 id 数组）、`perPlayerPick`—（搜证段必填）、`speechPerPlayer`—（讨论段必填）、`searchMode`—、`revealOnFind`—（搜证段：搜到即公开）、`order`—、`timeoutSec`— |
| `roles[]` | `id`、`name`、`personaId`—、`public{intro,appearance}`—、`private{script,secrets[],goals[],knownFactIds[]}` |
| `clues[]` | `id`、`title`、`content`（Markdown）、`location`—、`back`—（卡背，搜证选项只露这一行）、`ownerRoleId`—、`subjectRoleId`—、`mustReveal`—、`isPublic`—、`grantsItem`—、`requiresItems`—、`unlockedBy`—、`keywords`—、`imageUrl`—、`links{roles,secrets,facts}`— |
| `facts[]` | `id`、`text` |
| `truth` | `killerRoleId`、`killerRoleIds[]`—（多凶手并集）、`killerKind`—（`seat`/`npc`/`multiple`）、`killerName`—（NPC 真凶姓名）、`cases[]`—（多案剧本逐案真相：`id`/`name`/`phaseId`—/`killerRoleId`—/`killerRoleIds[]`—/`killerKind`—/`killerName`—/`method`—/`motive`—）、`method`—、`motive`—、`timeline[]`—、`summary`—（复盘全文，Markdown） |

`public.intro` 是开局所有人可见的一句话，`private.script` 是该角色的完整角色本（Markdown），
`truth` 永远不进任何角色的 prompt，只在主持人与复盘里出现。

### 4. 校验规则（发布必须全部通过）

失败时返回 `422 {errors: [{path, message}]}`，`path` 精确到字段，例如 `roles[1].private.script`。
草稿可以先带着这些错误存在（作为 `warnings` 显示）；**已发布剧本保存时只要有一条不过就整体拒绝**，
库里仍是旧版本。

| 规则 | `path` |
|---|---|
| 只支持 `schemaVersion` 1 | `schemaVersion` |
| 标题非空 | `meta.title` |
| 最少人数 > 0，最多人数 ≥ 最少人数 | `meta.playerCount.min` / `.max` |
| 最多人数 ≤ 角色数 | `meta.playerCount.max` |
| 至少一个角色 | `roles` |
| 角色 id 非空且唯一、角色名非空 | `roles[i].id` / `.name` |
| 每个角色的私密剧本非空 | `roles[i].private.script` |
| 秘密 id 非空、`revealCost` 在 0~1 | `roles[i].private.secrets[j].*` |
| `knownFactIds` 里的 id 必须存在于 `facts` | `roles[i].private.knownFactIds` |
| 线索 id 非空且唯一 | `clues[i].id` |
| `ownerRoleId` 与 `links.roles` 必须是已存在的角色 id | `clues[i].ownerRoleId` / `.links.roles` |
| `subjectRoleId` 必须是已存在的角色 id，且不能等于 `ownerRoleId` | `clues[i].subjectRoleId` |
| `unlockedBy` 里的 id 必须存在，且不能指向自己 | `clues[i].unlockedBy` |
| `requiresItems` 不能有空串 | `clues[i].requiresItems` |
| 写了 `keywords` 就必须有 `ownerRoleId`（关键词只用来开私人物品／记忆碎片） | `clues[i].keywords` |
| 事实 id 非空 | `facts[i].id` |
| 凶手角色必须存在（可留空，但复盘会说不出凶手）；NPC 真凶填 `killerKind=npc` + `killerName` | `truth.killerRoleId` / `truth.killerKind` / `truth.killerName` |
| 至少一个阶段，阶段 id 非空、类型合法 | `phases` / `phases[i].id` / `.type` |
| **第一个阶段必须是 `reading`** | `phases[0].type` |
| **必须有一个 `reveal` 阶段** | `phases` |
| 搜证段 `perPlayerPick` > 0，`cluePool` 里的 id 必须存在，`searchMode` 合法 | `phases[i].perPlayerPick` / `.cluePool` / `.searchMode` |
| 讨论段 `speechPerPlayer` > 0 | `phases[i].speechPerPlayer` |
| `order` 合法（`round_robin`/`random`/`host_assigned`） | `phases[i].order` |

### 5. 阶段流程的硬约束

1. **首段 `reading`、末段 `reveal`**：阅读给角色本，复盘放真相。中间顺序自由（可以有多个搜证/讨论轮）；
2. **搜证段的 `cluePool` 就是全部可搜范围**：引擎只在当前搜证阶段的池子里发牌，
   **池子留空 = 这一轮谁都搜不到任何东西**；不在任何池子里、也没标 `isPublic` / `ownerRoleId` 的线索
   永远不会出现在对局里；
3. `ownerRoleId` 是私人物品：只有该角色初始可见，且只能由该角色搜到；`isPublic: true` 开局即公共；
4. `searchMode`：`pick` 玩家看标题自选，`random` 只给「随机抽取」（抽到什么算什么，内容仍只有抽取者可见）；
5. `order`：`round_robin` 按入座顺序（主持人优先）、`random` 阶段开始时随机排一次、
   `host_assigned` 进入阶段时挂起，等主持人一次性排好整轮；
6. 一次行动一轮，配额用尽才轮到下一位；`timeoutSec` 为 0 表示用服务端默认（`SP_FLOOR_TIMEOUT_SEC`）；
7. **搜证段的线索取不到就不发**：`subjectRoleId`（本人）、缺 `requiresItems`、`unlockedBy` 里的线索
   还没露面，这三种情况该卡根本不出现在搜证列表里；`random` 抽到也会自动跳开。
   一个座位如果剩下的全是它取不了的卡，引擎把这轮配额判完并写一条 `system` 说明，不会把人卡在那里点空按钮；
8. `revealOnFind: true` 的搜证段，搜到立刻按公开处理（发 `clue.revealed` 公开事件、进 `publicClues`），
   不再出现「公开」按钮；不设则仍是「搜到归自己，要念再点公开」；
   卡面写「本条线索必须公开」的，把单张卡的 `mustReveal` 置 `true`，不管它在哪一轮都当场公开；
9. `grantsItem` 搜到即进该座位的物品栏（`visible.items`），物品按座位隔离，不共享；
10. `keywords` 是记忆碎片的开关：只有当众发言的正文或**已公开**线索的正文里出现这些词时，
    碎片才对它的 `ownerRoleId` 可见（私有搜证不触发）；在此之前连持有者自己也看不到。

### 5.1 常见规则用什么字段表达

| 剧本里常写的规则 | 写法 | 引擎行为 |
|---|---|---|
| 这一轮搜到的线索全场公开 | `search` 段 `revealOnFind: true` | 搜到即公开，自动写公开事件 |
| 这**张**卡必须公开（其余只可口述） | 卡上 `mustReveal: true` | 单张卡搜到即公开，不受阶段设置影响 |
| 除卡面注明公开的以外只可口述 | 默认（不设） | 搜到只进自己的线索，可随时点「公开」 |
| 抽到自己的搜身线索放回重抽 | `subjectRoleId` | 该角色看不到也抽不到，卡仍在池里给别人 |
| 这张卡只能某人调查（如「许迎可调查 01」） | `ownerRoleId` | 池里只有该角色可见可取 |
| 卡面「如有物品 XX」才能查 | `requiresItems` | 缺物品时不出现，强行点会被拦并说明缺什么 |
| 先搜到 A，B 才会浮出来 | `unlockedBy: ["A"]` | A 未找到也未被公开时，B 不出现 |
| 卡面「获得物品 XX」 | `grantsItem` | 搜到即入该座位物品栏 |
| 记忆碎片按关键词触发 | `ownerRoleId` + `keywords` | 关键词出现在当众发言或公开线索里，碎片才对持有者解锁 |
| 两两私聊、单独交换情报 | `type: discussion` 段，玩家自己发 `whisper.send` | 只有双方看得到，不占麦也不进前情提要；AI 被私聊会私下回一句 |
| 必须当众念出、不能把卡递给别人 | 写进 `dmScript` | 社交规则，引擎只负责记录（不阻止转述） |
| 要三个人同意才能开挖 | `requiresItems` + `dmScript` | 物品由引擎管，人数协商写在手册里 |
| 指认凶手、按被指认者分结局 | `type: vote` + `truth` + `reveal` 段 `dmScript` | 引擎计票并锁定得票者，分支写进复盘文本；真凶不在座（`killerKind: npc`）时玩家可填写姓名投票，记票为「某某（不在座）」 |
| 固定人数、每人一角色 | `meta.playerCount` + `roles[]` | 房间座位数受剧本限制，AI 补位 |

能力边界：引擎只管「谁能看到、谁能取到、取了之后多出什么」。
**不**建模的：私人聊天室（`whisper` 是一对一耳语，开不了五六个人的密室）、限时倒计时播报（只有 `timeoutSec`）、
手牌交换、给他人递物品。随机分配角色由房间完成（开局 `rand.Shuffle` 后发牌），不需要剧本字段。
私聊只在讨论段开放，且**不**触发记忆碎片：不公开说出来就不算被听见，避免有人用私聊当解锁器。

### 6. 正文与配图

- 角色本、线索正文、真相复盘、主持人手册都按 Markdown 子集渲染（见上一节的表）；解析器零依赖，
  未识别的标记原样显示，不会把半行标记当成格式吞掉；
- `imageUrl` 写站内相对路径最稳：随剧本固化的图写 `/script-assets/<剧本名>/images/<文件名>`，
  后台上传的图写 `/media/<剧本 id>/<文件名>`；换域名/端口不用改剧本，外链绝对 URL 也可以直接填；
- 固化图由 `GET /script-assets/<剧本名>/<路径>` 直接从 `scripts/` 读（只放行图片扩展名，`script.json`
  拿不到，单文件上限不设，靠 git 管版本；`Cache-Control: max-age=3600`，换图一小时内生效）；
- 上传图由 `POST /api/scripts/{id}/media`（`multipart/form-data`，字段 `file`）写入，
  落到 `data/media/<剧本 id>/`，响应 `{url, name, size, mime}`；
  `GET /media/{id}/{name}` 负责回放，按扩展名决定 `Content-Type` 并加 `nosniff` ；
- 图片不是必填：线索可以只有文字。

### 7. 版本与发布语义

| 动作 | 效果 |
|---|---|
| 保存草稿 | 允许带校验错误，错误变成 `warnings`，版本号不变 |
| 发布草稿 | 校验通过才置 `published`，版本号 +1；失败 422，状态不变 |
| 保存已发布 | 必须先通过校验，成功即 `published` + 版本号 +1（一次修订 = 一个新版本） |
| 已发布重复发布 | 幂等：只再校验一遍，版本号不动 |
| 另存为副本 | 复制出一份 `draft`，版本号沿用源版本，发布后接着 +1 |
| 进行中的对局 | 用开局时那一版的深拷贝，管理员改剧本不影响正在跑的房间 |
| 新开房间 | 绑定最新已发布版本 |

### 8. 发布前自检清单

- [ ] 第一个阶段是 `reading`，最后有一个 `reveal`；
- [ ] 每个搜证阶段的 `cluePool` 都**非空**，且已把该轮想被搜到的线索全部列进去；
- [ ] 每条线索都有去处：在某个 `cluePool` 里，或标了 `isPublic` / `ownerRoleId`；
- [ ] 每个角色的 `private.script` 都写全了（空的本子会被校验拦下）；
- [ ] 写了 `subjectRoleId` 的搜身卡，没有同时写 `ownerRoleId`，且确认它确实在第二个搜证池里；
- [ ] 每张 `requiresItems` / `unlockedBy` 的深入调查卡，对应的物品卡与前置卡都在本轮之前的池子里；
- [ ] 关键词碎片逐个试过：把关键词打在发言里能看到解锁；碎片本身不要放进搜证池（会被自己的正文触发）；
- [ ] `truth.killerRoleId` 指向真实存在的角色，且和线索、秘密对得上；
- [ ] 人数区间不超过角色数；
- [ ] 角色本里的 Markdown 在「预览」里看过一遍：标题、时间线、列表没有错位；
- [ ] 配图能打开（`/script-assets/…` 或 `/media/…` 或外链），且不包含不想提前泄露的信息。

## 部署（nginx + systemd）

对外只有 nginx 的 9091；Go 只监听回环，前端静态由 nginx 直接发。

```
浏览器 ──9091──> nginx ─┬─ /                        → /usr/share/nginx/script-play（Vite 产物）
                        ├─ /assets/                 → 同上，immutable 长缓存
                        ├─ /api/  /ws/              → 127.0.0.1:9092（Go）
                        └─ /media/ /script-assets/  → 127.0.0.1:9092（Go）
```

首次安装（配置都随仓库放 `deploy/`）：

```bash
make web-publish                                        # 构建前端 → /usr/share/nginx/script-play
cp deploy/nginx/script-play.conf /etc/nginx/conf.d/     # 与其它站点共存，不动现有 server 块
cp deploy/script-play.service /etc/systemd/system/
make server && systemctl daemon-reload && systemctl enable --now script-play
nginx -t && systemctl reload nginx
```

日常发版按改动范围选一条：

```bash
make deploy-backend   # 改了后端：go build → systemctl restart → 打印 exe/构建时间/启动时间/cgroup 取证
make deploy-web       # 改了前端：npm build → 原子发布到 /usr/share/nginx/script-play
make deploy           # 前后端都改：先后端再前端
```

`deploy-backend` 会校验新进程落在 `/system.slice/script-play.service`，不在就报错——那是手工 `nohup`
实例抢了端口、systemd 单元在空转重启的信号。只 `systemctl restart` 不重新 `go build` 等于没部署，
这一步已由 `make deploy-backend` 内置。

几条不能改错的：

- **站点根不能放 `/root` 下**：nginx worker 以 `nginx` 用户运行，进不去权限 750 的 `~`；
- `/api/` 的 `proxy_read_timeout` 必须大于 `SP_AGENT_TIMEOUT_SEC`（默认 180s），否则长生成中途被切断；
- `/ws/` 必须显式透传 `Upgrade` / `Connection`，否则握手永远 400；
- `/assets/*` 缺失要 404，不能回落 `index.html`，否则浏览器把 HTML 当 JS 执行、报 MIME 错白屏；
- `client_max_body_size` 要 ≥ 2 × `SP_MAX_UPLOAD_MB`（默认 256，即 `512m`）：单文件有一个上限，
  一次请求（一个游戏文件夹里好几个 PDF）有另一个，比后端小的话请求根本到不了 Go，
  浏览器只能收到 nginx 自己的 HTML 413。

本地开发仍是单进程：`make run`（Go 8080，带 `SP_WEB_DIR`）配 `make web-dev`（5173，代理 `/api`、
`/ws`、`/media`、`/script-assets`）。`SP_WEB_DIR` 只服务于这个场景，生产由 nginx 发前端。

## LLM Provider

三种，座位级可选（同一局不同角色可以跑不同模型）：

| Provider | 启用方式 | 说明 |
|---|---|---|
| `mock` | 默认 | 确定性假 provider，CI 与离线开发用 |
| `qodercli` | `SP_LLM_PROVIDER=qodercli` | 复用本机 OAuth 登录态，headless 一次性子进程 |
| `openai` | `SP_LLM_PROVIDER=openai` + `SP_OPENAI_BASE_URL` / `SP_OPENAI_API_KEY` / `SP_OPENAI_MODEL` | 任何 OpenAI 兼容端点（DeepSeek / Qwen / Ollama / one-api） |

`qodercli` 调用形态：`qodercli -p -o stream-json --tools= --no-session-persistence -m <model>`，
prompt 走 stdin。`--tools=` 必须显式关闭，否则子进程会在工作目录里跑文件/Shell 循环，
信息隔离和对局确定性都会失效。对局与剧本编辑都不需要模型读文件。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `SP_ADDR` | `:8080` | 监听地址 |
| `SP_DATA_DIR` | `./data` | 数据目录（`store/` 分域 JSON + `inbox/` 原件 + `media/` 上传图 + JWT secret） |
| `SP_WEB_DIR` | 空 | 设置后由 Go 自己托管前端静态文件（仅本地开发；生产由 nginx 发） |
| `SP_SCRIPTS_DIR` | `../scripts` | 随仓库固化的剧本目录；`GET /script-assets/…` 从这个根读图，相对工作目录 |
| `SP_JWT_SECRET` | 自动生成 | 不设则落盘 `data/secret.key` |
| `SP_ADMIN_USERNAME` / `SP_ADMIN_PASSWORD` | 空 | 启动时创建或提权管理员账号；已存在且未给密码时只提权。不设则没有管理员 |
| `SP_ADMIN_DISPLAY_NAME` | 空 | 管理员昵称；空则用用户名。设了会顺带改已有账号的昵称 |
| `SP_ACCESS_TTL_MIN` | `15` | access token 有效期（分钟）；过期后前端用 refresh token 静默续期 |
| `SP_REFRESH_TTL_HOUR` | `168` | refresh token 有效期（小时），只能用于 `POST /api/auth/refresh` |
| `SP_LLM_PROVIDER` | `qodercli` | `mock` / `qodercli` / `openai` |
| `SP_LLM_MAX_CONCURRENT` | `3` | 全局 LLM 并发上限（2 核机器的保护阀） |
| `SP_QODER_BIN` / `SP_QODER_MODEL` / `SP_QODER_TIMEOUT_SEC` | `qodercli` / `Auto` / `180` | qoder CLI 位置、模型、单次超时 |
| `SP_OPENAI_BASE_URL` / `SP_OPENAI_API_KEY` / `SP_OPENAI_MODEL` / `SP_OPENAI_VISION_MODEL` | 空 | OpenAI 兼容端点 |
| `SP_FLOOR_TIMEOUT_SEC` | `90` | 真人**每次轮到**发言/搜证/投票的默认等待上限（轮与轮各自重新计时）；建房时用 `speechSeconds` 按房间覆盖，`-1` 表示不限时 |
| `SP_AGENT_TIMEOUT_SEC` | `90` | 单次 Agent 生成上限 |
| `SP_MAX_ROOMS` | `2` | 同时进行的对局数 |
| `SP_MAX_UPLOAD_MB` | `256` | 单个原件上传上限（MB）；一次请求最多 2 倍，`client_max_body_size` 需同步放大 |

## 数据目录布局

存储是分域的 JSON 文件，不再是单个 `state.json` 快照：

```
data/store/
  users.json                 账号全表（几十 KB，登录时也只重写这一份）
  rooms.json                 房间全表
  materials.json             原件记录全表
  scripts/<scriptID>.json    一本剧本一个文件
  games/<gameID>.json        对局主体，events 字段已被抽走
  games/<gameID>.events.jsonl  对局事件日志，一行一个 Event，只追加
```

写入由 dirty 集合驱动：一局打出一条事件只追加一行 JSONL、重写该局十几 KB 的主体，
其它剧本和别的对局都不动。事件与主体的落盘顺序是「先追加日志、后写主体」，主体里的
`eventsInLog` 记录有效行数；进程在两者之间崩掉时，多出的行在加载时被忽略并压缩掉。

旧版本的 `data/state.json` 会在启动时自动迁移：拆成上述文件后，原文件改名为
`data/state.json.pre-split-<时间戳>` 保留，确认无误即可删除。备份只需 `cp -a data/store`。

## 信息隔离怎么保证

1. `engine.VisibleFor` 是唯一裁口：任何下发给客户端或 prompt 的数据都只吃它的返回值；
2. `engine.PromptBuilder` 的入参是 `Visible`，签名层面拿不到 `Game`/`Script`；
3. `engine.LeakGuard` 对生成文本做真相/他人秘密/提示词回显检查，命中先重写一次，
   再命中则模糊化并把 `leak_incident` 事件只发给主持人；
4. WebSocket 下行同样按座位过滤，不做「前端隐藏」。

`go test ./internal/engine` 里每个座位都有 golden 断言：可见字段集合多一个就失败。
`go test ./internal/httpapi -run TestFullGame` 会用 REST + WebSocket 真实打完一 局（5 个阶段）。

## 对局约定

- **角色随机抽取**：开局时，未指定角色的座位从剩余角色里随机发牌（不再按入座顺序），房主不会每次都拿到剧本第一个角色。
- **主持人推进**：对局页右侧的「进入下一阶段：X」会先在发言记录里写一条主持人的条目，再切换阶段；阶段分隔线只显示阶段名。
- **线索默认不公开**：搜到的线索只有本人可见，公开靠当事人用嘴说出来，或在搜证页点「公开」；剧本里标了 `isPublic` 的线索开局即公共（即「剧本要求」的情况）。AI 搜证不会再自动公开线索。
- **台词不含动作描写**：提示词要求只输出台词，服务端 `cleanSpeech` 会再裁一遍短括号旁白（如「（拍桌子）」），所以时间线与朗读里不会出现舞台提示。

## 目录

```
server/cmd/server            装配 + 优雅退出
server/internal/engine       状态机、可见性、prompt 组装、泄密检查、发言调度、单写者 Actor
server/internal/llm          provider 接口 + mock / qodercli / openai
server/internal/importer     标准 JSON → 草稿 → 校对 → 发布；兼管 data/inbox 收件目录
server/internal/httpapi      REST + WebSocket + 管理台
web/src/features             auth / lobby / room / game / studio / admin
web/src/realtime             座位票据重连的 WS 客户端
web/src/voice.ts             speechSynthesis 朗读（普通话音色、角色分声）；语音输入交给系统输入法
web/src/lib/theme.ts         主题注册表（暗夜 / 白昼 / 宣纸 / 松林）+ localStorage 记忆
```

## 界面主题

四套主题可在页头右上角的「主题」下拉里随时切换，选择存在 `localStorage`（`script-play.theme`），
首次访问跟随系统深浅色：

| 主题 | 底色 | 说明 |
|---|---|---|
| 暗夜 `night` | 深蓝 | 默认，现在的深色面板 |
| 白昼 `light` | 纯白 | 白底深墨字，适合投屏 / 白天房间 |
| 宣纸 `paper` | 暖白 | 纸感赭石点缀 |
| 松林 `forest` | 墨绿 | 夜间不刺眼的深色 |

实现上只有一份 `src/index.css` 的变量表：每套主题是一块 `[data-theme="..."]` 的 RGB 变量，
`tailwind.config.js` 把 `page/ink/panel/line/accent/warm/strong` 以及 `slate/rose/emerald/amber`
映射到这些变量，所以切换主题只要改 `<html data-theme>`，组件里不需要任何 `dark:` 分支。

## 测试

```bash
make test        # go vet + go test（后端）
make web-check   # tsc --noEmit + vitest
```

真实模型的一局（需要本机已登录 qodercli）：

```bash
cd server && SP_E2E_PROVIDER=qodercli go test ./internal/httpapi -run TestFullGame -v -timeout 20m
```

## 与最初架构方案的差异

- **语音**：方案写的是 sherpa-onnx WASM（离线流式 ASR + VITS TTS）。实际只保留输出侧：
  TTS 用浏览器自带的 `speechSynthesis`，零下载、零依赖，只使用普通话（`zh-CN`/`zh-Hans`）音色，
  粤语（`zh-HK`/`yue`）与台湾腔（`zh-TW`）会被跳过；每个角色按 `roleId` 稳定分配一个普通话音色并
  叠加语速/音高，同时朗读主持人台词、线索公开、投票结果与复盘：一局多人不会听起 来像同一个人。
  手机上的 Chromium（含 Edge）常常不列举任何音色，此时改用 `lang=zh-CN` 交给系统默认引擎朗读，
  并在页面上标「系统音色」；只有「列出了音色但都不是普通话」才真正静默。
  **语音输入已移除**：`getUserMedia` 与 `SpeechRecognition` 都要求 secure context，本服务以
  `http://<IP>:9091` 部署时麦克风对象根本不存在，按钮只能置灰；对局页改为提示玩家用系统输入法的
  语音听写（手机输入法麦克风键 / Windows Win+H / macOS 连按 Fn），结果直接落进输入框可编辑。
  sherpa 与服务端 ASR 都需要先有 TLS 或原生壳才划算，留在路线图里。
- **存储**：方案写的是 SQLite。首版实现为单进程内的 JSON 快照存储（原子 rename + 防抖落盘），
  零 cgo、零迁移；接口集中在 `internal/store`，替换成 SQLite 不影响其它包。
- **`qoder-acp`**：未实现，当前每个回合起一个 headless 子进程（冷启动 2~5s）。
