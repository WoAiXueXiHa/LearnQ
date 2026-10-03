const practiceLabels = {ready:"已就绪",archived:"已归档",failed:"处理失败",generating:"正在生成",draft:"待编辑确认",confirmed:"已确认",in_progress:"作答中",submitted:"已提交",feedback_pending:"正在生成反馈",feedback_ready:"反馈已生成",feedback_failed:"反馈生成失败",pending:"等待处理",scheduled:"等待复习",completed:"复习已完成",accepted:"已核对接受",disputed:"已标记问题",uncertain:"仍不确定",expression:"表达建议",omission:"需要补充",conflict:"疑似冲突",article_only:"原文支持（未外部核实）",pending_verification:"待核查",unable_to_judge:"暂无法判断",pending_snapshot:"等待下载",download_failed:"下载失败",skipped:"已明确跳过",limit_pending:"超过图片数量限制",deleting:"正在清理",processing:"处理中",indexing:"正在索引",uploaded:"已上传",parsing:"正在解析",embedding:"正在索引",snapshot_ready:"快照已保存",unsupported_html_image:"不支持 HTML 图片"};
const practiceLabel = value => practiceLabels[value] || "状态待确认，请查看诊断页";
function updatePracticeStep(step) {
 $("#practiceSteps").innerHTML = ["选择文章","资料准备","确认五题","作答","反馈与复习"].map((title,index)=>`<li ${index === step ? 'aria-current="step"' : ""}>${index+1}. ${title}</li>`).join("");
}
function updateAnswerProgress() {
 const fields = practiceAnswers();
 const saved = practiceState.attempt?.answers || [];
 const changed = fields.some((answer,index)=>answer !== saved[index]);
 const node = $("#practiceAnswerProgress");
 if (node) node.textContent = `已答 ${fields.filter(answer=>answer.trim()).length}/5 · ${changed ? "有未保存输入" : "草稿已保存"}`;
}
function renderPracticeFeedback(data, f, j) {
 const ordinal = f.feedback.ordinal;
 const index = ordinal - 1;
 const structured = data.reference_items?.[index];
 const shared = data.reference_evidence?.[index] || {};
 const links = (chunks, images) => (chunks || []).map(id => evidenceButton(data.document_id,data.index_id,id,false,data.reference_metadata?.[`chunk:${id}`])).join(" ") + (images || []).map(id => evidenceButton(data.document_id,data.index_id,id,true,data.reference_metadata?.[`image:${id}`])).join(" ");
 const items = f.items.map(item=>`<div class="feedback-item"><strong>${escapeHTML(practiceLabel(item.type))} · ${escapeHTML(practiceLabel(item.judgment_status))}</strong><p>${escapeHTML(item.explanation)}</p>${links(item.chunk_ids,item.image_ref_ids)}</div>`).join("");
 const points = structured?.length ? structured.map(item=>`<li>${escapeHTML(item.text)}<div>${links(item.chunk_ids,item.image_ref_ids)}</div></li>`).join("") : (data.reference_points?.[index] || []).map(point=>`<li>${escapeHTML(point)}</li>`).join("");
 return `<article class="card feedback-card"><h3>第 ${ordinal} 题反馈 · ${escapeHTML(f.feedback.status === "ready" ? "反馈已生成" : practiceLabel(f.feedback.status))}</h3><p>${escapeHTML(data.questions[index]?.prompt || "")}</p><details><summary>我的回答</summary><p class="practice-answer-text">${escapeHTML(data.answers[index] || "")}</p></details>${String(f.feedback.model || "").includes("fake") ? '<p class="muted">演示反馈 · 不判断回答正确性</p>' : ""}${items}<p>${escapeHTML(f.feedback.last_error || "")}</p><details class="reference-section"><summary>参考要点</summary>${structured?.length ? "" : '<p class="muted">历史参考要点，未建立逐项关联；以下仅为共同依据。</p>'}<ul class="reference-points">${points}</ul>${structured?.length ? "" : links(shared.chunk_ids,shared.image_ref_ids)}</details>${f.feedback.status === "failed" ? `<button data-task-retry="${f.feedback.task_id}">只重试这一题</button>` : ""}${(f.corrections || []).map(c=>`<p>历史意见：${escapeHTML(practiceLabel(c.disposition))} · ${escapeHTML(c.comment)}</p>`).join("")}${f.feedback.status === "ready" ? `<div class="actions"><button class="secondary" data-correction="${f.feedback.id}" data-disposition="disputed">标记反馈有问题</button><details><summary>更多操作</summary><div class="actions"><button class="secondary" data-correction="${f.feedback.id}" data-disposition="uncertain">仍不确定</button><button class="secondary" data-correction="${f.feedback.id}" data-disposition="accepted">核对后接受反馈</button>${f.items.some(item=>item.type === "conflict") ? `<button class="secondary" type="button" data-feedback-claims="${f.feedback.id}">核查冲突依据</button>` : ""}</div></details></div>` : ""}</article>`;
}
/* Article practice keeps answer drafts local until explicit save or submit. */
const practiceState = {document: null, set: null, attempt: null, timer: null, revision: 0};
const practiceJSON = (method, body) => ({method, headers: {"Content-Type": "application/json"}, body: JSON.stringify(body)});
const practiceError = error => showNotice(error.message, "danger");

