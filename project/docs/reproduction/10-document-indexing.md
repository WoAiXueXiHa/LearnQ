# 10 异步文档索引

上传支持 Markdown、TXT、JSON，限制 5 MiB、有效 UTF-8，JSON 还需语法有效。API 只落库并创建 document_index task；worker 按 uploaded→parsing→embedding→indexing→ready 推进。

切块保留标题和起止行，使用 rune 边界避免切坏 UTF-8。chunk 与 point ID 由内容稳定生成；中途失败重置为 uploaded 后幂等重建，最终 dead 把文档标成 failed。
