# 12 离线 RAG 评估

JSONL 每行包含 id、question 与带 relevance 的 relevant_chunks，可附答案、引用文本、标签和难度。评估固定运行 dense-only、sparse-only、hybrid-rrf 三路。

输出 Recall@K、NDCG 与引用覆盖率，并保存配置、embedding 模型、collection、指标 JSON 和 Markdown 报告。Fake 模式标记 pipeline_test，Real embedding 标记 retrieval_benchmark。
