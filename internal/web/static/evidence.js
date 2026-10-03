/* Only verified, immutable application evidence routes are accepted. */
let evidenceRevision = 0;
let evidenceReturnFocus = null;
let evidenceData = null;
const evidenceDialog = document.createElement("dialog");
evidenceDialog.id = "evidencePanel";
evidenceDialog.setAttribute("aria-labelledby", "evidenceTitle");
evidenceDialog.innerHTML = '<div class="evidence-head"><h2 id="evidenceTitle">固定版本依据</h2><button type="button" id="evidenceClose" class="secondary">关闭</button></div><div id="evidenceBody" aria-live="polite"></div>';
document.body.append(evidenceDialog);
function closeEvidence() { ++evidenceRevision; evidenceDialog.close(); evidenceReturnFocus?.focus(); }
evidenceDialog.querySelector("#evidenceClose").onclick = closeEvidence;
evidenceDialog.addEventListener("cancel", event => { event.preventDefault(); closeEvidence(); });
function evidenceButton(documentID, indexID, id, image = false, metadata = null) {
 const path = `/documents/${documentID}/indexes/${indexID}/${image ? "images" : "chunks"}/${encodeURIComponent(id)}`;
 return `<button type="button" class="secondary evidence-link" data-evidence="${escapeHTML(path)}">${escapeHTML(metadata ? `${image ? "图片" : "原文"} · ${metadata.heading_path?.slice(-1)[0] || metadata.title || "依据"} · L${metadata.start_line}–${metadata.end_line}` : image ? "图片依据" : "文章依据")}</button>`;
}
function sourceLines(source, start, end, full = false) {
 const lines = source.split("\n");
 const first = full ? 1 : Math.max(1, start - 3);
 const last = full ? lines.length : Math.min(lines.length, end + 3);
 return lines.slice(first - 1, last).map((line, i) => `<span class="source-line${i + first >= start && i + first <= end ? " selected" : ""}"><span class="line-number">${i + first}</span>${escapeHTML(line)}</span>`).join("");
}
function displayTextEvidence(mode = "excerpt") {
 const data = evidenceData;
 const chunk = data.chunk;
 let headings = [];
 try { headings = JSON.parse(chunk.heading_path_json || "[]"); } catch (_) { /* legacy paths remain empty */ }
 $("#evidenceBody").innerHTML = `<p><strong>${escapeHTML(data.document_name)}</strong> · 文章原文</p><p>${escapeHTML(headings.join(" / ") || chunk.title || "原文片段")} · 第 ${chunk.start_line}–${chunk.end_line} 行</p><p class="muted">${data.active ? "当前固定版本" : "历史固定版本"} · 原文支持不等于外部事实核实</p><div class="actions"><button type="button" data-evidence-mode="excerpt">相关片段</button><button type="button" class="secondary" data-evidence-mode="context">带行号上下文</button><button type="button" class="secondary" data-evidence-mode="full">完整固定版本</button><button type="button" class="secondary" data-evidence-mode="source">完整源码</button></div>${mode === "context" || mode === "source" ? `<pre class="evidence-source">${sourceLines(data.source, chunk.start_line, chunk.end_line, mode === "source")}</pre>` : `<div class="markdown evidence-reading">${mode === "full" ? data.source_html : data.rendered_html}</div>`}<p class="muted">片段按后端验证的原文块定位；块边界可能截断代码或表格，请用完整版本或源码核对。未建立逐句高亮。</p><details><summary>版本详情</summary><p>索引 ${escapeHTML(data.index_id)} · ${escapeHTML(data.article_sha256)}</p><p>UTF-8 字节 ${data.start_byte}–${data.end_byte}</p></details>`;
}
function imageDescriptionHTML(description, label) {
 if (!description) return "";
 if (typeof description === "string") { try { description = JSON.parse(description); } catch (_) { return `<p>${escapeHTML(label)}：描述格式不可读取</p>`; } }
 return `<h3>${escapeHTML(label)}</h3>${Object.entries(description).map(([key, value]) => `<p><strong>${escapeHTML(({visible_facts:"图中可见事实",interpretation:"模型解释",uncertainties:"不确定点",summary:"概述",text:"图中文字",uncertainty:"不确定点",explanation:"解释",visible_text:"图中文字",observations:"观察",title:"图题",extracted_text:"图中文字",key_points:"关键观察",learning_explanation:"模型解释"})[key] || key)}</strong></p><pre>${escapeHTML(typeof value === "string" ? value : JSON.stringify(value, null, 2))}</pre>`).join("")}`;
}
document.addEventListener("click", async event => {
 const button = event.target.closest("[data-evidence],[data-evidence-mode]");
 if (!button) return;
 if (button.dataset.evidenceMode) { displayTextEvidence(button.dataset.evidenceMode); return; }
 const path = button.dataset.evidence;
 if (!/^\/documents\/\d+\/indexes\/\d+\/(chunks\/[A-Za-z0-9_%.-]+|images\/\d+)$/.test(path)) return;
 const revision = ++evidenceRevision;
 evidenceReturnFocus = button;
 evidenceData = null;
 $("#evidenceBody").textContent = "正在读取固定版本依据…";
 if (!evidenceDialog.open) evidenceDialog.showModal();
 $("#evidenceClose").focus();
 try {
  const data = await request(path);
  if (revision !== evidenceRevision || !evidenceDialog.open) return;
  evidenceData = data;
  if (data.chunk) { displayTextEvidence(); return; }
  const row = data.occurrence;
  const fixedContent = /^\/api\/v1\/images\/\d+\/content$/.test(data.content_url || "") ? data.content_url : null;
  $("#evidenceBody").innerHTML = `<p>固定图片快照 · 第 ${row.start_line}–${row.end_line} 行</p><p>${escapeHTML(row.alt_text || "文章插图")}</p>${data.snapshot_available && fixedContent ? `<a href="${fixedContent}" target="_blank" rel="noopener"><img class="evidence-image" src="${fixedContent}" alt="${escapeHTML(row.alt_text || "固定图片快照")}"></a>` : "<p>尚无可读取快照；请处理图片或查看失败状态。</p>"}${imageDescriptionHTML(data.effective_description || data.description, String(data.effective_description_model || data.description_model || "").includes("fake") ? "演示描述 · 图片理解质量未验证" : "固定版本描述 · 观察与解释须核对原图")}${data.effective_description_model?.includes("user") ? "<p>来源：用户图片修订；模型原始描述保留在历史中。</p>" : ""}<details><summary>原始描述与修订历史</summary>${imageDescriptionHTML(data.description, "原始模型描述")}<pre>${escapeHTML(JSON.stringify(data.corrections || [], null, 2))}</pre></details><details><summary>版本详情</summary><p>索引 ${row.index_id} · 文章 ${escapeHTML(data.article_sha256)}</p><p>原图 ${escapeHTML(data.image_sha256 || "尚无快照")}</p></details>`;
 } catch (error) {
  if (revision === evidenceRevision && evidenceDialog.open) $("#evidenceBody").textContent = `固定版本依据无法读取：${error.message}。请检查资料是否已删除或损坏。`;
 }
});
