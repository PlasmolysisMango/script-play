# script-play 协议（REST + WebSocket）

所有接口默认 `Content-Type: application/json`。除 `/api/health`、`/api/auth/*` 外都需要
`Authorization: Bearer <token>`；浏览器 WebSocket 无法设置请求头，因此 `GET /ws/rooms/{code}` 允许
`?token=` 传 JWT。

错误响应统一为：

```json
{ "error": "人类可读的原因" }
```

## 1. 认证

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/register` | `{username, password, displayName?}` → `{token, refreshToken, expiresAt, user}` |
| POST | `/api/auth/login` | `{username, password}` → `{token, refreshToken, expiresAt, user}` |
| POST | `/api/auth/refresh` | `{refreshToken}` → 新的 `{token, refreshToken, expiresAt, user}` |
| GET | `/api/me` | 当前用户 `{user}`；`user` 含 `role: user|admin` |

密码用 bcrypt 存储（单向哈希，任何接口都不返回明文），token 为 HS256 JWT（服务端 `SP_JWT_SECRET` 或 `data/secret.key`）。
忘记密码只能重置，不能「查看原密码」。

会话是「短 access + 长 refresh」两枚 token，靠 `purpose` 声明互斥，不能混用：

| 种类 | `purpose` | 默认有效期 | 用途 |
|---|---|---|---|
| access | `access` | 15 分钟（`SP_ACCESS_TTL_MIN`） | 所有 REST / WS 鉴权 |
| refresh | `refresh` | 7 天（`SP_REFRESH_TTL_HOUR`） | 只能调 `/api/auth/refresh` |
| 座位票据 | `seat-ticket` | 1 小时 | 仅 `GET /ws/rooms/{code}?ticket=` |

access token 过期时前端自动用 refresh token 换一对新 token 并重放原请求；
refresh 被拒（过期/已失效/类型不对）才提示重新登录。签发早于本约定的旧 token
不带 `purpose`，服务端仍按 access 接受。

### 管理员

角色存在用户记录上（`role: user | admin`），**不写进 token**：每个请求都从库里读一次角色，
因此降权立即生效。管理员账号由环境变量引导：

```bash
SP_ADMIN_USERNAME=admin SP_ADMIN_PASSWORD=change-me   # 启动时创建或提权该账号
```

`SP_ADMIN_PASSWORD` 只在账号不存在时需要；账号已存在且未给密码时只提权、不改密码。
管理员额外拥有：剧本库全量读写、原件归档入口、`/api/admin/*` 管理台。

## 2. 元信息

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | `{ok, status, providers, personas, floorTimeoutSec, agentTimeoutSec}`；`providers` 是当前可用 LLM provider，`floorTimeoutSec` 是未经房间覆盖的默认发言时限（秒），`agentTimeoutSec` 是 AI 生成超时 |
| GET | `/api/personas` | `{personas: Persona[]}`（6 套预设性格） |
| GET | `/api/providers` | `{providers, default, models}` |

`Persona` 字段：`id, name, style, candor, aggression, suspicion, talkativeness, deceitfulness, emotionality, rules[]`，
数值均为 0~1。

## 3. 剧本

写操作（`POST/PUT/DELETE`）**仅管理员**可用，其他账号返回 403；
读取对所有登录用户开放，但普通用户只能拿到自己名下的草稿列表。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/scripts` | `{scripts: ScriptSummary[]}`：管理员拿到全量，其他人拿到自己名下的 |
| POST | `/api/scripts` | 管理员：请求体为标准剧本 JSON，直接建草稿 |
| GET | `/api/scripts/{id}` | 作者得到完整剧本；其他人得到**脱敏**剧本（无 truth、无 private、无线索） |
| PUT | `/api/scripts/{id}` | 仅作者或管理员。草稿可带错误保存（错误变 `warnings`）；已发布必须先过校验，否则 422 且不落库。返回 `{script, errors}`；已发布保存成功即版本号 +1 |
| POST | `/api/scripts/{id}/draft` | 把已发布版本复制成可编辑草稿（版本号沿用，发布后 +1） |
| POST | `/api/scripts/{id}/publish` | 校验通过后发布；失败返回 422 + `{errors: ValidationError[]}`；重复发布幂等 |
| DELETE | `/api/scripts/{id}` | 仅作者或管理员 |
| POST | `/api/scripts/{id}/media` | 管理员：`multipart/form-data`，字段 `file`（也接受 `image`/`files`）；png/jpg/jpeg/webp/gif，单张 ≤ 8 MB；返回 `201 {url, name, size, mime}` |
| GET | `/media/{id}/{name}` | 图片回放（免登录）；`Content-Type` 只由扩展名决定并带 `nosniff`，缺失或不在白名单返回 404 |
| GET | `/script-assets/{剧本目录}/{路径}` | 随剧本固化的图（`scripts/<剧本目录>/images/…`，免登录）；只放行图片扩展名，`script.json` 拿不到，`Cache-Control: max-age=3600` |

### 剧本从哪来

服务端**不解析**任何原始剧本。它只做三件事：归档原件、存草稿、校验发布。

1. **归档原件**：管理员把压缩包、docx、pdf、照片上传到 `POST /api/admin/materials`，
   文件原样存进服务端 `data/inbox/<时间戳>-raw_<随机>.<ext>`，`data/store/materials.json` 只多一条
   `RawMaterial`。服务端不解压、不 OCR、不摘要 —— 这一步没有 LLM 参与。
2. **转成标准 JSON**：开发者把原件转成 `docs/script.schema.json` 描述的结构，然后三选一：
   工作台「新建空白草稿」拿骨架后在可视化模式逐字段填、粘贴完整 JSON，或直接调接口：

```http
POST /api/scripts
Content-Type: application/json

{ "schemaVersion": 1, "meta": { "title": "...", "playerCount": {"min":4,"max":6} }, "roles": [...], ... }
```

   返回 `201 {script, errors}`；`errors` 是校验结果，草稿可以先带着错误存在。
3. **校对与发布**：`PUT /api/scripts/{id}` 保存，`POST /api/scripts/{id}/publish` 校验通过后发布；
   已发布剧本保存即产生下一个版本（进行中的对局仍绑开局那一版），想另开分支用
   `POST /api/scripts/{id}/draft`。角色本与线索里的配图两种来源：随剧本固化的放
   `scripts/<剧本目录>/images/`，正文写 `/script-assets/<剧本目录>/images/<文件名>`；后台上传的走
   `POST /api/scripts/{id}/media`，把返回的 `/media/<剧本 id>/<文件名>` 填进 `imageUrl`。

`RawMaterial` = `{id, name, path, mime?, size?, uploadedBy?, createdAt}`，`path` 是服务端绝对路径，
也是开发者去取文件的位置。

`ValidationError` = `{path, message}`，`path` 精确到字段，例如 `roles[1].private.script`。

## 4. 管理员

全部需要 `role = admin`，否则返回 403。密码以 bcrypt 存储，所以没有「查看密码」接口：
需要帮用户找回时只能重置为一次性临时密码。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/admin/users` | `{users: [{id, username, displayName, role, scripts, createdAt, lastLoginAt?, me}]}`，`scripts` 为该账号名下剧本数 |
| POST | `/api/admin/users/{id}/password` | `{password?}`；省略则生成 10 位随机密码，返回 `{ok, password, generated}`。**密码只在这里出现一次** |
| GET | `/api/admin/rooms` | `{rooms: [{id, code, name, status, scriptTitle, gameId, hostName, seats, players, running, createdAt}]}` |
| POST | `/api/admin/rooms/{code}/close` | 结束房间：停掉对局并落库为 `finished`，复盘仍可看 |
| DELETE | `/api/admin/rooms/{code}` | 删除房间：先结束再删房间与对局记录，不可恢复 |
| GET | `/api/admin/materials` | `{materials: RawMaterial[], dir, maxUploadMB}`；`dir` 是收件目录绝对路径，`maxUploadMB` 供前端预检 |
| POST | `/api/admin/materials` | `multipart/form-data`，字段名 `files`（可重复），返回 `201 {materials, dir, maxUploadMB}`；单文件上限 `SP_MAX_UPLOAD_MB`（默认 256），整请求上限为其 2 倍，超限返回 `413 {error}` 且不留下半成品 |
| DELETE | `/api/admin/materials/{id}` | 删记录并删除收件目录里的对应文件；越出 `dir` 的路径不会被删 |

`close` 与 `DELETE` 的区别：前者保留复盘日志并释放「同时进行对局」名额，后者连记录一起删。

原件只归档不解析：`data/inbox/` 里的文件由开发者自行转成标准 JSON，再通过 `POST /api/scripts`
导入。服务重启不需要恢复任何任务状态，因为根本没有后台任务。

## 5. 房间

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/rooms` | 我可见的房间（大厅全量 + 我参与的对局） |
| POST | `/api/rooms` | `{name?, scriptId, maxSeats?, allowSpectator?, speechSeconds?, autoSkip?}` → `{room}`，创建者为房主并自动占座；`maxSeats` 留空取剧本 `playerCount.max`，人数固定（min=max）时必须等于该值，可浮动时只能落在 min~max 内，否则 400；`speechSeconds` 为 10~600（单次轮次的秒数）、0（服务端默认，见 `/api/health` 的 `floorTimeoutSec`）或 -1（不限时）；计时按「每次轮到某个座位」重新开始，搜证与投票的等待也用同一个值；`autoSkip=false` 表示超时不跳过、等主持人推进（`-1` 不产生超时，此项无意义） |
| GET | `/api/rooms/{code}` | `{room}` |
| POST | `/api/rooms/{code}/join` | `{displayName?, roleId?, personaId?, spectator?}`；已有座位则 `rejoined: true`；对局已开始时只能以 `spectator` 身份加入（需 `allowSpectator`） |
| POST | `/api/rooms/{code}/seats` | 房主添加 AI 座位 `{roleId?, personaId?, displayName?}`；`personaId` 缺省或传 `random` 时服务端抽一个预设并落库为具体 id；`modelId` 仍可传（留空用服务端默认），但 Web 界面不再暴露模型选择 |
| PATCH | `/api/rooms/{code}/seats/{seatId}` | 房主可改全部字段；玩家改**自己**座位只能改 `roleId/ready`（选角色 / 准备），改昵称、性格、模型返回 403；`personaId: "random"` 同样会被抽成具体预设 |
| DELETE | `/api/rooms/{code}/seats/{seatId}` | 房主移除座位（不能移除自己；对局开始后返回 409） |
| POST | `/api/rooms/{code}/start` | 房主开局，自动给空座位分配未用角色 |
| GET | `/api/rooms/{code}/seat-ticket` | 用会话换一张短时（1h）座位票据 `{ticket, seatId, gameId, expiresAt, wsPath}`；没有座位返回 403 |
| GET | `/api/rooms/{code}/state?seatId=` | **按座位过滤**的对局状态；不传则用自己座位 |
| GET | `/api/rooms/{code}/replay` | 房主在对局结束后取完整日志 + 真相 |

`room` 结构（`RoomView`）：

```jsonc
{
  "id": "...", "code": "7K3M9Q", "name": "...", "status": "lobby|running|finished",
  "gameId": "...", "scriptId": "...", "scriptTitle": "...",
  "maxSeats": 4, "allowSpectator": true, "host": false, "mySeatId": "seat_...",
  "speechSeconds": 45, "autoSkip": false,
  "seats": [{ "id", "roleId", "roleName", "displayName", "kind": "human|agent|spectator",
              "personaId", "modelId"(仅自己), "isHost", "connected", "ready", "mine" }],
  "roles": [{ "id", "name", "intro", "personaId", "taken" }],
  "personas": [ Persona ]
}
```

座位身份由**连接持有的座位**决定，服务端不做文本侧说话人推断。`room.status` 以对局状态为准：
开局后为 `running`，复盘完成为 `finished`，前端不需要自己合并两个状态。对局结束时服务端同时把
房间记录写成 `finished` 并释放「同时进行对局」名额（`SP_MAX_ROOMS`），所以长跑的服务端不会
因为历史对局而拒绝新局。

## 6. 实时通道

`GET /ws/rooms/{code}?ticket=<seatTicket>`（推荐；也接受 `?token=<jwt>[&seat=<seatId>]`）

票据走 `GET /api/rooms/{code}/seat-ticket`，是绑定「本局 + 本座位」的短时 JWT
（`purpose=seat-ticket`，带 `gid`/`sid`）。服务端强制使用票据里的座位，
浏览器无法改 `seat` 参数冒充别人；票据过期或跨局使用返回 401/403。
断线重连时前端重新换一张票据即可。

一个连接绑定一个座位。所有人收到的事件都先经过同一条可见性管线
（`engine.EventVisibleTo` + `VisibleFor`），不同座位收到的字段不同。
可见性共四种：`public`、`seat:<座位id>`、`pair:<a>,<b>`（两个座位，用于私聊）、`dm`（仅主持人）。

### 下行：单个事件对象（无信封）

每帧就是一个 `Event`（`type` 即事件类型），前端按 `type` 分派：

```jsonc
{ "seq": 12, "gameId": "game_...", "at": "2026-01-01T00:00:00Z", "type": "speech",
  "phaseId": "p3", "seatId": "seat_...", "roleId": "r1", "speaker": "沈曼丽",
  "visibility": "public", "text": "我认为……", "clueId": "", "meta": {} }
```

| `event.type` | 说明 | 可见范围 |
|---|---|---|
| `phase` | 进入新阶段 | public |
| `dm` | 主持人播报（`dmScript`） | public |
| `speech` | 一条完整发言（真人或 AI 定稿） | public |
| `whisper` | 私聊：一对一的一句，`meta.to` 是收话座位 | `pair:<a>,<b>`（双方，不含主持人） |
| `agent.delta` | AI 流式片段（不落库） | public |
| `system` | 轮到你发言 / 搜证超时等提示 | public 或 `seat:<id>` |
| `clue.found` | 搜到线索（内容私有） | `seat:<id>` |
| `clue.revealed` | 公开某条线索；`revealOnFind` 阶段搜到时也走这条（公开） | public |
| `vote` | 某人投票 | public（票面角色名） |
| `vote.result` | 计票结果 | public |
| `reveal` | 复盘真相 | public（仅在 reveal 阶段后） |
| `leak_incident` | 泄密检查和处置记录 | `dm`（仅房主） |

### 上行命令

一条命令一帧：

```jsonc
{ "type": "speak.text", "text": "我认为……" }
{ "type": "search.clue", "clueId": "c1" }   // 自选搜证：按 id 取线索
{ "type": "search.draw" }                      // 随机搜证：服务端在可用池里抽一条
{ "type": "search.pass" }                      // 结束本轮搜证：剩下的次数作废，点数不扣
{ "type": "skill.use" }                          // 发动自己的特殊技能（无需点名的）
{ "type": "skill.use", "targetId": "seat_b" }   // 技能要点名一位道友时带上目标
{ "type": "clue.reveal", "clueId": "c1" }
{ "type": "whisper.send", "to": "seat_b", "text": "钳子是我拿的" }  // 私聊，只在讨论段可用
{ "type": "vote.cast", "roleId": "r3" }
{ "type": "order.set", "seatIds": ["seat_a", "seat_b"] }  // 定序：必须是本阶段可行动角色的一个排列（主持人或 order=role_assigned 指定的角色）
{ "type": "host.skip" }   // 进入下一阶段（帧名保留旧名）：UI 写「进入下一阶段：X」，落盘一条主持人的 dm
{ "type": "ping" }
```

`ping` 与 `host.skip` 立即处理；`host.skip` 推进后先写一条 `dm`（`speaker` 为「主持人」，`text` 为 `进入下一阶段：<下一阶段名>`），再进入新阶段并写 `phase` 事件（该事件只带阶段名）；其余命令优先投递到当前阶段的等待通道（搜证 / 投票 / 发言权 / 主持人定序）。
等待通道里收到 `speak.text` 一律按自由发言落盘，不会因为该座位欠一次搜证或投票而被吞掉；
`clue.reveal` 是旁路命令，任何阶段都生效（包括正轮到自己搜证、发言或投票时），
失败原因（例如线索不属于你）以 `error` 帧回写。
没有等待者时同样作为自由发言处理（开场阶段由主持人推进，非主持人的 `speak.text` 会被拒绝）。

阶段顺序由剧本的 `order` 决定，对局里只读：`round_robin` 按入座顺序（主持人优先），
`random` 在阶段开始时洗一次并把结果写成一条 `system` 事件，
`host_assigned` 则在阶段开始时挂起，等主持人用 `order.set` 提交整轮顺序；
`seatIds` 少一个或多一个都属于非法命令，以 `error` 帧回写并继续等。阶段带 `onlyRoleIds` 时，
「可行动角色」就是名单里的那些人（如 K5 的三轮搜证只排五位巫师，黑猫不占回合），
主持人面板列出的也正是这些座位，座位状态里不会给名单外的角色发抽卡按钮或剩余次数。
搜证阶段的 `searchMode` 同是剧本字段：`pick` 给列表由玩家选，`random` 不给列表，
玩家只能发 `search.draw`，服务端在「仍可搜」的池里均匀抽一条（越权用 `search.clue` 会被拒）。
「仍可搜」已经扣掉三类卡：`subjectRoleId` 是本座位自己的搜身线索、`requiresItems` 没拿齐、
`unlockedBy` 里的前提线索还没露面。一个座位如果剩下的全是取不了的卡，引擎把这轮配额判完，
写一条 `system`（仅本座位），不给人一个只会报错的按钮。
`ownerRoleId` 与 `subjectRoleId` 方向相反：它不是「本人不得抽」，而是「只有本人能抽」，别人发
`search.clue` 指这张会被拒、随机抽取也抽不到（K10 曲径通幽 06-10 的「××专属线索」）。
带有 `grantsClue` 的底卡把「指定的深入调查线索」交给抽到它的人：那几个 id 不必在 `cluePool`
里，抽到底卡的座位可以随时用 `search.clue` 点名称取（跨阶段也有效），仍要满足自己的
`requiresItems` 与 `unlockedBy`；没抽到底卡的人既看不到也拿不走。
剧本把该搜证段设为 `revealOnFind: true` 时，`search.clue` / `search.draw` 成功即按公开处理：
`ClueState.Revealed` 置位、发 public 的 `clue.revealed`（`meta` 带 `title`/`content`），不再发私有的 `clue.found`。
单张卡带了 `mustReveal: true`（卡面写「本条线索必须公开」）时，即使所在阶段没开 `revealOnFind`，也走同一条公开路径。
带有 `grantsItem` 的卡会在同一时刻写一条仅本座位可见的 `system`：「你获得了物品【X】」；
带有 `keywords` 的私人物品（记忆碎片）则会在「公开线索正文」或「当众发言」出现命中词时自动解锁。
碎片带 `unlockedBy` 时，列出的前几页都必须已经解锁，否则这一页不翻（K6 的三页记忆按页递进）；
带 `keywordsAll: true` 时列出的关键词要**全部**当众说过才解锁，不必在同一句里（K6 记忆二要同时说到「红烟深处」和「亡青叶」）。

`whisper.send` 是讨论段专用的旁路命令：不占麦、不进发言队列，也不消耗自由讨论的发言配额，
所以两个人小声说话不会把整桌的麦克风堵住。它写一条 `whisper` 事件，可见范围是
`pair:<座位A>,<座位B>`（ID 排序后拼接），第三人和主持人都收不到；因此它既不触发关键词解锁
（没当众说出来就不算听见），也不进入前情提要（提要只收 public 事件）。
非讨论阶段（搜证 / 投票 / 复盘）发私聊会被拒绝；观战者没有私聊权；不能发给自己。
收话方是 AI 座位且发话方是真人时，引擎在后台补一次「只能回这一句私聊」的模型调用，
回成 `whisper` 或一条「没有接这条私聊」的私有 `system`；AI 发给 AI 不自动回，避免两边互相私聊停不下来。
一次行动一轮：座位仍欠次数时保持发言权/搜证权不切换，配额用尽才按顺序交给下一位。

#### 点数经济与特殊技能（K6《后天魔之谜》）

剧本写 `meta.startPoints`（开局点数）与 `meta.pointsLabel`（点数的叫法，K6 是「仙骨」）时，
开局给每个玩家座位发这么多点，搜证每抽一张扣 `phase.drawCost`（不写就记 1）。一张都抽不起时
引擎不再把回合交给这个座位：`picksLeft` = min(本轮剩余次数, 点数 / 单价)。

- 点数全程公开（桌上仙骨是摆出来的）：`SeatState.tablePoints` 给出每个玩家座位的余额，
  自己的写在 `SeatState.points`，`SeatState.pointsLabel` 是它的名字。没有点数的本子这三个字段为空。
- `search.pass` 只在轮到自己搜证时合法：把本轮剩余次数作废放回顺序末尾，**不扣点数**（K6 千秋的「结束本轮」）。
  座位不在搜证名单、额度已用完时客户端不会拿到按钮。
- 阶段写 `order: "role_assigned"` 时，定序面板发给 `phase.orderRoleId` 对应的玩家座位，而不是房间主持人；
  该角色不在座时退回主持人。
- 阶段写 `order: "fixed"` 时顺序完全照 `phase.orderRoleIds` 走，不洗牌也不等主持人；
  写进名单的座位若不在房间里属于剧本错误，校验闸门会拒。
- `role.skill` 是一局一次的特殊技能，`skill.use` 在**任意搜证阶段**都能发（不必轮到自己，也不占回合），
  `SeatState.skill` 会带上 `ready`/`why` 让客户端知道现在能不能按。四种 `effect`：
  `steal_draws`（夺走目标本轮剩余抽卡权，`target: "next"` 取本轮顺序里的下一位）、
  `burn_points`（自己付 `exactCost` 点、目标点数清零）、
  `grant_points`（`targetRoleId` 的那位恢复 `value` 点）、
  `show_clues`（目标向施术者展示 `value` 张未公开线索，`public: true` 时改为向全桌公开）。
  每次发动都会写一条公开 `system`，全场都会看到谁动了谁的仙骨。

错误以 `{ "type": "error", "message": "..." }` 回写，不关闭连接。

### 卡牌本（`mode: "cardgame"`，K19）

阶段类型换成 `play` / `score`。出牌回合只把发牌权交给一个人，因此这些命令都落在
「当前出牌人」的等待通道里；不是自己的回合会收到 `error`，不会静默丢掉。

```jsonc
{ "type": "card.play", "cardId": "c-scene-03-1" }               // 普通牌：同色或同数字
{ "type": "card.play", "cardId": "s-inspect-3", "to": "seat_b" }  // 技能牌：指定一位玩家
{ "type": "card.play", "cardId": "f-recolor-1-2", "color": "现场" } // 线索中断：指定颜色
{ "type": "card.random" }    // 只在「天黑请停止搜索」之后合法
{ "type": "card.pass" }      // 手上真的没有能出的牌才能过
{ "type": "card.stall", "cardIds": ["c-sms-02-1", "c-news-05-2"] } // 陷入僵局：挑两张已出的线索牌
{ "type": "card.give", "cardId": "c-scene-07-1" }                // 交换线索：交还一张自己的牌
{ "type": "score.submit", "roleId": "r_zhen", "to": "seat_c", "guesses": { "seat_a": "r_tan" } }
```

- **发牌**：`play` 阶段开始时洗牌并发牌，每人 `deal` 张（0 表示牌堆平均分）；摸到带
  `first` 标记的牌的人先出。发牌事件是公开的，未知词卡里印着「需要公开」的那几张也在
  同一时刻公开宣告。
- **合法牌**：与台面同色或同数字；黑技能牌（调查身份 / 陷入僵局 / 交换线索）任何时候都
  合法，但必须指定别人。`线索中断` 指定的颜色只约束下一手，下家过牌后就失效。
- **技能牌不改变要跟的牌**：技能牌没有颜色和数字，它落台后下家跟的仍是它前面那张
  （`table.top` 是堆顶，`table.base` 才是要跟的那张）。
- **随机出牌**：`card.random` 只在 `randomNext` 为真时接受；没有这个标记时服务端拒绝，
  否则任何客户端都能把一张出不掉的牌丢掉。
- **技能第二步**：`card.play` 之后如果引擎返回 `pending`，同一个座位继续占用发言权，
  用 `card.stall` / `card.give` 收尾；两张都交完才轮到下家。没有可选的牌（例如技能牌
  就是最后一张）时这一半作废，牌局照常推进——不会停在别人无法完成的等待上。
- **技能目标**：已经出完牌的人不能再被指定（他不会再摸牌，牌就出不来了）；只剩自己时
  技能牌仍然可以出，效果作废。
- **过牌**：`playable` 里还有牌时 `card.pass` 会被拒。全员连续过牌时，由最后一位出牌人
  随机打一张（`CardState.last` 记录的是他）；如果只剩一个人还有牌，就由这个人随机打。
- **结算**：`score.submit` 对应印刷的积分卡四项。名次按把手牌出完的先后，分值为
  `points`（10/7/4/1/0）；凶手身份、凶手玩家、每人身份各 ±5/−3；「指认自己是凶手而
  最终确实是凶手」这一条不计分。凶手总分进入 `killerRank`（3）名内则凶手获胜。
- **身份保密**：卡牌本从发牌到 `reveal` 之前，别人的 `visible.seats[].roleId/roleName`
  为空，事件与 `transcript` 里的 `roleId` 也不下发，`speaker` 只写座位名；
  `visible.cardRoles` 才是盒盖上印的五个角色（A–E）。房间页的座位列表同样不写角色名。
- **余牌计数**：`table.deck` 是「还没打出来的牌数」，牌堆一次性发完、没有摸牌堆。

### 卡牌本的 `SeatState`

```jsonc
{
  "hand": [{ "id": "c-scene-03-1", "kind": "clue", "color": "现场", "number": 3, "title": "现场 03", "text": "…" }],
  "table": {
    "top": { "id": "s-inspect-3", "kind": "inspect", "title": "调查身份", "text": "…" },
    "base": { "id": "c-sms-02-2", "kind": "clue", "color": "短信记录", "number": 2, "title": "短信记录 02", "text": "…" },
    "turnSeatId": "seat_b",
    "counts": { "seat_a": 26, "seat_b": 27 },
    "finished": ["seat_d"],
    "forcedColor": "现场",
    "randomNext": false,
    "pending": { "seatId": "seat_b", "kind": "stall", "target": "seat_c" },
    "result": [{ "seatId": "seat_a", "rank": 1, "rankPoints": 10, "killerPoints": 5, "playerPoints": -3, "identityPoints": 10, "total": 22 }],
    "playable": ["c-scene-03-1", "s-inspect-3"],
    "colors": ["现场", "试探身份", "新闻报道", "路人证言", "短信记录"],
    "deck": 108,
    "discard": [{ "id": "c-scene-01-1", "kind": "clue", "color": "现场", "number": 1, "title": "现场 01" }]
  },
  "scoreDone": false
}
```

`playable` 只算给本人（观战者拿到的是空），客户端不需要重算规则；`discard` 是整堆，
`陷入僵局` 的第二步要从里面挑牌。

### 状态查询

断线重连时先 `GET /api/rooms/{code}/state` 拉全量（已按座位过滤），再连 WS 取增量。

`SeatState`：

```jsonc
{
  "visible": {
    "gameId", "roomId", "phase", "phaseIndex", "round",
    "self": { "id", "roleId", "roleName", "displayName", "kind", "isHost", "connected", "ready" },
    "ownRole": { "id", "name", "public", "private" },   // 只有自己的
    "publicRoles": [{ "intro", "appearance" }],
    "ownClues": [Clue], "publicClues": [Clue], "ownFacts": [Fact],
    "items": ["钳子", "手电筒"],                       // 本座位物品栏（grantsItem 授予）
    "transcript": [{ "seq", "kind", "seatId", "roleId", "speaker", "text", "at" }],
    "truth": Truth,                                     // 仅 reveal 阶段 + 主持人视图
    "seats": [PublicSeat]
  },
  "phases": [{ "id", "name", "type", "round", "status": "pending|current|done" }],
  "floor": { "seatId", "phaseId", "expiresAt" },      // 发言与搜证轮次都会占用
  "status": "lobby|running|finished",
  "voteCount": 2,
  "votes": { "r3": 2 },                                // 仅观战/主持人
  "personas": { "seat_...": "谨慎型" },                 // seatId → 性格名（不含模型）
  "search": [{ "id", "hint" }],                      // 自选搜证：只给卡背面的字（地点＋编号），不含 title/content
  "draw": 2,                                          // 随机搜证：本座位还剩几次抽取
  "order": ["seat_a", "seat_b"],                       // 本阶段顺序（seatId 数组）
  "prompt": {                                          // 仅主持人：引擎正在等他做什么
    "kind": "order", "phaseId": "p3", "text": "…",
    "seats": [{ "seatId", "role", "display", "kind" }]
  }
}
```

所有数组字段（`ownClues` / `publicClues` / `ownFacts` / `items` / `transcript` / `seats` / `search` / `order`）在无内容时
是 `[]` 而非 `null`：严格 JSON 客户端不允许 `null`。

`visible` 中**不存在**的字段就是该座位拿不到的信息：`truth`、他人的 `private`、未公开线索、
其他座位的 `personaId` / `modelId` 都在结构层面被排除。带 `keywords` 的私人物品（记忆碎片）
在关键词被说出之前，连它的持有者在 `ownClues` 里也看不到；`visible.items` 只含本座位的物品。

## 7. 信息隔离的保证方式

1. `engine.VisibleFor(game, script, seat, opt)` 是唯一裁口，任何下发数据或 prompt 都只吃它的返回值；
2. `engine.PromptBuilder` 的入参是 `Visible`，函数签名层面拿不到 `Game`/`Script`；
3. `engine.LeakGuard` 对生成文本做真相/他人秘密/提示词回显检查，命中先重写一次，再命中则模糊化并记
   `leak_incident` 给主持人；
4. WebSocket 下行同样过 `EventVisibleTo`，不依赖前端隐藏。

`go test ./internal/engine` 中每个座位都有 golden 断言：可见字段集合多一个就失败。
