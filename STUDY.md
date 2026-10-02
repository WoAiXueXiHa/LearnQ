# LearnQ 借鉴研究：OpenTutor 与 OpenAI Evals

> 审阅日期：2026-09-29。OpenTutor 基于 [`db15d90`](https://github.com/zijinz456/OpenTutor/tree/db15d901a7c5c5f46941487ca4ebc2cfb20425c2)，OpenAI Evals 基于 [`8eac7a7`](https://github.com/openai/evals/tree/8eac7a7de5215c907fbddc30efdaf316913eccdd)。本文区分上游仓库的实现、LearnQ 已有能力和建议方案；建议不等于已实现，仓库 README 的产品宣称不等于实际效果证据。

## 一、先确定 LearnQ 要解决的问题

面向校招，LearnQ 值得展示的 AI 工程问题是：**一次基于个人资料的练习，怎样产生可核对的反馈，并能用固定样例证明改动确实改善了结果？** 用户流程应尽量短：选择资料 → 回答少量问题 → 看到具体差距和原文 → 下次复习薄弱题。底层异步可靠性继续服务这条流程，但不与 Herald 重复争夺“消息可靠投递”的项目主线。

目前 LearnQ 已具备学习记录、异步报告、复习任务、文档索引、RAG 问答和执行轨迹；尚无“用户作答 → 按题判定薄弱点 → 按题复习”的完整闭环。现有 [复盘提示](internal/skill/prompts/daily-review.md)只约束模型依据输入事实生成报告；[复习规则](internal/domain/review.go)把用户选择的 0–5 熟练度直接映射到 1、2、4、7、14、30 天。不能由“写过一条学习记录”推断用户理解了什么，也不能把这套简单排期称作经过验证的自适应复习。

## 二、OpenTutor：把资料转成持续练习

### 2.1 它的问题定义与方案

[OpenTutor README](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/README.md)把资料导入、教学、练习、记忆和提醒串成循环；[PRD](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/docs/PRD.md)进一步强调内容是工作区的主体，聊天只是控制入口之一，并用渐进展示减少新用户的界面负担。这个设计解决的不是“模型能否生成总结”，而是用户练过之后系统怎样保留证据、再次安排练习。

在实现上，`PracticeProblem` 保存题目、标准答案、解释、知识点、来源、生成批次和版本；`PracticeResult` 保存用户答案、正确性、错误类别与作答时间；`WrongAnswer` 保存错题、诊断详情与复习历史。[数据模型](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/apps/api/models/practice.py) / [错题模型](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/apps/api/models/ingestion.py)

出题和答题是分开的：生成题目可作为一个有版本的批次保存，旧批次可以归档；提交答案会记录作答，再根据错误更新错题与进度；错误后还可衍生一道更简单的诊断题，用来区分基础概念缺口与原题陷阱。[题目批次](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/apps/api/routers/quiz_generation.py) / [答题提交](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/apps/api/routers/quiz_submission.py) / [诊断题入口](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/apps/api/routers/wrong_answers.py)

**值得内化的因果关系：** 没有作答就没有关于“懂没懂”的观察；没有题目与原文的稳定关联，反馈就无法核对；没有逐次作答记录，排期就只能猜。先保存这些事实，再谈自适应。

### 2.2 OpenTutor 本身不能照搬的地方

- 在所审阅版本的[答题提交](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/apps/api/routers/quiz_submission.py)与[错题重答](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/apps/api/routers/wrong_answers.py)路径中，部分非编程题以答案字符串忽略大小写后相等来判定。对于开放式技术问答，这会把正确的不同表述判成错；LearnQ 不能照抄后声称能准确诊断理解。
- [错误分类器](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/apps/api/services/diagnosis/classifier.py)用结构化类别、置信度和解释约束模型输出，但模型调用或解析失败时会回退到 `conceptual` 类别和低置信度。LearnQ 应把这种情况显示为“未能判断”，不能写成已证实的概念错误。
- OpenTutor 有多种练习类型、工作区模块、知识图谱和实验性调度能力；[README](https://github.com/zijinz456/OpenTutor/blob/db15d901a7c5c5f46941487ca4ebc2cfb20425c2/README.md)也明确标注部分高级流程处于实验阶段。LearnQ 当前不需要复制完整产品面，只需验证一条小而可信的练习链路。

### 2.3 LearnQ 应如何吸收

建议的最小数据单位是**有依据的练习题**，而不是整份报告：

| 实体 | 最少保存什么 | 解决的问题 |
|---|---|---|
| `practice_question`（建议） | 资料 ID/版本、题干、参考要点、证据片段 ID 与位置、生成版本、状态 | 知道这道题从哪里来；原文变化后能识别旧题 |
| `practice_attempt`（建议） | 题目 ID、用户原答、提交时间、反馈结果与依据、人工纠正记录 | 保留“哪里没懂”的真实观察，便于申诉与回放 |
| `review_state`（建议） | 题目 ID、上次结果、连续答对次数、下次时间、跳过记录 | 只复习薄弱题，排期有历史可解释 |

这不是要求立刻加三张表，而是说明三类事实不能混在报告 Markdown 或单个 `mastery` 字段里。可按现有 [文档索引与 RAG API](internal/api/operations.go)、[报告落库与复习创建](internal/store/store.go) 的边界逐步接入。资料不足、片段失效或答案有歧义时，不生成确定性诊断；允许用户查看原文并纠正反馈。

## 三、OpenAI Evals：用固定样例把“感觉变好了”变成可复核结果

### 3.1 它的设计思路

[OpenAI Evals 的构建流程](https://github.com/openai/evals/blob/8eac7a7de5215c907fbddc30efdaf316913eccdd/docs/build-eval.md)把一次评测拆为：定义要检验的行为 → 制作 JSONL 样例 → 选择评分器 → 注册带版本的评测 → 在指定模型或系统上运行 → 查看逐例结果与汇总指标。数据集和版本是核心；改了样例或判定标准，要更新版本，避免两次分数不可比。

[模板说明](https://github.com/openai/evals/blob/8eac7a7de5215c907fbddc30efdaf316913eccdd/docs/eval-templates.md)区分确定答案适用的匹配类评测和开放答案适用的模型评分。模型评分把生成结果送入另一条评分提示，并约束输出为可解析的类别；[实现](https://github.com/openai/evals/blob/8eac7a7de5215c907fbddc30efdaf316913eccdd/evals/elsuite/modelgraded/classify.py)会记录每条样例的类别、分数和聚合结果，还支持用人工标签计算 `metascore`，检查评分器自身是否可靠。[模型调用接口](https://github.com/openai/evals/blob/8eac7a7de5215c907fbddc30efdaf316913eccdd/evals/api.py)把被测系统与评分逻辑解耦。

**值得内化的因果关系：** 指标只回答它测量的事。检索命中率回答“找到了没有”；答案评测回答“有没有按证据回答”；练习反馈评测回答“有没有指出用户真正的错”。三者不能互相代替。模型裁判本身也会错，必须拿人工标注校准，并保留“无法判断”这一类结果。

### 3.2 对照 LearnQ 现状

LearnQ 已有 [RAG JSONL 样例](data/eval/rag.jsonl)及 [检索评测实现](internal/evaluation/evaluation.go)：可比较 dense、sparse、hybrid 的 Recall@K、NDCG 和检索覆盖率，并区分 fake 管线测试与真实检索基准。这是可复用起点，但 `correct_answer`、`tags` 和 `difficulty` 当前未参与评分；现有运行只评估检索，**不评估最终答案或学习反馈的质量**。现有 [`make eval-rag`](Makefile)发送 `{}`，而[评测 API](internal/api/operations.go)要求实际 JSONL，因此该命令目前不能作为可信的一键评测入口。

LearnQ 当前对引用编号和动作权限有程序化检查，但引用存在、编号合法不意味着每句话都得到原文支持；检索覆盖率也不等于答案引用覆盖率。评测报告必须明确写出每个指标的分母、判定依据和未覆盖的问题。

### 3.3 建议的 LearnQ 评测集与运行记录

先取约 20–30 条**有使用权且可重复加载的技术资料样例**，每条给出稳定 ID、资料版本、问题、期望证据位置、参考要点、允许的不同表述与不应出现的结论。覆盖正常问答、跨段整合、原文无答案、相似概念混淆、来源失效、用户回答部分正确等。不要只用 README 中容易回答的题来证明产品效果。

示例形状（**拟议格式，非现有 API 输入**）：

```json
{"id":"study-001","source_version":"sha256:...","question":"为什么 Redis 领取后还要 MySQL Acquire？","expected_evidence":[{"document":"worker-notes.md","locator":"## Acquire"}],"reference_points":["Redis 仅表示队列领取","MySQL 建立租约与处理所有权"],"forbidden_claims":["Redis 领取即可保证唯一执行"],"tags":["retrieval","reasoning"]}
```

每次运行保存数据集哈希、代码提交、提示词版本、模型/Embedding 名称及参数、索引版本、每例召回片段、最终回答、评分、失败原因、耗时和费用。先用规则检查样例格式、证据 ID 和引用链接，再用人工标签评估语义正确性；若引入模型裁判，先用一小组人工标注样例核对裁判一致性，并保留人工复核入口。真实模型运行需单独执行和记录成本，自动测试仍只使用 Fake/Mock。

## 四、两个项目合起来，LearnQ 的最小可交付链路

```text
用户选笔记/文档
  → 固定资料版本与证据位置
  → 生成最多 2–3 道有明确依据的题
  → 用户先作答
  → 保存原答，再给“正确要点 / 漏点 / 疑点 / 原文链接”
  → 用户可纠正反馈
  → 按题保存结果和下一次练习时间
  → 把匿名或自有的失败样例回收到版本化评测集
  → 改提示词/检索/评分后，用同一数据集比较逐例差异
```

产品上只展示题目、反馈、原文和下次练习；Trace、检索分数、提示词版本放在诊断页。技术上把每个 AI 输出当作**待检验建议**，保留原始输入、证据与用户修正。首轮排期用保守、可解释的规则，并保存逐题事件；有足够历史后再考虑 FSRS 等模型。模型失败时保留题目和作答，不伪造评分或“已掌握”。

## 五、实施顺序与停止线

1. **先修事实与状态：** 修复错误成功/自检展示和 `make eval-rag`；保证“失败、未评估、无证据”可区分。验收为命令可用、失败可见、已有数据不丢。
2. **建立评测基线：** 冻结资料与样例版本，先跑检索；再人工标注若干真实回答和“用户答错”的反馈质量。输出逐例记录，而非只有平均分。若真实模型因鉴权或依赖失败，记录阻塞，不把 Fake 结果写成模型质量证据。
3. **只做最短练习链路：** 有依据的题、作答、具体反馈、原文定位、逐题复习。先确保内容可读、反馈可纠正，再考虑更复杂排期。
4. **决定是否扩展：** 用真实资料反复走通，检查用户是否愿意第二次回来。若评测不能证明薄弱点判断更准，或交互比直接用备忘录/通用 AI 更费事，就停止扩展知识图谱、多 Agent 和自适应界面。

完成这几步后，面试表述应是“我怎样定义 AI 输出质量、收集失败样例、设计可复现评测并修正问题”，而不是“我接入了多少模型或 Agent”。Herald 继续负责说明通知投递可靠性；LearnQ 负责说明 AI 结果的证据、质量与用户反馈闭环。