function alignPracticeArticle(documentId, indexId) {
  if (practiceState.document?.id === documentId && practiceState.document?.active_index_id === indexId) return;
  practiceState.document = null;
  $("#practiceImages").innerHTML = `<p>当前练习属于文章 ${escapeHTML(documentId)} 的固定索引 ${escapeHTML(indexId)}。如需生成新题，请在文章列表选择文章。</p>`;
}

let practiceRefreshRevision = 0;
async function refreshPractice() {
  const refreshRevision = ++practiceRefreshRevision;
  try {
    const documents = await request("/documents");
    if (refreshRevision !== practiceRefreshRevision) return;
    $("#practiceArticles").innerHTML = documents.filter(d => ["md", ".md", "text/markdown"].includes(d.media_type)).map(d => `<article class="card"><h3>${escapeHTML(d.filename)}</h3><p>${escapeHTML(practiceLabel(d.status))}</p><button data-document="${d.id}" ${d.active_index_id ? "" : "disabled"}>选择文章</button><a href="/api/v1/documents/${d.id}/export">导出文章与练习记录</a>${d.status === "archived" ? `<button type="button" data-restore-document="${d.id}">恢复文章</button>` : d.status === "ready" ? `<button type="button" data-archive-document="${d.id}">归档，保留历史</button>` : ""}<button type="button" data-deletion-document="${d.id}">${d.status === "deleting" ? "查看清理进度" : "彻底删除：先查看影响"}</button></article>`).join("") || '<p class="empty">上传一篇完整讲述知识点的 Markdown 文章。</p>';
    const reviews = await request("/practice-reviews");
    if (refreshRevision !== practiceRefreshRevision) return;
    $("#practiceReviews").innerHTML = reviews.map(r => `<article class="card"><p>题组 ${r.question_set_id} · ${escapeHTML(new Date(r.due_at).toLocaleString())}</p><span>${escapeHTML(practiceLabel(r.status))}</span>${r.status === "scheduled" ? `<button data-review="${r.id}">开始重练</button>` : r.completed_attempt_id ? `<button data-open-attempt="${r.completed_attempt_id}">${r.status === "completed" ? "查看重练结果" : "继续重练"}</button>` : ""}</article>`).join("") || '<p class="empty">尚未安排复习。</p>';
	const [sets, attempts] = await Promise.all([request("/question-sets"), request("/practice-attempts")]);
	if (refreshRevision !== practiceRefreshRevision) return;
	$("#practiceHistory").innerHTML = sets.map(s => `<article class="card"><p>题组 ${s.id} · ${escapeHTML(practiceLabel(s.status))}</p><button data-open-set="${s.id}">打开题组</button></article>`).join("") + attempts.map(a => `<article class="card"><p>作答 ${a.id} · ${escapeHTML(practiceLabel(a.status))}</p><button data-open-attempt="${a.id}">继续作答 / 查看反馈</button></article>`).join("");
  } catch (error) { practiceError(error); }
}

