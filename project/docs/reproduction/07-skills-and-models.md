# 07 Skill 与模型

五个 Skill 是 daily-review、algorithm-diagnosis、interview-followup、project-explanation、weekly-plan。每个定义包含版本、prompt 版本、schema 版本、目标 agent 与允许工具。

Fake 与 OpenAI-compatible Real 实现相同窄接口。两种模式都经过 JSON Schema 校验；Fake 输出和 embedding 确定，可用于离线验收，Real 通过环境变量配置 endpoint、model 与 key。
