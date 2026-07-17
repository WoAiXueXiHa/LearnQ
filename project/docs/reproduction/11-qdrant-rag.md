# 11 Qdrant 混合检索

collection 使用命名 dense 与 sparse vector。dense 来自 embedding，sparse 来自确定性 hashed lexical 特征；hybrid 查询使用 dense/sparse prefetch 与 RRF fusion。

RAG 返回前会用 MySQL 再过滤 chunk 所属文档必须 ready。每条引用包含 source、document_id、chunk_id、标题、行号、摘要、rank 与 score；没有有效证据返回 `RAG_NO_EVIDENCE`。
