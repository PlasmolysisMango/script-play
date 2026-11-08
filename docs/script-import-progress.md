# 全量剧本导入进度（K1–K28 + AOFU）

> 本机实测：`raw/` 的 PDF 页数与图片数来自 pypdf / `os.walk`，线上状态读自 `scripts/*/script.json` 与 `server/data/state.json`。
> 完成口径见 `docs/script-framework-gaps.md` 与导入 SOP（转录 1:1 → check-scripts → 冒烟 → 发布 → 复查）。

| 本 | 原件 PDF / 页 | 独立图片 | 转录 | 阶段 / 线索 | 线上 | 勘察 survey |
| --- | --- | --- | --- | --- | --- | --- |
| K1 | 7 / 71 | 83 | ✅ `K1-游轮迷影` | 10 段 / 58 条 | published v8 | ✅ |
| K10 | 11 / 118 | 7 | ✅ `K10-梁溪诡影` | 13 段 / 70 条 | published v8 | ✅ |
| K11 | 11 / 134 | 0 | ✅ `K11-再见苏西` | 10 段 / 88 条 | published v2 | 缺少 |
| K12 | 7 / 113 | 22 | ✅ `K12-娘娘千岁` | 18 段 / 84 条 | published v1 | ✅ |
| K13 | 9 / 73 | 0 | ✅ `K13-群星落幕时` | 11 段 / 50 条 | published v2 | ✅ |
| K14 | 8 / 47 | 14 | ✅ `K14-动物监友会` | 9 段 / 59 条 | published v1 | ✅ |
| K15 | 6 / 131 | 8 | ✅ `K15-屠龙之证` | 8 段 / 40 条 | published v1 | ✅ |
| K16 | 11 / 115 | 0 | ✅ `K16-血色唐人街` | 8 段 / 49 条 | published v1 | ✅ |
| K17 | 9 / 119 | 7 | ✅ `K17-临渊客栈` | 11 段 / 55 条 | published v1 | ✅ |
| K18 | 9 / 136 | 0 | ✅ `K18-谁与争疯` | 10 段 / 15 条 | published v1 | ✅ |
| K19 | 0 / 0 | 31 | ✅ `K19-生日快乐` | 4 段 / 0 条 | published v1 | ✅ |
| K2 | 18 / 260 | 14 | ✅ `K2-黑夜传说` | 13 段 / 31 条 | published v1 | ✅ `.tmp-import/K2/review.md` |
| K20 | 12 / 156 | 0 | ⏭ 缺件跳过（无真相） | — | 未发布 | ✅ `.tmp-import/K20/survey.md` |
| K21 | 8 / 106 | 0 | ✅ `K21-迷失航班` | 9 段 / 40 条 | published v1 | ✅ `.tmp-import/K21/survey.md` |
| K22 | 10 / 233 | 18 | ✅ `K22-妖猫出长安` | 12 段 / 55 条 | published v1 | ✅ `.tmp-import/K22/review.md` |
| K23 | 12 / 209 | 180 | ✅ `K23-诚如恶魔低语` | 18 段 / 55 条 | published v1 | ✅ `.tmp-import/K23/review.md` |
| K24 | 14 / 290 | 120 | ✅ `K24-惊魂青金村` | 14 段 / 61 条 | published v1 | ✅ `.tmp-import/K24/review.md` |
| K25 | 12 / 145 | 0 | ✅ `K25-小白船上的哭声` | 12 段 / 20 条 | published v1 | ✅ `.tmp-import/K25/survey.md` |
| K26 | 5 / 110 | 0 | ⏭ 缺件跳过 | — | 未发布 | ✅ `.tmp-import/K26/survey.md` |
| K27 | 11 / 183 | 0 | ✅ `K27-隐秘关系` | 10 段 / 54 条 | published v2 | ✅ |
| K28 | 10 / 195 | 0 | ✅ `K28-女帝攻略` | 18 段 / 80 条 | published v1 | ✅ `.tmp-import/K28/survey.md` |
| K3 | 7 / 117 | 9 | ✅ `K3-大疫` | 10 段 / 60 条 | published v2 | ✅ |
| K4 | 12 / 96 | 0 | ✅ `K4-觉醒` | 8 段 / 44 条 | published v1 | ✅ |
| K5 | 11 / 143 | 0 | ✅ `K5-杀死巫师` | 14 段 / 60 条 | published v2 | ✅ |
| K6 | 9 / 123 | 0 | ✅ `K6-后天魔之谜` | 12 段 / 58 条 | published v4 | ✅ |
| K7 | 6 / 107 | 12 | ✅ `K7-星火号` | 10 段 / 55 条 | published v2 | 缺少 |
| K8 | 8 / 103 | 0 | ✅ `K8-望雀` | 12 段 / 71 条 | published v1 | ✅ |
| K9 | 10 / 126 | 0 | ✅ `K9-疯人塔` | 12 段 / 74 条 | published v2 | ✅ |
| AOFU | 10 / 167 | 70 | ✅ `AOFU-持斧奥夫` | 11 段 / 46 条 | published v1 | ✅ `.tmp-import/AOFU/review.md` |

## 汇总

- 已转录并发布：**26 / 26** 本（K1、K2、K3、K4、K5、K6、K8、K9、K10、K12、K13、K14、K15、K16、K17、K18、K19、K21、K22、K23、K24、K25、K27、K28，加原有 K7、K11）；另导入 AOFU《持斧奥夫》（7 人 / 11 段 / 46 条），已发布 v1。
- 缺件跳过：**K26**（5 PDF / 110 页，线索与真相目录为空，未生成 `script.json`；详见 `docs/script-framework-gaps.md` 与 `.tmp-import/K26/survey.md`）。
- 真相缺失跳过：**K20**（12 PDF / 156 页，真相/结局/复盘在外部二维码，目录内无 1:1 可转录真相；详见 `docs/script-framework-gaps.md` 与 `.tmp-import/K20/survey.md`）。
- 全量计划内剧本已全部处理完毕；AOFU《持斧奥夫》为计划外追加导入。

## 每本收尾检查

```
make check-scripts ; make test ; make web-check
python3 .tmp-import/publish.py [--match=<库里旧标题>] <剧本目录名>
```