async function selectPracticeArticle(id) {
  clearTimeout(practiceState.timer);
  resetAuthorityPanel();
  practiceState.document = null;
  $("#practiceImages").innerHTML = "";
  const revision = ++practiceState.revision;
	practiceState.set = null;
	practiceState.attempt = null;
	$("#practiceSet").innerHTML = "";
	$("#practiceAttempt").innerHTML = "";
  try {
    const documents = await request("/documents");
    if (revision !== practiceState.revision) return;
    const document = documents.find(d => d.id === id);
    if (!document?.active_index_id) throw new Error("文章索引尚未完成，请稍后刷新。");
    practiceState.document = document;
 updatePracticeStep(1);
    const data = await request(`/documents/${id}/indexes/${document.active_index_id}/images`);
    if (revision !== practiceState.revision) return;
    const readyCount = data.images.filter(row=>row.status === "skipped" || (row.image_id && data.snapshots[row.image_id]?.status === "ready" && data.image_indexes[row.id]?.status === "ready")).length;
    $("#practiceImages").innerHTML = `<article class="card practice-images"><h3>${escapeHTML(document.filename)}：资料准备</h3><p>${data.images.length} 张图片 · ${readyCount} 张已处理或明确跳过</p><details ${data.image_evidence_complete ? "" : "open"}><summary>查看图片与处理状态</summary>${data.images.map(row => {
      const snapshot = row.image_id ? data.snapshots[row.image_id] : null;
      const ready = snapshot?.status === "ready" && data.image_indexes[row.id]?.status === "ready";
      const demo = String(snapshot?.description_model || "").includes("fake");
      return `<div class="card"><p>${escapeHTML(row.alt_text || "文章插图")} · 第 ${row.start_line} 行 · ${escapeHTML(ready ? "证据已保存" : practiceLabel(row.status))}</p>${snapshot ? `<img class="image-thumb" src="/api/v1/images/${snapshot.id}/content" alt="${escapeHTML(row.alt_text || "固定图片快照")}" loading="lazy">` : ""}${evidenceButton(document.id,document.active_index_id,row.id,true)}${!ready && row.status !== "skipped" ? `<button data-image-process="${row.id}">处理 / 重试下载</button><button data-image-skip="${row.id}" class="secondary">明确跳过</button>` : ""}${snapshot?.status === "ready" ? `<button class="secondary" type="button" data-correct-image="${row.id}">修订图片描述</button>` : ""}${snapshot?.status === "failed" ? `<button data-image-retry="${snapshot.id}">重试描述</button>` : ""}<p class="muted">${demo ? "图片快照已保存，描述为演示内容，理解质量未验证。" : ""}</p><p>${escapeHTML(row.last_error || snapshot?.last_error || data.image_indexes[row.id]?.last_error || "")}</p></div>`;
    }).join("") || "<p>文章没有外链图片。</p>"}</details><p>${data.image_evidence_complete ? "资料处理流程已完成，可以生成五题。" : "图片尚未全部完成，请稍后刷新，或明确跳过装饰图。"}</p><button id="practiceGenerate" ${data.image_evidence_complete ? "" : "disabled"}>生成新的五题</button></article>`;
  } catch (error) { practiceError(error); }
}

async function openPracticeSet(id) {
  clearTimeout(practiceState.timer);
  resetAuthorityPanel();
  const revision = ++practiceState.revision;
  try {
    const data = await request(`/question-sets/${id}`);
    if (revision !== practiceState.revision) return;
    practiceState.set = {id, ...data};
 updatePracticeStep(2);
 $("#practiceAttempt").innerHTML = "";
 practiceState.attempt = null;
    alignPracticeArticle(data.question_set.document_id, data.question_set.index_id);
    $("#practiceSet").innerHTML = `<article class="card"><h3>五题预览 · ${escapeHTML(practiceLabel(data.question_set.status))}</h3>${data.questions.map((q, j) => `<label>第 ${j + 1} 题<textarea data-question="${j}" ${data.question_set.status === "draft" ? "" : "readonly"}>${escapeHTML(q.prompt)}</textarea></label>`).join("")}<p>${escapeHTML(data.question_set.last_error || "")}</p><p class="muted">编辑题干会保留原参考依据。若改变了考查主题，请重新生成题组；系统不会因编辑自动调用模型。</p>${data.question_set.status === "draft" ? '<button id="practiceSaveSet">保存编辑</button><button id="practiceConfirm">确认五题</button>' : ""}${data.question_set.status === "confirmed" ? '<button id="practiceStart">开始作答 / 再次练习</button>' : ""}${data.question_set.status === "failed" ? `<button data-task-retry="${data.question_set.generation_task_id}">重试生成</button>` : ""}</article>`;
    if (data.question_set.status === "generating" && currentView === "practice") practiceState.timer = setTimeout(() => openPracticeSet(id), 2000);
  } catch (error) { practiceError(error); }
}

async function savePracticeSet(confirm) {
  const revision = practiceState.revision;
  const state = practiceState.set;
  const questions = state.questions.map((q, j) => ({...q, prompt: $(`[data-question="${j}"]`).value}));
  await request(`/question-sets/${state.id}${confirm ? "/confirm" : ""}`, practiceJSON(confirm ? "POST" : "PUT", {questions}));
  if (revision === practiceState.revision) await openPracticeSet(state.id);
}

function practiceAnswers() { return $$('[data-answer]').map(element => element.value); }

