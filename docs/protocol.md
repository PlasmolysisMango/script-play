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
管理员额外拥有：剧本库全量读写（含内置示例）、原件归档入口、`/api/admin/*` 管理台。

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
| POST | `/api/scripts/sample` | 管理员：一键导入内置示例剧本并发布 |
| POST | `/api/scripts/{id}/media` | 管理员：`multipart/form-data`，字段 `file`（也接受 `image`/`files`）；png/jpg/jpeg/webp/gif，单张 ≤ 8 MB；返回 `201 {url, name, size, mime}` |
| GET | `/media/{id}/{name}` | 图片回放（免登录）；`Content-Type` 只由扩展名决定并带 `nosniff`，缺失或不在白名单返回 404 |
| GET | `/script-assets/{剧本目录}/{路径}` | 随剧本固化的图（`scripts/<剧本目录>/images/…`，免登录）；只放行图片扩展名，`script.json` 拿不到，`Cache-Control: max-age=3600` |

### 剧本从哪来

服务端**不解析**任何原始剧本。它只做三件事：归档原件、存草稿、校验发布。

1. **归档原件**：管理员把压缩包、docx、pdf、照片上传到 `POST /api/admin/materials`，
   文件原样存进服务端 `data/inbox/<时间戳>-raw_<随机>.<ext>`，`state.json` 只多一条
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
| GET | `/api/admin/materials` | `{materials: RawMaterial[], dir}`；`dir` 是收件目录绝对路径 |
| POST | `/api/admin/materials` | `multipart/form-data`，字段名 `files`（可重复），返回 `201 {materials, dir}`；单文件上限 `SP_MAX_UPLOAD_MB`（默认 64） |
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
| `agent.delta` | AI 流式片段（不落库） | public |
| `system` | 轮到你发言 / 搜证超时等提示 | public 或 `seat:<id>` |
| `clue.found` | 搜到线索（内容私有） | `seat:<id>` |
| `clue.revealed` | 公开某条线索 | public |
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
{ "type": "clue.reveal", "clueId": "c1" }
{ "type": "vote.cast", "roleId": "r3" }
{ "type": "order.set", "seatIds": ["seat_a", "seat_b"] }  // 主持人定序：必须是全部玩家的一个排列
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
`seatIds` 少一个或多一个都属于非法命令，以 `error` 帧回写并继续等。
搜证阶段的 `searchMode` 同是剧本字段：`pick` 给列表由玩家选，`random` 不给列表，
玩家只能发 `search.draw`，服务端在「仍可搜」的池里均匀抽一条（越权用 `search.clue` 会被拒）。
一次行动一轮：座位仍欠次数时保持发言权/搜证权不切换，配额用尽才按顺序交给下一位。

错误以 `{ "type": "error", "message": "..." }` 回写，不关闭连接。

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

所有数组字段（`ownClues` / `publicClues` / `ownFacts` / `transcript` / `seats` / `search` / `order`）在无内容时
是 `[]` 而非 `null`：严格 JSON 客户端不允许 `null`。

`visible` 中**不存在**的字段就是该座位拿不到的信息：`truth`、他人的 `private`、未公开线索、
其他座位的 `personaId` / `modelId` 都在结构层面被排除。

## 7. 信息隔离的保证方式

1. `engine.VisibleFor(game, script, seat, opt)` 是唯一裁口，任何下发数据或 prompt 都只吃它的返回值；
2. `engine.PromptBuilder` 的入参是 `Visible`，函数签名层面拿不到 `Game`/`Script`；
3. `engine.LeakGuard` 对生成文本做真相/他人秘密/提示词回显检查，命中先重写一次，再命中则模糊化并记
   `leak_incident` 给主持人；
4. WebSocket 下行同样过 `EventVisibleTo`，不依赖前端隐藏。

`go test ./internal/engine` 中每个座位都有 golden 断言：可见字段集合多一个就失败。
