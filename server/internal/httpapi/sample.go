package httpapi

// SampleScriptJSON is an original four-player script used by the sample
// endpoint and by the first-run seed. It is fictional and tiny on purpose.
const SampleScriptJSON = `{
  "schemaVersion": 1,
  "meta": {
    "title": "雨夜灯塔（示例）",
    "author": "script-play",
    "summary": "台风夜，灯塔看守人被发现死在塔顶。四个人各有一段说不清的十五分钟。",
    "difficulty": "easy",
    "tags": ["本格", "四人本", "短本"],
    "playerCount": {"min": 4, "max": 4},
    "durationMin": 90
  },
  "truth": {
    "killerRoleId": "r3",
    "method": "用灯塔的备用钥匙反锁塔顶，再以检修电缆为由掩盖停电的十分钟，让死者的死亡时间被误判。",
    "motive": "死者掌握了周叙挪用灯塔维修款的全部账目。",
    "summary": "周叙在台风夜以检修电缆为名切断了灯塔供电，趁黑用扳手击倒看守人老陈，再把备用钥匙放回值班室，制造出老陈在停电后才遇害的假象。真正的死亡时间比所有人以为的早了十分钟。",
    "timeline": [
      "21:40 老陈照例登塔点灯",
      "21:55 周叙以检修电缆为由拉下总闸",
      "22:05 灯塔恢复供电，老陈已被击倒",
      "22:20 林晚在值班室门口遇到浑身湿透的周叙"
    ]
  },
  "phases": [
    {"id": "p1", "type": "reading", "name": "阅读剧本", "dmScript": "台风逼近，灯塔的灯灭了十分钟。第二天清晨，看守人老陈被发现倒在塔顶。先花时间读完自己的角色本，再开始自我介绍。"},
    {"id": "p2", "type": "discussion", "name": "自我介绍与案情讨论", "round": 1, "speechPerPlayer": 1, "order": "round_robin"},
    {"id": "p3", "type": "search", "name": "第一轮搜证", "round": 1, "perPlayerPick": 2, "cluePool": ["c1", "c2", "c3", "c4", "c5"]},
    {"id": "p4", "type": "discussion", "name": "第一轮公聊", "round": 1, "speechPerPlayer": 2, "order": "round_robin"},
    {"id": "p5", "type": "search", "name": "第二轮搜证", "round": 2, "perPlayerPick": 1, "cluePool": ["c5", "c6"]},
    {"id": "p6", "type": "discussion", "name": "第二轮公聊", "round": 2, "speechPerPlayer": 1, "order": "round_robin"},
    {"id": "p7", "type": "vote", "name": "投票"},
    {"id": "p8", "type": "reveal", "name": "复盘"}
  ],
  "roles": [
    {
      "id": "r1",
      "name": "林晚",
      "personaId": "candid",
      "public": {"intro": "灯塔补给船的船长，昨夜送补给到岛上，性格直爽。"},
      "private": {
        "script": "你是补给船船长林晚。台风前你按计划送补给上岛，21:50 你在码头系缆绳，看到灯塔的灯灭了一次。你讨厌老陈，因为他拖欠过你的运费，但你只是和他吵了两句就走了。22:20 你在值班室门口撞见周叙，他浑身湿透、手里握着一把扳手，说是去修电缆。",
        "secrets": [
          {"id": "s1", "text": "你 21:40 和老陈在码头大吵了一架，还说了“你等着”这种狠话。", "revealCost": 0.55},
          {"id": "s2", "text": "你看到周叙 22:20 手里有扳手，衣服是湿的。", "revealCost": 0.3}
        ],
        "goals": ["洗清自己的嫌疑", "找出真正动了手的人"],
        "knownFactIds": ["f1"]
      }
    },
    {
      "id": "r2",
      "name": "苏禾",
      "personaId": "guarded",
      "public": {"intro": "岛上气象站的观测员，负责记录台风数据，话不多。"},
      "private": {
        "script": "你是气象观测员苏禾。21:30 到 22:10 你一直在气象站记录数据，日志可以证明。但你删掉了其中一段，因为那十分钟你在偷偷给岸上的家人打电话，用的是灯塔的卫星电话。你不想让人知道你去过灯塔值班室。",
        "secrets": [
          {"id": "s3", "text": "22:00 前后你进过灯塔值班室用卫星电话，离开时值班室的门是虚掩的。", "revealCost": 0.65}
        ],
        "goals": ["别让人发现你私用卫星电话", "用气象数据帮助大家还原时间线"],
        "knownFactIds": ["f2"]
      }
    },
    {
      "id": "r3",
      "name": "周叙",
      "personaId": "sly",
      "public": {"intro": "灯塔的维保工程师，负责这次台风前的线路检修。"},
      "private": {
        "script": "你是维保工程师周叙，也是拿走维修款的人。老陈翻到了账目，今晚约你在塔顶谈。21:55 你拉下总闸制造停电，在黑暗里用扳手击倒了他，再把备用钥匙放回值班室。22:20 你从塔上下来，遇到林晚，于是说自己是去修电缆。你必须把死亡时间往后推十分钟，否则你自己的不在场证明会崩。",
        "secrets": [
          {"id": "s4", "text": "总闸是你主动拉下的，而且你知道备用钥匙放在哪里。", "revealCost": 0.95},
          {"id": "s5", "text": "你挪用了灯塔维修款，老陈手里有账目复印件。", "revealCost": 0.8}
        ],
        "goals": ["把死亡时间说成 22:05 之后", "让林晚成为最可疑的人"],
        "knownFactIds": ["f3"]
      }
    },
    {
      "id": "r4",
      "name": "陈砚",
      "personaId": "aggressive",
      "public": {"intro": "死者的侄子，来岛上处理叔父的旧宅，脾气急。"},
      "private": {
        "script": "你是老陈的侄子陈砚。你昨天下午才上岛，为了旧宅的产权和叔父吵过。21:00 到 22:00 你在旧宅整理遗物，没有人证。你其实在叔父的抽屉里看到了半张账目复印件，上面有周叙的名字，但你怕被当成嫌疑人，把复印件藏进了口袋。",
        "secrets": [
          {"id": "s6", "text": "你口袋里藏着半张写有周叙名字的账目复印件。", "revealCost": 0.5}
        ],
        "goals": ["找出谁害了叔父", "别让自己成为第一个被怀疑的人"],
        "knownFactIds": ["f3"]
      }
    }
  ],
  "clues": [
    {"id": "c1", "title": "沾了机油的扳手", "location": "塔顶楼梯拐角", "content": "一把 12 寸扳手，锤头有暗色污渍，手柄上有防滑手套的纹路。", "links": {"roles": ["r3"], "secrets": ["s4"]}},
    {"id": "c2", "title": "值班室的钥匙板", "location": "灯塔值班室", "content": "备用钥匙挂在原位，但挂钩上有一道新的擦痕，像是刚被取放过的。", "links": {"roles": ["r3"], "facts": ["f3"]}},
    {"id": "c3", "title": "气象日志残页", "location": "气象站", "content": "21:30 至 22:10 的记录被撕掉一页，边缘还留着胶带痕。", "links": {"roles": ["r2"], "secrets": ["s3"]}},
    {"id": "c4", "title": "码头缆绳", "location": "码头", "content": "缆绳上系着 21:45 的时间标签，绳结打得很急，说明有人匆忙离开。", "links": {"roles": ["r1"], "secrets": ["s1"]}},
    {"id": "c5", "title": "总闸的检修牌", "location": "配电房", "content": "检修牌上的签名是周叙，时间写着 22:05，但墨水还没完全干。", "links": {"roles": ["r3"], "secrets": ["s4"]}},
    {"id": "c6", "title": "半张账目复印件", "location": "旧宅书房", "content": "只剩半页的维修款账目，能辨认出“周”字和一串金额。", "links": {"roles": ["r3", "r4"], "secrets": ["s5", "s6"], "facts": ["f3"]}}
  ],
  "facts": [
    {"id": "f1", "text": "灯塔每晚 21:40 点灯，由看守人亲自登塔。"},
    {"id": "f2", "text": "台风期间全岛只有灯塔值班室有一部卫星电话。"},
    {"id": "f3", "text": "灯塔维修款由维保工程师周叙经手，账目由死者保管。"}
  ]
}`