async function openPracticeAttempt(id, preserveDraft = false) {
  clearTimeout(practiceState.timer);
  resetAuthorityPanel();
  const revision = ++practiceState.revision;
  try {
    const data = await request(`/practice-attempts/${id}`);
    if (revision !== practiceState.revision) return;
    const localAnswers = preserveDraft && practiceState.attempt?.attempt.id === id ? practiceAnswers() : null;
    practiceState.attempt = data;
    alignPracticeArticle(data.document_id, data.index_id);
    const editing = data.attempt.status === "in_progress";
 updatePracticeStep(editing ? 3 : 4);
 $("#practiceSet").innerHTML = "";
 const imageDetails = $("#practiceImages details"); if (imageDetails) imageDetails.open = false;
    const answers = localAnswers?.length === 5 ? localAnswers : data.answers;
    if (editing && localAnswers?.length === 5 && localAnswers.some((answer, index) => answer !== data.answers[index])) {
      showNotice("保存等待期间的新输入已保留在页面，尚未保存，请再次保存草稿。");
    }
    $("#practiceAttempt").innerHTML = `<article class="card"><h3>五题作答 · ${escapeHTML(practiceLabel(data.attempt.status))}</h3>${editing ? '<p id="practiceAnswerProgress" class="practice-progress" aria-live="polite"></p>' : '<p class="muted">已提交的回答保持冻结；参考要点与反馈在下方。</p>'}${editing ? "" : "<details><summary>回看全部已提交回答</summary>"}${data.questions.map((q,j)=>`<label>${j+1}. ${escapeHTML(q.prompt)}<textarea data-answer="${j}" ${editing ? "" : "readonly"}>${escapeHTML(answers[j] || "")}</textarea></label>`).join("")}${editing ? "" : "</details>"}<div class="actions">${editing ? '<button id="practiceSaveAnswers" class="secondary">保存草稿</button><button id="practiceSubmit">提交全部五题</button>' : `<button id="practiceReloadAttempt" class="secondary">刷新反馈</button><button id="practiceSchedule" ${data.attempt.status === "feedback_ready" ? "" : "disabled"}>安排复习</button>`}</div></article>${(data.feedback || []).map((f,j)=>renderPracticeFeedback(data,f,j)).join("")}`;
    if (editing) updateAnswerProgress();
    if (data.attempt.status === "feedback_pending" && currentView === "practice") practiceState.timer = setTimeout(() => openPracticeAttempt(id), 2000);
  } catch (error) { practiceError(error); }
}

$("#practiceUpload").onsubmit = async event => {
  event.preventDefault();
  const button = event.submitter;
  setButtonBusy(button, true);
  try {
    const file = $("#practiceFile").files[0];
    if (!file || !file.name.toLowerCase().endsWith(".md")) throw new Error("请选择 Markdown 文件。");
    const form = new FormData(); form.append("file", file);
    await request("/documents", {method: "POST", body: form}, 30000);
    showNotice("文章已上传，索引完成后刷新并选择文章。", "success");
    await refreshPractice();
  } catch (error) { practiceError(error); }
  finally { setButtonBusy(button, false); }
};

$("#practiceRefresh").onclick = async () => {
  await refreshPractice();
  if (practiceState.document) await selectPracticeArticle(practiceState.document.id);
};

function focusPracticeContent(selector) {
  const panel = $(selector);
  const heading = panel.querySelector("h3");
  if (!heading) return;
  heading.tabIndex = -1;
  heading.focus({preventScroll: true});
  panel.scrollIntoView({block: "start", behavior: "instant"});
}

