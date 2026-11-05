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

**服务端不解析原始剧本。** 它只负责：归档你上传的原件、存草稿、校验发布。把 docx / pdf / 压缩包 / 照片
变成结构化剧本这一步由开发者线下完成。

1. **上传原件**：工作台「原始素材」直接传 zip、docx、pdf、照片，文件原样落到服务端
   `data/inbox/<时间戳>-raw_<随机>.<ext>`，面板上直接显示每份文件的服务端绝对路径。不解压、不 OCR、不调用
   LLM。
2. **转成标准 JSON**：按 `docs/script.schema.json` 把原件写成剧本 JSON（仓库里已转好的放 `scripts/`，
   例如 `scripts/K7-星火号.json`），然后二选一：
   工作台「新建空白草稿」拿一份带默认阶段的骨架（reading → 讨论 → 搜证 → 公聊 → 投票 → 复盘）
   逐字段填，或「粘贴 JSON」整份导入。格式不对也能存：导入返回的 `errors` 就是待修清单。
3. **校对与发布**：左侧点中任一剧本即可编辑，草稿与已发布没有区别（已发布剧本保存即发布新版本，
   版本号 +1；进行中的对局仍用开局时那一版，新开的房间用新版本；已发布剧本只接受能通过校验的内容）。
   「结构化」模式改基础信息（标题/简介/人数/难度/时长/标签）和**阶段与轮次**
   （类型、名称、轮次、顺序策略、搜证方式、搜证/发言次数、超时、线索池、主持人手册，可上下移动与增删）；
   「JSON」模式改角色、线索、真相、事实表等复杂结构。
   发布前校验：第一个阶段必须是 `reading`、必须有 `reveal`、搜证/讨论阶段的次数 > 0 等。
4. **阶段策略**（都是剧本字段，对局里只读）：
   - `order`：`round_robin` 按入座顺序（主持人优先）、`random` 阶段开始时随机排一次、
     `host_assigned` 阶段开始时挂起，等主持人在对局页一次性排好整轮顺序；
   - `searchMode`：`pick` 看标题自选、`random` 只给「随机抽取」按钮，抽到什么算什么（线索内容仍只有抽取者可见）。
   两个策略对搜证与讨论阶段都生效；一次行动一轮，配额用尽才轮到下一位。

管理台（导航栏「管理台」）：

- 账号：列表（含角色、剧本数、注册时间、最近登录），可为任意账号重置为一次性临时密码。
  密码是 bcrypt 单向哈希，**看不到原密码**，这是唯一可行的「找回」方式；
- 房间：列表（剧本、房主、状态、人数），可「结束」（保留复盘）或「删除」（连记录一起删）。

## 单进程部署（前端 + API 同源）

```bash
cd web && npm run build                       # 产物在 web/dist
cd ../server && go build -o bin/script-play ./cmd/server
SP_ADDR=:9091 \
SP_DATA_DIR=./data \
SP_WEB_DIR=$(pwd)/../web/dist \
SP_LLM_PROVIDER=qodercli \
nohup ./bin/script-play > /tmp/script-play.log 2>&1 &
```

`SP_WEB_DIR` 设置后同一端口同时托管前端、`/api` 与 `/ws`，不需要额外反代。
`/assets/*`（Vite 带指纹的文件名）返回 `immutable` 长缓存，`index.html` 返回
`no-store`，避免浏览器拿旧壳指向已删除的 bundle；缺失的 `/assets/*` 直接 404，
不会被 SPA 兜底伪装成 200 的 HTML。

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
| `SP_DATA_DIR` | `./data` | 数据目录（JSON 快照 + `inbox/` 原件 + JWT secret） |
| `SP_WEB_DIR` | 空 | 设置后同时托管前端静态文件（生产单进程部署） |
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
| `SP_MAX_UPLOAD_MB` | `64` | 单个原件上传上限（MB） |

## 信息隔离怎么保证

1. `engine.VisibleFor` 是唯一裁口：任何下发给客户端或 prompt 的数据都只吃它的返回值；
2. `engine.PromptBuilder` 的入参是 `Visible`，签名层面拿不到 `Game`/`Script`；
3. `engine.LeakGuard` 对生成文本做真相/他人秘密/提示词回显检查，命中先重写一次，
   再命中则模糊化并把 `leak_incident` 事件只发给主持人；
4. WebSocket 下行同样按座位过滤，不做「前端隐藏」。

`go test ./internal/engine` 里每个座位都有 golden 断言：可见字段集合多一个就失败。
`go test ./internal/httpapi -run TestFullGame` 会用 REST + WebSocket 真实打完一局（5 个阶段）。

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
  叠加语速/音高，同时朗读主持人台词、线索公开、投票结果与复盘：一局多人不会听起来像同一个人。
  **语音输入已移除**：`getUserMedia` 与 `SpeechRecognition` 都要求 secure context，本服务以
  `http://<IP>:9091` 部署时麦克风对象根本不存在，按钮只能置灰；对局页改为提示玩家用系统输入法的
  语音听写（手机输入法麦克风键 / Windows Win+H / macOS 连按 Fn），结果直接落进输入框可编辑。
  sherpa 与服务端 ASR 都需要先有 TLS 或原生壳才划算，留在路线图里。
- **存储**：方案写的是 SQLite。首版实现为单进程内的 JSON 快照存储（原子 rename + 防抖落盘），
  零 cgo、零迁移；接口集中在 `internal/store`，替换成 SQLite 不影响其它包。
- **`qoder-acp`**：未实现，当前每个回合起一个 headless 子进程（冷启动 2~5s）。
