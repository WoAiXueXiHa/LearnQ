const api = "/api/v1";
const $ = selector => document.querySelector(selector);
const $$ = selector => [...document.querySelectorAll(selector)];
let lastRecordBody = null;
let taskTimer = null;
let documentTimer = null;

const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, char => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
}[char]));

function showNotice(message, kind = "info") {
  const box = $("#notice");
  box.hidden = false;
  box.className = `notice ${kind}`;
  box.textContent = message;
  clearTimeout(showNotice.timer);
  showNotice.timer = setTimeout(() => box.hidden = true, 6000);
}

async function request(path, options = {}) {
  const response = await fetch(api + path, options);
  const contentType = response.headers.get("content-type") || "";
  const payload = contentType.includes("json") ? await response.json() : await response.text();
  if (!response.ok) {
    const error = payload?.error;
    throw new Error(error ? `${error.message}（${error.code}）` : String(payload));
  }
  return payload.data;
}

function switchView(id) {
  $$("main > section").forEach(section => section.hidden = section.id !== id);
  $$(".tabs button").forEach(button => button.classList.toggle("active", button.dataset.view === id));
  if (id === "records") loadRecords();
  if (id === "reviews") loadReviews();
  if (id === "knowledge") loadDocuments();
}

$$(".tabs button").forEach(button => button.onclick = () => switchView(button.dataset.view));

async function health() {
  try {
    await fetch("/health/ready", {cache: "no-store"}).then(response => {
      if (!response.ok) throw new Error();
    });
    $("#health").textContent = "服务正常";
    $("#health").className = "badge success";
  } catch {
    $("#health").textContent = "服务不可用";
    $("#health").className = "badge danger";
  }
}

function recordPayload(forceCreate = false) {
  const form = new FormData($("#recordForm"));
  return {
    title: form.get("title").trim(),
    summary: form.get("summary").trim(),
    duration_minutes: Number(form.get("duration")),
    force_create: forceCreate,
    modules: [{category: form.get("category"), content: form.get("content").trim()}]
  };
}

async function submitRecord(forceCreate = false) {
  const button = $("#recordSubmit");
  button.disabled = true;
  button.textContent = "正在创建…";
  try {
    const body = recordPayload(forceCreate);
    lastRecordBody = body;
    const operationKey = sessionStorage.getItem("learnq-record-operation") || crypto.randomUUID();
    sessionStorage.setItem("learnq-record-operation", operationKey);
    const data = await request("/study-records", {
      method: "POST",
      headers: {"Content-Type": "application/json", "Idempotency-Key": operationKey},
      body: JSON.stringify(body)
    });
    $("#duplicateBox").hidden = !data.deduplicated;
    sessionStorage.removeItem("learnq-record-operation");
    showNotice(data.deduplicated ? "发现相同记录，已打开原任务。" : "学习记录已创建，正在生成报告。", data.deduplicated ? "warning" : "success");
    await loadRecords();
    if (data.deduplicated && !forceCreate) {
      $("#duplicateBox").scrollIntoView({behavior: "smooth"});
    } else {
      switchView("task");
      $("#taskID").value = data.task_id;
      openTask(data.task_id);
    }
  } catch (error) {
    showNotice(error.message, "danger");
  } finally {
    button.disabled = false;
    button.textContent = "创建学习报告";
  }
}

$("#recordForm").onsubmit = event => {
  event.preventDefault();
  submitRecord(false);
};
$("#forceCreate").onclick = () => submitRecord(true);