$("#practice").addEventListener("click", async event => {
  const button = event.target.closest("button");
  if (!button || button.closest("#practiceUpload") || button.id === "practiceRefresh") return;
  if (!button.matches("[data-document],[data-archive-document],[data-restore-document],[data-open-set],[data-open-attempt],[data-image-process],[data-image-skip],[data-image-retry],[data-task-retry],[data-correction],[data-review],#practiceGenerate,#practiceSaveSet,#practiceConfirm,#practiceStart,#practiceSaveAnswers,#practiceSubmit,#practiceReloadAttempt,#practiceSchedule")) return;
  setButtonBusy(button, true);
  try {
    const revision = practiceState.revision;
    const stillCurrent = () => revision === practiceState.revision && currentView === "practice";
    const document = practiceState.document;
    const selectedAttempt = practiceState.attempt;
    const selectedSet = practiceState.set;
    const imageBase = document ? `/documents/${document.id}/indexes/${document.active_index_id}/images` : "";
    if (button.dataset.document) await selectPracticeArticle(Number(button.dataset.document));
 else if (button.dataset.archiveDocument) { await request(`/documents/${button.dataset.archiveDocument}/archive`, {method: "POST"}); await refreshPractice(); }
 else if (button.dataset.restoreDocument) { await request(`/documents/${button.dataset.restoreDocument}/restore`, {method: "POST"}); await refreshPractice(); }
	else if (button.dataset.openSet) { practiceState.attempt = null; $("#practiceAttempt").innerHTML = ""; await openPracticeSet(Number(button.dataset.openSet)); focusPracticeContent("#practiceSet"); }
	else if (button.dataset.openAttempt) { await openPracticeAttempt(Number(button.dataset.openAttempt)); focusPracticeContent("#practiceAttempt"); }
    else if (button.dataset.imageProcess) { await request(`${imageBase}/${button.dataset.imageProcess}/process`, {method: "POST"}, 30000); if (stillCurrent()) await selectPracticeArticle(document.id); }
    else if (button.dataset.imageSkip) { const reason = prompt("请说明为什么跳过这张图片（例如：纯装饰图）"); if (reason) { await request(`${imageBase}/${button.dataset.imageSkip}/skip`, practiceJSON("POST", {reason})); if (stillCurrent()) await selectPracticeArticle(document.id); } }
    else if (button.dataset.imageRetry) { await request(`/images/${button.dataset.imageRetry}/retry`, {method: "POST"}); if (stillCurrent()) await selectPracticeArticle(document.id); }
    else if (button.id === "practiceGenerate") { const set = await request(`/documents/${document.id}/question-sets`, {method: "POST"}); if (stillCurrent()) await openPracticeSet(set.id); }
    else if (button.id === "practiceSaveSet") await savePracticeSet(false);
    else if (button.id === "practiceConfirm") await savePracticeSet(true);
    else if (button.id === "practiceStart") { const attempt = await request(`/question-sets/${selectedSet.id}/attempts`, {method: "POST"}); if (stillCurrent()) await openPracticeAttempt(attempt.id); }
    else if (["practiceSaveAnswers", "practiceSubmit"].includes(button.id)) { const submit = button.id === "practiceSubmit"; const id = selectedAttempt.attempt.id; await request(`/practice-attempts/${id}/${submit ? "submit" : "answers"}`, practiceJSON(submit ? "POST" : "PUT", {answers: practiceAnswers()})); if (stillCurrent()) await openPracticeAttempt(id, !submit); }
    else if (button.id === "practiceReloadAttempt") await openPracticeAttempt(practiceState.attempt.attempt.id, true);
    else if (button.dataset.taskRetry) { await request(`/tasks/${button.dataset.taskRetry}/retry`, {method: "POST"}); if (stillCurrent()) { if (selectedAttempt) await openPracticeAttempt(selectedAttempt.attempt.id); else if (selectedSet) await openPracticeSet(selectedSet.id); } }
    else if (button.dataset.correction) { const comment = prompt("说明接受依据、具体误判或仍未确定的条件；原反馈和历史意见会保留。"); if (comment) { await request(`/answer-feedback/${button.dataset.correction}/corrections`, practiceJSON("POST", {disposition: button.dataset.disposition, comment})); showNotice("纠错意见已追加保存。"); if (stillCurrent() && selectedAttempt) await openPracticeAttempt(selectedAttempt.attempt.id); } }
    else if (button.id === "practiceSchedule") { await request(`/practice-attempts/${practiceState.attempt.attempt.id}/review`, practiceJSON("POST", {due_at: new Date(Date.now() + 86400000).toISOString()})); await refreshPractice(); }
    else if (button.dataset.review) { const attempt = await request(`/practice-reviews/${button.dataset.review}/start`, {method: "POST"}); if (stillCurrent()) await openPracticeAttempt(attempt.id); }
  } catch (error) { practiceError(error); }
  finally { setButtonBusy(button, false); }
});

$("#practice").addEventListener("input", event=> { if (event.target.matches("[data-answer]")) updateAnswerProgress(); });
request("/model-budget").then(data=> { $("#practiceMode").textContent = data.mode === "fake" ? "当前为演示模式：五题、图片描述与反馈仅用于验证流程，不分析文章或判断回答质量。" : "当前使用外部模型；请结合原文核对题目与反馈依据。"; }).catch(practiceError);
updatePracticeStep(0);
$("#tab-practice").onclick = () => { switchView("practice"); refreshPractice(); };
switchView("practice", false);
refreshPractice();