async function loadRecords() {
  try {
    const records = await request("/study-records");
    $("#recordList").innerHTML = records.length ? records.map(record => `
      <article class="card clickable" onclick="switchView('task');openTask(${record.task_id})">
        <div class="card-title"><strong>${escapeHTML(record.title)}</strong><span class="status ${record.task_status}">${escapeHTML(record.task_status || "未知")}</span></div>
        <p>${escapeHTML(record.summary || "未填写摘要")}</p>
        <small>${record.duration_minutes} 分钟 · ${new Date(record.created_at).toLocaleString()} · Task #${record.task_id}</small>
      </article>`).join("") : `<div class="empty">还没有学习记录。</div>`;
  } catch (error) {
    $("#recordList").innerHTML = `<div class="empty danger">${escapeHTML(error.message)}</div>`;
  }
}

function renderMarkdown(markdown) {
  return escapeHTML(markdown || "")
    .replace(/^### (.+)$/gm, "<h4>$1</h4>")
    .replace(/^## (.+)$/gm, "<h3>$1</h3>")
    .replace(/^# (.+)$/gm, "<h2>$1</h2>")
    .replace(/\n\n/g, "</p><p>")
    .replace(/\n/g, "<br>");
}

function renderTrace(trace) {
  const runs = trace?.agent_runs || [];
  const tools = trace?.tool_calls || [];
  return `<div class="card"><h3>Agent 执行轨迹</h3>
    ${runs.length ? runs.map(run => `<div class="trace-row"><strong>${escapeHTML(run.skill_name)}</strong><span>${escapeHTML(run.model_name)}</span><small>${run.latency_ms} ms · 输入 ${run.input_tokens} / 输出 ${run.output_tokens} tokens</small></div>`).join("") : `<p class="muted">Agent 尚未运行。</p>`}
    ${tools.length ? `<h4>真实工具调用</h4>${tools.map(tool => `<details><summary>${escapeHTML(tool.tool_name)} · ${tool.latency_ms} ms ${tool.error_reason ? "· 失败" : ""}</summary><pre>${escapeHTML(tool.response_json)}</pre>${tool.retrieval_citations_json !== "[]" ? `<pre>${escapeHTML(tool.retrieval_citations_json)}</pre>` : ""}</details>`).join("")}` : ""}
  </div>`;
}

async function openTask(id) {
  if (!id) return;
  clearTimeout(taskTimer);
  $("#taskID").value = id;
  try {
    const data = await request(`/tasks/${id}/detail`, {cache: "no-store"});
    const task = data.task;
    const record = data.study_record;
    const report = data.report;
    const review = data.review_task;
    $("#taskDetail").className = "stack";
    $("#taskDetail").innerHTML = `
      <article class="card">
        <div class="card-title"><h3>${escapeHTML(record?.title || task.kind)}</h3><span class="status ${task.status}">${escapeHTML(task.status)}</span></div>
        <p>${escapeHTML(record?.summary || "任务处理中")}</p>
        <div class="timeline"><span>pending</span><span>queued</span><span>processing</span><span>succeeded</span></div>
        ${task.last_error ? `<p class="danger">${escapeHTML(task.last_error)}</p>` : ""}
        ${task.status === "dead" ? `<button onclick="retryTask(${task.id})">重新执行</button>` : ""}
      </article>
      ${report ? `<article class="card report"><div class="card-title"><h3>生成报告</h3><span class="badge ${report.export_status === "succeeded" ? "success" : "warning"}">导出 ${escapeHTML(report.export_status)}</span></div><div class="markdown"><p>${renderMarkdown(report.markdown_content)}</p></div>${report.export_error ? `<p class="danger">${escapeHTML(report.export_error)}</p>` : ""}</article>` : `<div class="card empty">报告生成中，页面会自动刷新。</div>`}
      ${review ? `<article class="card"><h3>复习安排</h3><p>下一次：${new Date(review.due_at).toLocaleString()}</p><small>当前熟练度 ${review.mastery}/5</small></article>` : ""}
      ${renderTrace(data.trace)}`;
    if (!["succeeded", "dead"].includes(task.status)) taskTimer = setTimeout(() => openTask(id), 1000);
  } catch (error) {
    $("#taskDetail").innerHTML = `<div class="card danger">${escapeHTML(error.message)}</div>`;
  }
}

async function retryTask(id) {
  try {
    await request(`/tasks/${id}/retry`, {method: "POST"});
    showNotice("任务已重新进入队列。", "success");
    openTask(id);
  } catch (error) { showNotice(error.message, "danger"); }
}

function reviewCard(review, due) {
  return `<article class="card">
    <div class="card-title"><strong>${escapeHTML(review.record_title)}</strong><span class="badge ${due ? "danger" : "warning"}">${due ? "已到期" : new Date(review.due_at).toLocaleString()}</span></div>
    <p>${escapeHTML(String(review.markdown_content || "").replace(/^#+/gm, "").slice(0, 160))}</p>
    ${due ? `<label>本次掌握程度<select id="mastery-${review.id}">${[0,1,2,3,4,5].map(value => `<option value="${value}">${value}/5</option>`).join("")}</select></label><div class="actions"><button onclick="completeReview(${review.id})">完成复习</button><button class="secondary" onclick="skipReview(${review.id})">延后一天</button></div>` : `<small>将在 ${relativeTime(review.due_at)}后到期</small>`}
  </article>`;
}

function relativeTime(date) {
  const hours = Math.max(0, Math.round((new Date(date) - Date.now()) / 36e5));
  return hours < 24 ? `${hours} 小时` : `${Math.round(hours / 24)} 天`;
}

async function loadReviews() {
  try {
    const [due, upcoming] = await Promise.all([request("/review-tasks?scope=due"), request("/review-tasks?scope=upcoming")]);
    $("#dueList").innerHTML = due.length ? due.map(item => reviewCard(item, true)).join("") : `<div class="empty">现在没有到期复习。</div>`;
    $("#upcomingList").innerHTML = upcoming.length ? upcoming.map(item => reviewCard(item, false)).join("") : `<div class="empty">报告生成后，复习计划会出现在这里。</div>`;
  } catch (error) { showNotice(error.message, "danger"); }
}

async function completeReview(id) {
  const mastery = Number($(`#mastery-${id}`).value);
  try {
    const result = await request(`/review-tasks/${id}/complete`, {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({mastery})});
    showNotice(`复习已完成，下次时间：${new Date(result.due_at).toLocaleString()}`, "success");
    loadReviews();
  } catch (error) { showNotice(error.message, "danger"); }
}
async function skipReview(id) {
  try {
    await request(`/review-tasks/${id}/skip`, {method: "POST", headers: {"Content-Type": "application/json"}, body: "{}"});
    showNotice("已延后一天。", "success"); loadReviews();
  } catch (error) { showNotice(error.message, "danger"); }
}

const skillExamples = {
  "daily-review": {title: "Go 并发学习", summary: "学习 goroutine、channel 和 context", duration_minutes: 30},
  "algorithm-diagnosis": {topic: "二分搜索边界", question: "如何避免左右边界错误？"},
  "interview-followup": {topic: "MySQL 事务与 Outbox", summary: "希望生成递进面试追问"},
  "project-explanation": {title: "LearnQ", summary: "异步任务、lease fencing、RAG"},
  "weekly-plan": {title: "本周计划", summary: "请基于 SQL 周统计组织下周行动"}
};

function updateSkillInput() {
  $("#skillInput").value = JSON.stringify(skillExamples[$("#skillName").value] || {}, null, 2);
}

$("#skillForm").onsubmit = async event => {
  event.preventDefault();
  const name = $("#skillName").value;
  try {
    JSON.parse($("#skillInput").value);
    const data = await request(`/skills/${name}/runs`, {method: "POST", headers: {"Content-Type": "application/json"}, body: $("#skillInput").value});
    const output = data.output;
    $("#skillResult").className = "stack";
    $("#skillResult").innerHTML = `<article class="card report"><div class="card-title"><h3>${escapeHTML(output.title)}</h3><span class="badge success">Run #${data.agent_run_id}</span></div><p>${escapeHTML(output.summary)}</p>${output.sections.map(section => `<h4>${escapeHTML(section.heading)}</h4><p>${escapeHTML(section.content)}</p>`).join("")}</article>`;
    const detail = await request(`/agent-runs/${data.agent_run_id}`);
    $("#skillResult").insertAdjacentHTML("beforeend", renderTrace({agent_runs: [detail.run], tool_calls: detail.tool_calls}));
  } catch (error) {
    $("#skillResult").innerHTML = `<div class="card danger">${escapeHTML(error.message)}</div>`;
  }
};

$("#uploadForm").onsubmit = async event => {
  event.preventDefault();
  const form = new FormData();
  form.append("file", $("#documentFile").files[0]);
  try {
    const data = await request("/documents", {method: "POST", body: form});
    showNotice(`文档 #${data.document_id} 已上传，正在索引。`, "success");
    loadDocuments(true);
  } catch (error) { showNotice(error.message, "danger"); }
};

async function loadDocuments(keepPolling = false) {
  clearTimeout(documentTimer);
  try {
    const documents = await request("/documents", {cache: "no-store"});
    const ready = documents.some(document => document.status === "ready");
    const active = documents.some(document => ["uploaded", "parsing", "embedding", "indexing"].includes(document.status));
    $("#ragSubmit").disabled = !ready;
    $("#documentList").innerHTML = documents.length ? documents.map(document => `
      <article class="card"><div class="card-title"><strong>${escapeHTML(document.filename)}</strong><span class="status ${document.status}">${escapeHTML(document.status)}</span></div>
      <small>Document #${document.id} · Task #${document.indexing_task_id}</small>
      ${document.error_message ? `<p class="danger">${escapeHTML(document.error_message)}</p>` : ""}
      <div class="actions">${document.status === "failed" ? `<button onclick="retryTask(${document.indexing_task_id});loadDocuments(true)">重试索引</button>` : ""}<button class="secondary" onclick="deleteDocument(${document.id})">删除</button></div></article>`).join("") : `<div class="empty">还没有文档。上传 Markdown、TXT 或 JSON 后才能进行 RAG 查询。</div>`;
    if (active || keepPolling) documentTimer = setTimeout(() => loadDocuments(false), 1000);
  } catch (error) { showNotice(error.message, "danger"); }
}

async function deleteDocument(id) {
  if (!confirm("确认删除文档及其向量索引？")) return;
  try { await request(`/documents/${id}`, {method: "DELETE"}); showNotice("文档已删除。", "success"); loadDocuments(); }
  catch (error) { showNotice(error.message, "danger"); }
}

$("#ragForm").onsubmit = async event => {
  event.preventDefault();
  try {
    const data = await request("/rag/query", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({question: $("#question").value, top_k: 5})});
    $("#ragResult").className = "stack";
    $("#ragResult").innerHTML = `<article class="card report"><h3>证据回答</h3><p>${escapeHTML(data.answer)}</p></article>
      <div class="cards">${data.citations.map(citation => `<article class="card citation"><div class="card-title"><strong>[${escapeHTML(citation.source)}] ${escapeHTML(citation.title)}</strong><span>${Number(citation.score).toFixed(3)}</span></div><p>${escapeHTML(citation.summary)}</p><small>文档 #${citation.document_id} · 行 ${citation.start_line}–${citation.end_line} · Chunk ${escapeHTML(citation.chunk_id)}</small></article>`).join("")}</div>`;
  } catch (error) {
    $("#ragResult").innerHTML = `<div class="card danger">${escapeHTML(error.message)}</div>`;
  }
};

fetch(api + "/skills").then(response => response.json()).then(payload => {
  payload.data.forEach(skill => $("#skillName").add(new Option(skill.description, skill.name)));
  updateSkillInput();
});
$("#skillName").onchange = updateSkillInput;

health();
loadRecords();
setInterval(health, 15000);
